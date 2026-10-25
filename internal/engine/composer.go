package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/brandnewteam/presto/internal/config"
)

// Container paths are fixed so host paths, including Windows drive paths, never leak
// into Composer's environment inside the image.
const containerWorkspace, containerCache = "/presto/work", "/presto/cache"

// workspaceDir uses the system temporary directory, except for Docker: macOS VMs such
// as Colima share only the home directory, so bind mounts start below the user cache.
func workspaceDir(docker bool) (string, error) {
	if !docker {
		return os.MkdirTemp("", "presto-prepare-")
	}
	cache, err := dockerCacheDir()
	if err != nil {
		return "", err
	}
	parent := filepath.Join(cache, "work")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, "prepare-")
}

func dockerCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("Docker Composer preparation needs a user cache directory: %w", err)
	}
	return filepath.Join(cache, "presto", "docker"), nil
}

func composerArgs(settings config.Composer) []string {
	args := append([]string{}, settings.Command[1:]...)
	args = append(args, "install", "--no-dev", "--prefer-dist", "--optimize-autoloader", "--no-interaction", "--no-progress")
	if settings.Scripts == nil || !*settings.Scripts {
		args = append(args, "--no-scripts")
	}
	if settings.Plugins == nil || !*settings.Plugins {
		args = append(args, "--no-plugins")
	}
	return args
}

// composerCommand runs Composer in workspace/name, on the host or in the configured image.
func composerCommand(ctx context.Context, settings config.Composer, workspace, name, vendorDir, binDir string) (*exec.Cmd, error) {
	if image := settings.Docker(); image != "" {
		return dockerComposerCommand(ctx, settings, image, workspace, name, vendorDir, binDir)
	}
	root := filepath.Join(workspace, name)
	executable, args := settings.Command[0], composerArgs(settings)
	// The standard Windows Composer installer uses a .bat wrapper around a
	// sibling composer.phar. Invoke PHP directly to keep argument handling shell-free.
	if runtime.GOOS == "windows" && executable == "composer" {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return nil, err
		}
		ext := strings.ToLower(filepath.Ext(resolved))
		if ext == ".bat" || ext == ".cmd" {
			phar := filepath.Join(filepath.Dir(resolved), "composer.phar")
			if info, err := os.Stat(phar); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("Windows Composer wrapper has no adjacent composer.phar; configure composer.command = ['php', 'C:/path/to/composer.phar']")
			}
			executable = "php"
			args = append([]string{phar}, args...)
		}
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "COMPOSER_MIRROR_PATH_REPOS=1", "COMPOSER="+filepath.Join(root, "composer.json"), "COMPOSER_VENDOR_DIR="+filepath.Join(root, filepath.FromSlash(vendorDir)), "COMPOSER_BIN_DIR="+filepath.Join(root, filepath.FromSlash(binDir)))
	return cmd, nil
}

// dockerComposerCommand mounts only the private workspace and a persistent Composer
// cache. Composer still sees exactly the same isolated copy as a host invocation.
func dockerComposerCommand(ctx context.Context, settings config.Composer, image, workspace, name, vendorDir, binDir string) (*exec.Cmd, error) {
	cache, err := dockerCacheDir()
	if err != nil {
		return nil, err
	}
	composerCache := filepath.Join(cache, "composer")
	if err := os.MkdirAll(composerCache, 0700); err != nil {
		return nil, err
	}
	workspaceMount, err := bindMount(workspace, containerWorkspace)
	if err != nil {
		return nil, err
	}
	cacheMount, err := bindMount(composerCache, containerCache)
	if err != nil {
		return nil, err
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	container := "presto-composer-" + hex.EncodeToString(suffix)
	root := path.Join(containerWorkspace, name)
	args := []string{"run", "--rm", "--init", "--name", container, "--mount", workspaceMount, "--mount", cacheMount, "--workdir", root,
		"--env", "COMPOSER_MIRROR_PATH_REPOS=1",
		"--env", "COMPOSER=" + path.Join(root, "composer.json"),
		"--env", "COMPOSER_VENDOR_DIR=" + path.Join(root, vendorDir),
		"--env", "COMPOSER_BIN_DIR=" + path.Join(root, binDir),
		"--env", "COMPOSER_HOME=" + path.Join(containerCache, "home"),
		"--env", "COMPOSER_CACHE_DIR=" + path.Join(containerCache, "files"),
		// Without a value, Docker forwards the host variable only when it is set.
		"--env", "COMPOSER_AUTH",
	}
	if runtime.GOOS != "windows" {
		// Files written into the workspace must stay removable by the invoking user.
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	args = append(args, settings.DockerArgs...)
	args = append(args, image)
	args = append(args, settings.Command[0])
	args = append(args, composerArgs(settings)...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	// Killing the Docker client does not stop its container; remove it explicitly.
	cmd.Cancel = func() error {
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(stop, "docker", "rm", "--force", container).Run()
		return cmd.Process.Kill()
	}
	return cmd, nil
}

// bindMount quotes --mount fields as CSV, which Docker parses, so paths may contain commas.
func bindMount(source, target string) (string, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write([]string{"type=bind", "source=" + source, "target=" + target}); err != nil {
		return "", err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

// pinAutoloaderSuffix makes Composer's generated autoloader class name stable. Composer
// 2.2, the last release for PHP 7.1, otherwise picks a random suffix on every fresh
// install, so each sync would rewrite vendor/autoload.php. The suffix is unique per
// module because two modules sharing it would redeclare the same class in one shop.
// The returned function restores the original composer.json before packaging.
func pinAutoloaderSuffix(root, name string) (func() error, error) {
	manifest := filepath.Join(root, "composer.json")
	original, err := os.ReadFile(manifest)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, fmt.Errorf("composer.json must contain a JSON object")
	}
	settings, _ := document["config"].(map[string]any)
	if settings == nil {
		// PHP encodes an empty config object as [], which Composer accepts.
		if value, exists := document["config"]; exists && !isEmptyList(value) {
			return nil, fmt.Errorf("composer.json config must be a JSON object")
		}
		settings = map[string]any{}
		document["config"] = settings
	}
	if suffix, _ := settings["autoloader-suffix"].(string); suffix != "" {
		return func() error { return nil }, nil
	}
	lock, err := os.ReadFile(filepath.Join(root, "composer.lock"))
	if err != nil {
		lock = original
	}
	hash := sha256.Sum256(append([]byte(name+"\x00"), lock...))
	settings["autoloader-suffix"] = hex.EncodeToString(hash[:16])
	pinned, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(manifest, pinned, 0644); err != nil {
		return nil, err
	}
	return func() error { return os.WriteFile(manifest, original, 0644) }, nil
}

func isEmptyList(value any) bool {
	list, ok := value.([]any)
	return ok && len(list) == 0
}
