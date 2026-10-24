//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"presto/internal/engine"
	"presto/internal/testshop"
)

func TestSIGTERMDuringUpgradeStillDisablesModule(t *testing.T) {
	if os.Getenv("PRESTO_TEST_PHP_COMMAND") != "" {
		t.Skip("signal test requires direct PHP process ownership")
	}
	php := testshop.PHP(t)
	base := t.TempDir()
	binary := filepath.Join(base, "presto")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	project := filepath.Join(base, "examplemodule")
	testshop.Put(t, project, "examplemodule.php", testshop.Module("1.3.0", ""))
	testshop.Put(t, project, "upgrade/upgrade-1.1.0.php", "<?php\nfunction upgrade_module_1_1_0($module) { file_put_contents(_PS_MODULE_DIR_.'examplemodule/upgrade-started','1'); sleep(5); return true; }\n")
	shop := testshop.Create(t)
	phpJSON, _ := json.Marshal(php)
	configPath := filepath.Join(base, "config.toml")
	testshop.Put(t, base, "config.toml", "[targets.local]\ntype='local'\nroot="+strconv.Quote(shop)+"\n[targets.local.prestashop]\nupgrade=true\nshop_context='shop'\nshop_id=1\nphp="+string(phpJSON)+"\n")
	command := exec.Command(binary, "--config", configPath, "--project", project, "sync", "local", "--json")
	var output, logs bytes.Buffer
	command.Stdout, command.Stderr = &output, &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(8 * time.Second)
wait:
	for {
		select {
		case err := <-done:
			t.Fatalf("ended before signal: %v %s", err, logs.String())
		case <-timeout:
			t.Fatal("migration did not start")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(shop, "modules", "examplemodule", "upgrade-started")); err == nil {
				break wait
			}
		}
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled upgrade returned success")
		}
		var result engine.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("missing failure result: %v %s %s", err, output.String(), logs.String())
		}
		if result.Upgrade == nil || !result.Upgrade.Disabled {
			t.Fatalf("termination skipped recovery: %s %s", output.String(), logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("receiver did not complete recovery")
	}
}
