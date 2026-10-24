// Package config loads data-only configuration without executing shell code.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"
)

type Module struct {
	Name      string    `toml:"name"`
	Version   string    `toml:"version"`
	ConfigXML ModuleXML `toml:"config_xml"`
}

type Files struct {
	Exclude  []string `toml:"exclude"`
	Include  []string `toml:"include"`
	Preserve []string `toml:"preserve"`
}

type Composer struct {
	Enabled     *bool    `toml:"enabled"`
	Command     []string `toml:"command"`
	Scripts     *bool    `toml:"scripts"`
	Plugins     *bool    `toml:"plugins"`
	RequireLock *bool    `toml:"require_lock"`
}

type Target struct {
	Type       string      `toml:"type" json:"type"`
	Root       string      `toml:"root" json:"root"`
	Host       string      `toml:"host" json:"host,omitempty"`
	Agent      string      `toml:"agent" json:"agent,omitempty"`
	SSHArgs    []string    `toml:"ssh_args" json:"ssh_args,omitempty"`
	PrestaShop *PrestaShop `toml:"prestashop" json:"prestashop,omitempty"`
}

type Config struct {
	Schema   int               `toml:"schema"`
	Module   Module            `toml:"module"`
	Files    Files             `toml:"files"`
	Composer Composer          `toml:"composer"`
	Targets  map[string]Target `toml:"targets"`
	Loaded   []string          `toml:"-"`
}

func Defaults() Config {
	return Config{
		Schema:  1,
		Targets: map[string]Target{},
		Files: Files{
			Exclude:  []string{".git/", ".gitignore", ".svn/", ".hg/", ".github/", ".vscode/", ".idea/", "node_modules/", "dist/", "deps/", "tests/", ".DS_Store", ".env", ".env.*", ".rsyncrc", ".prestarc*", ".tags*", ".editorconfig", ".tm_properties", ".eslintrc*", ".jshintrc", ".jscsrc", "build.sh", "stage.sh", "todo.md", "TODO.md", "config.xml"},
			Preserve: []string{"uploads/"},
		},
		Composer: Composer{Command: []string{"composer"}},
	}
}

// GlobalPath follows platform config conventions and permits a dotfiles override.
func GlobalPath() (string, error) {
	if path := os.Getenv("PRESTO_CONFIG"); path != "" {
		return ExpandPath(path, "")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(base, "presto", "config.toml"), nil
}

// Load merges defaults, global config, project config, then private project config.
// Rule arrays append; scalar settings and complete named targets replace earlier values.
func Load(global, project string, explicitGlobal bool) (Config, error) {
	cfg := Defaults()
	paths := []string{global}
	if project != "" {
		paths = append(paths, filepath.Join(project, "presto.toml"), filepath.Join(project, "presto.local.toml"))
	}
	for i, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) && !(i == 0 && explicitGlobal) {
			continue
		}
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		var layer Config
		if err := toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields().Decode(&layer); err != nil {
			return cfg, fmt.Errorf("config %s: %w", path, err)
		}
		if layer.Schema != 0 && layer.Schema != 1 {
			return cfg, fmt.Errorf("config %s: unsupported schema %d", path, layer.Schema)
		}
		if layer.Module.Name != "" {
			cfg.Module.Name = layer.Module.Name
		}
		if layer.Module.Version != "" {
			cfg.Module.Version = layer.Module.Version
		}
		cfg.Module.ConfigXML = cfg.Module.ConfigXML.merge(layer.Module.ConfigXML)
		cfg.Files.Exclude = append(cfg.Files.Exclude, layer.Files.Exclude...)
		cfg.Files.Include = append(cfg.Files.Include, layer.Files.Include...)
		cfg.Files.Preserve = append(cfg.Files.Preserve, layer.Files.Preserve...)
		if layer.Composer.Enabled != nil {
			cfg.Composer.Enabled = layer.Composer.Enabled
		}
		if layer.Composer.Scripts != nil {
			cfg.Composer.Scripts = layer.Composer.Scripts
		}
		if layer.Composer.Plugins != nil {
			cfg.Composer.Plugins = layer.Composer.Plugins
		}
		if layer.Composer.RequireLock != nil {
			cfg.Composer.RequireLock = layer.Composer.RequireLock
		}
		if layer.Composer.Command != nil {
			if len(layer.Composer.Command) == 0 || layer.Composer.Command[0] == "" {
				return cfg, fmt.Errorf("config %s: composer.command must contain an executable", path)
			}
			cfg.Composer.Command = layer.Composer.Command
		}
		for name, target := range layer.Targets {
			if name == "" || (target.Type != "local" && target.Type != "ssh") || target.Root == "" {
				return cfg, fmt.Errorf("config %s: target %q needs type = local or ssh and a root", path, name)
			}
			if target.Type == "local" {
				target.Root, err = ExpandPath(target.Root, filepath.Dir(path))
				if err != nil {
					return cfg, err
				}
			} else {
				if target.Host == "" || strings.HasPrefix(target.Host, "-") || strings.ContainsAny(target.Host, "\r\n\x00 \t") {
					return cfg, fmt.Errorf("target %q: invalid SSH host", name)
				}
				if target.Agent == "" {
					target.Agent = "presto"
				}
			}
			if target.PrestaShop != nil {
				if err := target.PrestaShop.Validate(); err != nil {
					return cfg, fmt.Errorf("target %q: %w", name, err)
				}
			}
			cfg.Targets[name] = target
		}
		cfg.Loaded = append(cfg.Loaded, path)
	}
	for _, group := range [][]string{cfg.Files.Exclude, cfg.Files.Include, cfg.Files.Preserve} {
		for _, pattern := range group {
			if err := ValidatePattern(pattern); err != nil {
				return cfg, err
			}
		}
	}
	return cfg, nil
}

// ExpandPath expands only a leading ~/; relative paths use the defining config directory.
func ExpandPath(path, base string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimLeft(path[1:], "/\\"))
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Abs(path)
}

func ValidatePattern(pattern string) error {
	p := strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
	if p == "" || strings.Contains(pattern, "\\") || strings.Contains(pattern, "\x00") || !doublestar.ValidatePattern(p) {
		return fmt.Errorf("invalid file pattern %q (use forward slashes)", pattern)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return fmt.Errorf("invalid parent traversal in pattern %q", pattern)
		}
	}
	return nil
}
