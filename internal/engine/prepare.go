// Package engine implements packaging and synchronization independently of the CLI.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/brandnewteam/presto/internal/config"
)

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var safeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

type Snapshot struct {
	Root    string `json:"-"`
	Source  string `json:"-"`
	Name    string `json:"module"`
	Version string `json:"version"`
	Files   []File `json:"files"`
	Rules   Rules  `json:"rules"`
}

func ValidModule(name string) bool { return moduleName.MatchString(name) }

// FindProject supports invocation from a module's subdirectories, with TOML taking precedence.
func FindProject(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(current); err != nil || !info.IsDir() {
		return "", fmt.Errorf("project directory does not exist: %s", current)
	}
	for dir := current; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, "presto.toml")); err == nil && !info.IsDir() {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for dir := current; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, filepath.Base(dir)+".php")); err == nil && !info.IsDir() {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", fmt.Errorf("no module found above %s; use --project or add presto.toml with [module] name", current)
}

// Prepare copies the selected working tree before invoking Composer. The source is never modified.
func Prepare(ctx context.Context, source string, cfg config.Config, noComposer bool, logs io.Writer) (*Snapshot, func(), error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, nil, err
	}
	source, err = filepath.Abs(resolved)
	if err != nil {
		return nil, nil, err
	}
	name := cfg.Module.Name
	if name == "" {
		name = filepath.Base(source)
	}
	if !ValidModule(name) {
		return nil, nil, fmt.Errorf("invalid module name %q", name)
	}
	main, err := os.ReadFile(filepath.Join(source, name+".php"))
	if err != nil {
		return nil, nil, fmt.Errorf("module entry point %s.php: %w", name, err)
	}
	version := cfg.Module.Version
	if version == "" {
		properties, _ := phpProperties(string(main), name)
		if property, exists := properties["version"]; exists && !property.conditional {
			version, _ = phpString(property.tokens)
		}
	}
	if version != "" && !safeVersion.MatchString(version) {
		return nil, nil, fmt.Errorf("invalid module version %q", version)
	}
	useComposer := !noComposer && (cfg.Composer.Enabled == nil || *cfg.Composer.Enabled)
	if _, err := os.Stat(filepath.Join(source, "composer.json")); err != nil {
		useComposer = false
	}
	workspace, err := workspaceDir(useComposer && cfg.Composer.Docker() != "")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	fail := func(err error) (*Snapshot, func(), error) { cleanup(); return nil, nil, err }
	root := filepath.Join(workspace, name)
	if err := os.Mkdir(root, 0755); err != nil {
		return fail(err)
	}
	rules := NewRules(cfg.Files)
	vendorDir, binDir := "vendor", "vendor/bin"
	if useComposer {
		var settings struct {
			Config struct {
				VendorDir string `json:"vendor-dir"`
				BinDir    string `json:"bin-dir"`
			} `json:"config"`
		}
		data, err := os.ReadFile(filepath.Join(source, "composer.json"))
		if err != nil {
			return fail(err)
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return fail(fmt.Errorf("composer.json: %w", err))
		}
		if settings.Config.VendorDir != "" {
			vendorDir = settings.Config.VendorDir
		}
		binDir = vendorDir + "/bin"
		if settings.Config.BinDir != "" {
			binDir = settings.Config.BinDir
		}
		if !validPath(vendorDir) || !validPath(binDir) {
			return fail(fmt.Errorf("Composer vendor-dir and bin-dir must be relative paths within the module"))
		}
		if !rules.SelectedDir(vendorDir) {
			return fail(fmt.Errorf("file rules exclude Composer vendor directory %q", vendorDir))
		}
	}
	if useComposer && cfg.Composer.RequireLock != nil && *cfg.Composer.RequireLock {
		if _, err := os.Stat(filepath.Join(source, "composer.lock")); err != nil {
			return fail(fmt.Errorf("Composer preparation requires composer.lock: %w", err))
		}
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if useComposer && (rel == vendorDir || strings.HasPrefix(rel, vendorDir+"/") || rel == binDir || strings.HasPrefix(rel, binDir+"/")) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks match like directories, as they did before directory-only patterns.
		dir := entry.IsDir() || entry.Type()&os.ModeSymlink != 0
		selected := rules.selected(rel, dir)
		// Composer inputs are copied even when excluded from the eventual package.
		if useComposer && (rel == "composer.json" || rel == "composer.lock") {
			selected = true
		}
		if !selected {
			if entry.IsDir() && (rules.PreservedDir(rel) || reserved(rel) || len(rules.Include) == 0) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in selected source: %s (copy the file or exclude it)", rel)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported source file type: %s", rel)
		}
		return copyFile(path, filepath.Join(root, filepath.FromSlash(rel)), normalizedMode(info.Mode()))
	})
	if err != nil {
		return fail(err)
	}
	if useComposer {
		cmd, err := composerCommand(ctx, cfg.Composer, workspace, name, vendorDir, binDir)
		if err != nil {
			return fail(err)
		}
		cmd.Stdout, cmd.Stderr = logs, logs
		restore, err := pinAutoloaderSuffix(root, name)
		if err != nil {
			return fail(err)
		}
		if image := cfg.Composer.Docker(); image != "" {
			fmt.Fprintf(logs, "Preparing production Composer dependencies in Docker image %s…\n", image)
		} else {
			fmt.Fprintln(logs, "Preparing production Composer dependencies in an isolated directory…")
		}
		runErr := cmd.Run()
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if runErr != nil {
			return fail(fmt.Errorf("Composer preparation failed: %w (configure composer.command or composer.docker_image for the module's PHP version)", runErr))
		}
		if err := restore(); err != nil {
			return fail(err)
		}
	}
	snapshot := &Snapshot{Root: root, Source: source, Name: name, Version: version, Rules: rules}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !rules.selected(rel, entry.IsDir() || entry.Type()&os.ModeSymlink != 0) {
			if entry.IsDir() && (rules.PreservedDir(rel) || len(rules.Include) == 0) {
				return filepath.SkipDir
			}
			return nil
		}
		if !validPath(rel) {
			return fmt.Errorf("unsupported module path: %q", rel)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in prepared module: %s", rel)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported prepared file: %s", rel)
		}
		hash, size, err := hashFile(path)
		if err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, File{rel, hash, size, uint32(normalizedMode(info.Mode()))})
		return nil
	})
	if err != nil {
		return fail(err)
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	casePaths := map[string]string{}
	for _, file := range snapshot.Files {
		for name := file.Path; name != "."; name = filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))) {
			key := strings.ToLower(name)
			if previous, ok := casePaths[key]; ok && previous != name {
				return fail(fmt.Errorf("case-colliding module paths %q and %q", previous, name))
			}
			casePaths[key] = name
		}
	}
	foundMain := false
	for _, file := range snapshot.Files {
		if file.Path == name+".php" {
			foundMain = true
		}
	}
	if !foundMain {
		return fail(fmt.Errorf("file rules exclude the module entry point %s.php", name))
	}
	return snapshot, cleanup, nil
}

func normalizedMode(mode fs.FileMode) fs.FileMode {
	if mode&0111 != 0 {
		return 0755
	}
	return 0644
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

func copyFile(source, destination string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".presto-copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, copyErr := io.Copy(out, in)
	if err := out.Chmod(mode); err != nil && copyErr == nil {
		copyErr = err
	}
	if err := out.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		return copyErr
	}
	return os.Rename(out.Name(), destination)
}
