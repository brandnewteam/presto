package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := New("test", strings.NewReader(""), &stdout, &stderr)
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), err
}

func TestInitDoesNotOverwriteConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global", "config.toml")
	if _, err := execute(t, "init", "--global", "--config", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("personal configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "init", "--global", "--config", path); err == nil {
		t.Fatal("overwrote configuration")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "personal configuration" {
		t.Fatal("configuration changed")
	}
}

func TestDynamicVersionRequiresOverrideOrNoVersion(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "examplemodule")
	if err := os.MkdirAll(filepath.Join(project, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	main := []byte("<?php\n$this->version = self::VERSION;\n")
	if err := os.WriteFile(filepath.Join(project, "examplemodule.php"), main, 0644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(base, "config.toml")
	if err := os.WriteFile(global, []byte("schema=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRESTO_CONFIG", global)
	if _, err := execute(t, "build", "--project", project); err == nil {
		t.Fatal("invented a dynamic version")
	}
	if _, err := execute(t, "build", "--project", filepath.Join(project, "src"), "--no-version"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, "dist", "latest-examplemodule.zip")); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "build", "--project", project, "--module-version", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, "dist", "v2.0.0-examplemodule.zip")); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "build", "--project", project, "--module-version", "../../escape"); err == nil {
		t.Fatal("accepted path in version")
	}
	data, err := os.ReadFile(filepath.Join(project, "examplemodule.php"))
	if err != nil || !bytes.Equal(data, main) {
		t.Fatal("changed source")
	}
}
