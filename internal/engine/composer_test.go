package engine

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/config"
)

func TestDockerComposerUsesContainerPaths(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache, with comma")
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("LocalAppData", cache)
	image := "presto-composer:php7.1"
	settings := config.Composer{Command: []string{"composer", "--ansi"}, DockerImage: &image, DockerArgs: []string{"--platform", "linux/amd64"}}
	workspace := filepath.Join(t.TempDir(), "work, space")
	cmd, err := composerCommand(context.Background(), settings, workspace, "examplemodule", "lib/vendor", "bin")
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args[1:]
	if filepath.Base(cmd.Args[0]) != "docker" && filepath.Base(cmd.Args[0]) != "docker.exe" || args[0] != "run" || cmd.Dir != "" || cmd.Env != nil {
		t.Fatalf("not a plain docker invocation: %q dir=%q", cmd.Args, cmd.Dir)
	}
	var mounts []map[string]string
	for i, arg := range args {
		if arg == "--mount" {
			fields, err := csv.NewReader(strings.NewReader(args[i+1])).Read()
			if err != nil {
				t.Fatal(err)
			}
			mount := map[string]string{}
			for _, field := range fields {
				key, value, _ := strings.Cut(field, "=")
				mount[key] = value
			}
			mounts = append(mounts, mount)
		}
	}
	if len(mounts) != 2 || mounts[0]["source"] != workspace || mounts[0]["target"] != "/presto/work" || mounts[1]["target"] != "/presto/cache" || !strings.HasPrefix(mounts[1]["source"], cache) {
		t.Fatalf("wrong mounts: %v", mounts)
	}
	for _, want := range []string{"COMPOSER=/presto/work/examplemodule/composer.json", "COMPOSER_VENDOR_DIR=/presto/work/examplemodule/lib/vendor", "COMPOSER_BIN_DIR=/presto/work/examplemodule/bin", "COMPOSER_AUTH"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing --env %s in %q", want, args)
		}
	}
	at := slices.Index(args, image)
	if at < 0 || !slices.Equal(args[at-2:at], []string{"--platform", "linux/amd64"}) || !slices.Equal(args[at+1:at+4], []string{"composer", "--ansi", "install"}) {
		t.Fatalf("wrong image/command order: %q", args)
	}
	if runtime.GOOS != "windows" && !slices.Contains(args, "--user") {
		t.Fatal("container would write root-owned workspace files")
	}
	if strings.Contains(strings.Join(args[at:], " "), workspace) {
		t.Fatal("host workspace path leaked into the container command")
	}
	if _, err := os.Stat(mounts[1]["source"]); err != nil {
		t.Fatal("Composer cache directory was not created")
	}
}

func TestPinAutoloaderSuffixKeepsKeyOrder(t *testing.T) {
	cases := map[string]string{
		"without config": `{"require": {"zeta/b": "^1", "alpha/a": "^2"}, "replace": {"ql/x": "1.0"}}`,
		"empty config":   `{"require": {"zeta/b": "^1", "alpha/a": "^2"}, "config": [], "replace": {"ql/x": "1.0"}}`,
		"other config":   `{"require": {"zeta/b": "^1", "alpha/a": "^2"}, "config": {"platform": {"php": "7.1.33"}, "autoloader-suffix": ""}, "replace": {"ql/x": "1.0"}}`,
	}
	for label, manifest := range cases {
		t.Run(label, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "composer.json")
			if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
				t.Fatal(err)
			}
			restore, err := pinAutoloaderSuffix(root, "examplemodule")
			if err != nil {
				t.Fatal(err)
			}
			pinned, _ := os.ReadFile(path)
			var document struct {
				Config struct {
					Platform map[string]string `json:"platform"`
					Suffix   string            `json:"autoloader-suffix"`
				} `json:"config"`
			}
			if err := json.Unmarshal(pinned, &document); err != nil || len(document.Config.Suffix) != 32 {
				t.Fatalf("suffix not pinned: %s", pinned)
			}
			if label == "other config" && document.Config.Platform["php"] != "7.1.33" {
				t.Fatalf("config.platform lost: %s", pinned)
			}
			text := string(pinned)
			if !(strings.Index(text, `"require"`) < strings.Index(text, `"zeta/b"`) && strings.Index(text, `"zeta/b"`) < strings.Index(text, `"alpha/a"`) && strings.Index(text, `"alpha/a"`) < strings.Index(text, `"replace"`)) {
				t.Fatalf("key order changed: %s", pinned)
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			if restored, _ := os.ReadFile(path); string(restored) != manifest {
				t.Fatalf("composer.json not restored: %s", restored)
			}
		})
	}
}

func TestPinAutoloaderSuffixRejectsNonObjects(t *testing.T) {
	for _, manifest := range []string{`null`, `[]`, `{"config": null}`, `{"config": [1]}`, `{} {}`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "composer.json"), []byte(manifest), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := pinAutoloaderSuffix(root, "examplemodule"); err == nil {
			t.Errorf("%s: accepted", manifest)
		}
	}
}
