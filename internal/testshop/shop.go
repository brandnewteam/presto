// Package testshop provides an offline harness with PrestaShop 1.6's real upgrade methods.
package testshop

import (
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

//go:embed testdata/config.inc.php
var bootstrap []byte

//go:embed testdata/native-upgrade.php
var nativeUpgrade []byte

func PHP(t *testing.T) []string {
	t.Helper()
	if value := os.Getenv("PRESTO_TEST_PHP_COMMAND"); value != "" {
		var command []string
		if err := json.Unmarshal([]byte(value), &command); err != nil || len(command) == 0 {
			t.Fatalf("invalid PRESTO_TEST_PHP_COMMAND: %v", err)
		}
		return command
	}
	path, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP is not installed")
	}
	return []string{path}
}

func Put(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func Create(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	Put(t, root, "config/config.inc.php", string(bootstrap))
	Put(t, root, "config/native-upgrade.php", string(nativeUpgrade))
	Put(t, root, "config/state.json", `{"installed":true,"version":"1.0.0","enabled":[1,2,3],"events":[],"disabled_calls":[],"shops":[{"id":1,"group":1,"active":true},{"id":2,"group":1,"active":false},{"id":3,"group":2,"active":true},{"id":4,"group":2,"active":true}]}`)
	Put(t, root, "modules/examplemodule/examplemodule.php", Module("1.0.0", ""))
	return root
}

func Module(version, extra string) string {
	return "<?php\nclass examplemodule extends Module { public function __construct() { $this->name = 'examplemodule';\n$this->version = '" + version + "';\nparent::__construct(); " + extra + " } }\n"
}

func State(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func Set(t *testing.T, root, key string, value any) {
	t.Helper()
	state := State(t, root)
	state[key] = value
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	Put(t, root, "config/state.json", string(data))
}
