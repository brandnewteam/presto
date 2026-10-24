package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLayersAndRelativePaths(t *testing.T) {
	base := t.TempDir()
	global := filepath.Join(base, "user", "config.toml")
	project := filepath.Join(base, "project")
	write(t, global, "schema = 1\n[files]\nexclude=['global/']\n[targets.local]\ntype='local'\nroot='../shop'\n[targets.production]\ntype='ssh'\nhost='alias'\nroot='/srv/shop'\n")
	write(t, filepath.Join(project, "presto.toml"), "schema=1\n[module]\nname='examplemodule'\n[files]\nexclude=['project/']\n[composer]\nscripts=true\n")
	write(t, filepath.Join(project, "presto.local.toml"), "[module]\nversion='2.0.0'\n[targets.local]\ntype='local'\nroot='other-shop'\n")
	cfg, err := Load(global, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Module.Name != "examplemodule" || cfg.Module.Version != "2.0.0" || cfg.Composer.Scripts == nil || !*cfg.Composer.Scripts {
		t.Fatalf("wrong config: %+v", cfg)
	}
	if cfg.Targets["local"].Root != filepath.Join(project, "other-shop") {
		t.Fatal(cfg.Targets["local"])
	}
	if cfg.Targets["production"].Agent != "presto" {
		t.Fatal("missing default agent")
	}
	if len(cfg.Loaded) != 3 {
		t.Fatal(cfg.Loaded)
	}
	joined := strings.Join(cfg.Files.Exclude, ",")
	if !strings.Contains(joined, "global/") || !strings.Contains(joined, "project/") {
		t.Fatal(joined)
	}
}

func TestRejectInvalidConfig(t *testing.T) {
	for _, content := range []string{"schema=9", "[composer]\nenabeld=true", "[targets.x]\ntype='remote'\nroot='/srv/shop'", "[targets.x]\ntype='ssh'\nhost='-bad'\nroot='/srv/shop'", "[files]\nexclude=['../outside']", "[files]\nexclude=['[']", "[composer]\ncommand=[]"} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			write(t, path, content)
			if _, err := Load(path, "", true); err == nil {
				t.Fatalf("accepted %s", content)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent"), "", true); err == nil {
		t.Fatal("accepted missing explicit config")
	}
}

func TestExpandHomeAndGlobalOverride(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"~", "~/subdir"} {
		got, err := ExpandPath(input, "")
		if err != nil {
			t.Fatal(err)
		}
		expected := home
		if input != "~" {
			expected = filepath.Join(home, "subdir")
		}
		if got != expected {
			t.Fatalf("%s => %s", input, got)
		}
	}
	t.Setenv("PRESTO_CONFIG", "~/dotfiles/presto.toml")
	got, err := GlobalPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, "dotfiles", "presto.toml") {
		t.Fatal(got)
	}
}

func TestPrestaShopTargetValidation(t *testing.T) {
	for _, settings := range []string{
		"upgrade=true",
		"upgrade=true\nshop_context='shop'",
		"upgrade=true\nshop_context='shop'\nshop_id=-1",
		"upgrade=true\nshop_context='all'\nshop_id=1",
		"upgrade=true\nshop_context='group'\nshop_group_id=0",
		"upgrade=true\nshop_context='group'\nshop_group_id=1\nshop_id=1",
		"upgrade=true\nshop_context='all'\nphp=['']",
		"upgrade=true\nshop_context='all'\nphp=[]",
		"upgrade=true\nshop_context='implicit'",
		"upgrade=true\nshop_context='all'\non_error='ignore'",
	} {
		t.Run(settings, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			write(t, file, "[targets.local]\ntype='local'\nroot='shop'\n[targets.local.prestashop]\n"+settings)
			if _, err := Load(file, "", true); err == nil {
				t.Fatalf("accepted invalid lifecycle settings: %s", settings)
			}
		})
	}
	for _, settings := range []string{"upgrade=false", "upgrade=true\nshop_context='all'", "upgrade=true\nshop_context='shop'\nshop_id=2", "upgrade=true\nshop_context='group'\nshop_group_id=1"} {
		file := filepath.Join(t.TempDir(), "config.toml")
		write(t, file, "[targets.local]\ntype='local'\nroot='shop'\n[targets.local.prestashop]\n"+settings)
		cfg, err := Load(file, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Targets["local"].PrestaShop == nil {
			t.Fatal("lost lifecycle settings")
		}
	}
}
