package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/engine"
	"github.com/brandnewteam/presto/internal/lifecycle"
	"github.com/brandnewteam/presto/internal/testshop"
)

type syncFixture struct {
	project string
	config  string
	alpha   string
	zeta    string
}

func localTarget(name, root string) string {
	return "[targets." + name + "]\ntype='local'\nroot=" + strconv.Quote(root) + "\n"
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	base := t.TempDir()
	f := syncFixture{project: filepath.Join(base, "examplemodule"), config: filepath.Join(base, "config.toml"), alpha: filepath.Join(base, "alpha-shop"), zeta: filepath.Join(base, "zeta-shop")}
	testshop.Put(t, f.project, "examplemodule.php", "<?php\n$this->version = '1.2.3';\n")
	testshop.Put(t, f.project, "views/template.tpl", "new template")
	testshop.Put(t, f.project, "config.xml", "source cache")
	for _, root := range []string{f.alpha, f.zeta} {
		testshop.Put(t, root, "config/config.inc.php", "<?php\n")
		testshop.Put(t, root, "modules/examplemodule/obsolete.php", "old code")
		testshop.Put(t, root, "modules/examplemodule/uploads/live.txt", "runtime data")
	}
	// Deliberately reverse declaration order and include an unreachable SSH target.
	testshop.Put(t, base, "config.toml", localTarget("zeta", f.zeta)+"[targets.remote]\ntype='ssh'\nhost='fixture.invalid'\nroot='/srv/shop'\n"+localTarget("alpha", f.alpha))
	return f
}

func (f syncFixture) args(extra ...string) []string {
	return append([]string{"--config", f.config, "--project", f.project, "sync"}, extra...)
}

func TestSyncWithoutTargetAppliesAllLocalTargets(t *testing.T) {
	f := newSyncFixture(t)
	out, err := execute(t, f.args("--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var results []syncTargetResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err, out)
	}
	if len(results) != 2 || results[0].Target != "alpha" || results[1].Target != "zeta" {
		t.Fatal(out)
	}
	for i, root := range []string{f.alpha, f.zeta} {
		result := results[i]
		if result.Error != "" || result.DryRun || len(result.Created) != 2 || len(result.Deleted) != 1 {
			t.Fatal(out)
		}
		data, err := os.ReadFile(filepath.Join(root, "modules/examplemodule/views/template.tpl"))
		if err != nil || string(data) != "new template" {
			t.Fatalf("%s %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(root, "modules/examplemodule/obsolete.php")); !os.IsNotExist(err) {
			t.Fatal("did not delete obsolete code", err)
		}
		data, err = os.ReadFile(filepath.Join(root, "modules/examplemodule/uploads/live.txt"))
		if err != nil || string(data) != "runtime data" {
			t.Fatal("lost runtime data", err)
		}
		if _, err := os.Stat(filepath.Join(root, "modules/examplemodule/config.xml")); !os.IsNotExist(err) {
			t.Fatal("sync generated config.xml", err)
		}
	}
}

func TestSyncAllDryRunAndHumanOutput(t *testing.T) {
	f := newSyncFixture(t)
	out, err := execute(t, f.args("--dry-run", "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var results []syncTargetResult
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 2 {
		t.Fatalf("%s %v", out, err)
	}
	for _, result := range results {
		if !result.DryRun {
			t.Fatal(out)
		}
	}
	for _, root := range []string{f.alpha, f.zeta} {
		if _, err := os.Stat(filepath.Join(root, "modules/examplemodule/examplemodule.php")); !os.IsNotExist(err) {
			t.Fatal("dry run created module entry point", err)
		}
		if _, err := os.Stat(filepath.Join(root, "modules/.presto-locks")); !os.IsNotExist(err) {
			t.Fatal("dry run created lock directory", err)
		}
		if _, err := os.Stat(filepath.Join(root, "modules/examplemodule/obsolete.php")); err != nil {
			t.Fatal("dry run deleted code", err)
		}
	}
	out, err = execute(t, f.args("--dry-run")...)
	if err != nil {
		t.Fatal(err)
	}
	first, last := strings.Index(out, " → alpha ("), strings.Index(out, " → zeta (")
	if first < 0 || last <= first {
		t.Fatal(out)
	}
}

func TestNamedSyncKeepsSingleTargetJSON(t *testing.T) {
	f := newSyncFixture(t)
	out, err := execute(t, f.args("zeta", "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var result engine.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Module != "examplemodule" {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(f.alpha, "modules/examplemodule/examplemodule.php")); !os.IsNotExist(err) {
		t.Fatal("named sync touched another target", err)
	}
	if _, err := execute(t, f.args("alpha", "zeta")...); err == nil {
		t.Fatal("accepted multiple target arguments")
	}
	if _, err := execute(t, "--config", f.config, "--project", f.project, "deploy"); err == nil {
		t.Fatal("deploy no longer requires a target")
	}
	if err := os.WriteFile(f.config, []byte(localTarget("zeta", f.zeta)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, f.args("--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var all []syncTargetResult
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 1 || all[0].Target != "zeta" {
		t.Fatalf("single local target must still produce an array without arguments: %s %v", out, err)
	}
}

func TestSyncAllRequiresLocalTargetsAndPreflightsRoots(t *testing.T) {
	t.Run("no local targets", func(t *testing.T) {
		f := newSyncFixture(t)
		if err := os.WriteFile(f.config, []byte("[targets.remote]\ntype='ssh'\nhost='fixture.invalid'\nroot='/srv/shop'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, f.args()...); err == nil || !strings.Contains(err.Error(), "no local targets") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("invalid root before any writes", func(t *testing.T) {
		f := newSyncFixture(t)
		if err := os.Remove(filepath.Join(f.zeta, "config/config.inc.php")); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, f.args()...); err == nil || !strings.Contains(err.Error(), `target "zeta"`) {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(filepath.Join(f.alpha, "modules/examplemodule/examplemodule.php")); !os.IsNotExist(err) {
			t.Fatal("changed first target before rejecting invalid second root", err)
		}
	})
}

func TestSyncAllContinuesAfterAnApplyFailure(t *testing.T) {
	f := newSyncFixture(t)
	testshop.Put(t, f.alpha, "modules/examplemodule/views", "conflicts with source directory")
	out, err := execute(t, f.args("--json")...)
	if err == nil || !strings.Contains(err.Error(), `target "alpha"`) {
		t.Fatalf("%s %v", out, err)
	}
	var results []syncTargetResult
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 2 {
		t.Fatalf("%s %v", out, err)
	}
	if results[0].Error == "" || results[1].Error != "" {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(f.zeta, "modules/examplemodule/examplemodule.php")); err != nil {
		t.Fatal("did not continue to second target", err)
	}
	if _, err := os.Stat(filepath.Join(f.alpha, "modules/examplemodule/obsolete.php")); err != nil {
		t.Fatal("failed validation deleted code", err)
	}
}

func TestSyncAllReportsFailedTargetsInHumanOutput(t *testing.T) {
	f := newSyncFixture(t)
	testshop.Put(t, f.alpha, "modules/examplemodule/views", "conflicts with source directory")
	var stdout, stderr bytes.Buffer
	command := New("test", strings.NewReader(""), &stdout, &stderr)
	command.SetArgs(f.args())
	if err := command.Execute(); err == nil {
		t.Fatal("ignored failed target")
	}
	if !strings.Contains(stderr.String(), "Failed to synchronize examplemodule → alpha (") || !strings.Contains(stdout.String(), "→ zeta (") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
}

func TestBatchComposerProcess(t *testing.T) {
	if os.Getenv("PRESTO_BATCH_COMPOSER_HELPER") != "1" {
		return
	}
	file, err := os.OpenFile(os.Getenv("PRESTO_BATCH_COMPOSER_COUNT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("prepared\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("vendor", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("vendor/autoload.php", []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestSyncAllPreparesComposerOnlyOnce(t *testing.T) {
	f := newSyncFixture(t)
	count := filepath.Join(t.TempDir(), "composer-count.txt")
	t.Setenv("PRESTO_BATCH_COMPOSER_HELPER", "1")
	t.Setenv("PRESTO_BATCH_COMPOSER_COUNT", count)
	command, _ := json.Marshal([]string{os.Args[0], "-test.run=^TestBatchComposerProcess$", "--"})
	testshop.Put(t, f.project, "presto.toml", "[composer]\ncommand="+string(command)+"\n")
	testshop.Put(t, f.project, "composer.json", `{"name":"example/module"}`)
	if out, err := execute(t, f.args("--json")...); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	data, err := os.ReadFile(count)
	if err != nil || string(data) != "prepared\n" {
		t.Fatalf("Composer ran more than once: %s %v", data, err)
	}
	for _, root := range []string{f.alpha, f.zeta} {
		if _, err := os.Stat(filepath.Join(root, "modules/examplemodule/vendor/autoload.php")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.project, "vendor")); !os.IsNotExist(err) {
		t.Fatal("Composer changed checkout", err)
	}
}

func TestSyncAllPreservesPerTargetUpgradeContext(t *testing.T) {
	php := testshop.PHP(t)
	f := newSyncFixture(t)
	f.alpha, f.zeta = testshop.Create(t), testshop.Create(t)
	testshop.Set(t, f.zeta, "fail_constructor", true)
	testshop.Put(t, f.project, "examplemodule.php", testshop.Module("1.2.3", "if (!empty(prestoState()['fail_constructor'])) { throw new Exception('bad constructor'); }"))
	testshop.Put(t, f.project, "upgrade/upgrade-1.1.0.php", "<?php function upgrade_module_1_1_0($module) { return true; }\n")
	command, _ := json.Marshal(php)
	config := localTarget("alpha", f.alpha) + "[targets.alpha.prestashop]\nupgrade=true\nshop_context='shop'\nshop_id=1\nphp=" + string(command) + "\n" +
		localTarget("zeta", f.zeta) + "[targets.zeta.prestashop]\nupgrade=true\nshop_context='group'\nshop_group_id=2\nphp=" + string(command) + "\n"
	if err := os.WriteFile(f.config, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, f.args("--json")...)
	if err == nil {
		t.Fatal("ignored failing upgrade", out)
	}
	var results []syncTargetResult
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 2 {
		t.Fatalf("%s %v", out, err)
	}
	if results[0].Upgrade == nil || results[0].Upgrade.Status != "upgraded" || len(results[0].Upgrade.ShopIDs) != 1 || results[0].Upgrade.ShopIDs[0] != 1 {
		t.Fatal(out)
	}
	if results[1].Upgrade == nil || !results[1].Upgrade.Disabled || results[1].Error == "" {
		t.Fatal(out)
	}
	enabled := testshop.State(t, f.zeta)["enabled"].([]any)
	if len(enabled) != 2 || enabled[0] != float64(1) || enabled[1] != float64(2) {
		t.Fatalf("disabled shops outside target context: %v", enabled)
	}
}

func TestFailureOutputShowsRecoveryState(t *testing.T) {
	var stderr bytes.Buffer
	command := New("test", strings.NewReader(""), &bytes.Buffer{}, &stderr)
	upgrade := &lifecycle.Result{Status: "failed", ShopContext: "shop", ShopIDs: []int{2}, FromVersion: "1.0.0", ToVersion: "1.1.0", RecoveryError: "database unavailable"}
	printFailure(command, "local", engine.Result{Module: "examplemodule", Destination: "/shop/modules/examplemodule", Updated: []string{"examplemodule.php"}, Upgrade: upgrade}, false)
	for _, want := range []string{"planned 0 created, 1 updated, 0 deleted", "PrestaShop: failed (context shop, shops [2], version 1.0.0 → 1.1.0)", "activation state unknown", "recovery failed: database unavailable"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("missing %q in:\n%s", want, &stderr)
		}
	}
	stderr.Reset()
	printFailure(command, "local", engine.Result{Module: "examplemodule"}, false)
	if stderr.Len() != 0 {
		t.Fatal("single-target failure without details must leave reporting to the error")
	}
}
