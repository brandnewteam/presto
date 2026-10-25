package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brandnewteam/presto/internal/config"
	"github.com/brandnewteam/presto/internal/testshop"
)

func lifecycleSnapshot(t *testing.T, constructor, secondScript string) *Snapshot {
	t.Helper()
	source := filepath.Join(t.TempDir(), "examplemodule")
	put(t, source, "examplemodule.php", testshop.Module("1.3.0", constructor))
	put(t, source, "upgrade/upgrade-1.1.0.php", "<?php\nfunction upgrade_module_1_1_0($module) { prestoEvent(array('version'=>'1.1.0','scope'=>Shop::getContext(),'shops'=>array_values(Shop::getContextListShopID()))); echo 'upgrade output'; return true; }\n")
	put(t, source, "upgrade/upgrade-1.2.0.php", "<?php\nfunction upgrade_module_1_2_0($module) { "+secondScript+" }\n")
	return prepareFixture(t, source, config.Defaults())
}

func upgradeOptions(t *testing.T, scope string) SyncOptions {
	t.Helper()
	settings := &config.PrestaShop{Upgrade: true, PHP: testshop.PHP(t), ShopContext: scope}
	if scope == "shop" {
		settings.ShopID = 1
	}
	if scope == "group" {
		settings.ShopGroupID = 1
	}
	return SyncOptions{PrestaShop: settings, Logs: io.Discard}
}

func TestNativeUpgradeSuccessAndNoReenable(t *testing.T) {
	shop := testshop.Create(t)
	snapshot := lifecycleSnapshot(t, "echo 'constructor output';", "prestoEvent(array('version'=>'1.2.0')); return true;")
	result, err := Synchronize(context.Background(), snapshot, shop, false, upgradeOptions(t, "all"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Upgrade.Status != "upgraded" || result.Upgrade.Applied != 2 || result.Upgrade.FromVersion != "1.0.0" || result.Upgrade.ToVersion != "1.3.0" {
		t.Fatalf("unexpected upgrade: %+v", result.Upgrade)
	}
	if !reflect.DeepEqual(result.Upgrade.ShopIDs, []int{1, 2, 3, 4}) {
		t.Fatal(result.Upgrade.ShopIDs)
	}
	state := testshop.State(t, shop)
	if state["version"] != "1.3.0" || !reflect.DeepEqual(state["enabled"], []any{float64(1), float64(2), float64(3)}) {
		t.Fatalf("changed activation state or failed version finalization: %#v", state)
	}
	events := state["events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["scope"] != float64(4) {
		t.Fatal(events)
	}
	result, err = Synchronize(context.Background(), snapshot, shop, false, upgradeOptions(t, "all"))
	if err != nil || result.Upgrade.Status != "up_to_date" || len(testshop.State(t, shop)["events"].([]any)) != 2 {
		t.Fatalf("reran upgrades: %+v %v", result.Upgrade, err)
	}
}

func TestNativeUpgradeFailureDisablesExactScope(t *testing.T) {
	for _, scope := range []string{"all", "shop", "group"} {
		t.Run(scope, func(t *testing.T) {
			shop := testshop.Create(t)
			snapshot := lifecycleSnapshot(t, "", "return false;")
			result, err := Synchronize(context.Background(), snapshot, shop, false, upgradeOptions(t, scope))
			if err == nil || result.Upgrade.Status != "failed" || !result.Upgrade.Disabled || result.Error == "" {
				t.Fatalf("failure not reported: %+v %v", result, err)
			}
			state := testshop.State(t, shop)
			expected := []any{}
			if scope == "shop" {
				expected = []any{float64(2), float64(3)}
			}
			if scope == "group" {
				expected = []any{float64(3)}
			}
			if !reflect.DeepEqual(state["enabled"], expected) {
				t.Fatalf("wrong deactivation scope %s: %#v", scope, state["enabled"])
			}
			if state["version"] != "1.1.0" || result.Upgrade.ToVersion != "1.1.0" {
				t.Fatalf("lost partial migration version: %#v %+v", state, result.Upgrade)
			}
			if len(state["events"].([]any)) != 1 {
				t.Fatal("first migration did not run exactly once")
			}
			destination := filepath.Join(shop, "modules", "examplemodule")
			if _, err := os.Stat(filepath.Join(destination, "upgrade", "upgrade-1.2.0.php")); err != nil {
				t.Fatal("files were rolled back unexpectedly")
			}
		})
	}
}

func TestConstructorAndRuntimeFailuresStillDeactivate(t *testing.T) {
	for _, body := range []string{"throw new Exception('constructor failed');", "exit(0);", "trigger_error('fatal constructor', E_USER_ERROR);"} {
		t.Run(body, func(t *testing.T) {
			shop := testshop.Create(t)
			snapshot := lifecycleSnapshot(t, body, "return true;")
			result, err := Synchronize(context.Background(), snapshot, shop, false, upgradeOptions(t, "shop"))
			if err == nil || !result.Upgrade.Disabled || result.Upgrade.Status != "failed" {
				t.Fatalf("did not recover from broken module: %+v %v", result.Upgrade, err)
			}
			if !reflect.DeepEqual(testshop.State(t, shop)["enabled"], []any{float64(2), float64(3)}) {
				t.Fatal("wrong deactivation scope")
			}
		})
	}
	t.Run("upgrade throws", func(t *testing.T) {
		shop := testshop.Create(t)
		result, err := Synchronize(context.Background(), lifecycleSnapshot(t, "", "throw new Exception('migration failed');"), shop, false, upgradeOptions(t, "group"))
		if err == nil || !result.Upgrade.Disabled {
			t.Fatalf("%+v %v", result.Upgrade, err)
		}
	})
}

func TestFailedDeactivationIsReported(t *testing.T) {
	shop := testshop.Create(t)
	testshop.Set(t, shop, "disable_fails", true)
	result, err := Synchronize(context.Background(), lifecycleSnapshot(t, "", "return false;"), shop, false, upgradeOptions(t, "shop"))
	if err == nil || result.Upgrade.Disabled || result.Upgrade.RecoveryError == "" {
		t.Fatalf("claimed successful deactivation: %+v %v", result.Upgrade, err)
	}
}

func TestLifecyclePreflightStopsBeforeFileChanges(t *testing.T) {
	for _, failure := range []string{"syntax", "shop", "version", "downgrade"} {
		t.Run(failure, func(t *testing.T) {
			shop := testshop.Create(t)
			old := read(t, shop, "modules/examplemodule/examplemodule.php")
			snapshot := lifecycleSnapshot(t, "", "return true;")
			opts := upgradeOptions(t, "shop")
			switch failure {
			case "syntax":
				// Prepare accepts source without executing PHP; target lint must reject it.
				source := filepath.Join(t.TempDir(), "examplemodule")
				put(t, source, "examplemodule.php", "<?php\n$this->version = '1.3.0';\nfunction broken( {\n")
				snapshot = prepareFixture(t, source, config.Defaults())
			case "shop":
				opts.PrestaShop.ShopID = 99
			case "version":
				testshop.Set(t, shop, "ps_version", "8.0.0")
			case "downgrade":
				testshop.Set(t, shop, "version", "9.0.0")
			}
			result, err := Synchronize(context.Background(), snapshot, shop, false, opts)
			if err == nil || result.Upgrade.Status != "preflight_failed" {
				t.Fatalf("preflight succeeded: %+v %v", result.Upgrade, err)
			}
			if got := read(t, shop, "modules/examplemodule/examplemodule.php"); got != old {
				t.Fatal("preflight modified module files")
			}
			if len(testshop.State(t, shop)["disabled_calls"].([]any)) != 0 {
				t.Fatal("preflight disabled module")
			}
		})
	}
}

func TestLifecycleLintSkipsComposerVendor(t *testing.T) {
	for _, vendor := range []string{"vendor", "lib/deps"} {
		t.Run(vendor, func(t *testing.T) {
			shop := testshop.Create(t)
			source := filepath.Join(t.TempDir(), "examplemodule")
			put(t, source, "examplemodule.php", testshop.Module("1.3.0", ""))
			if vendor != "vendor" {
				put(t, source, "composer.json", `{"config": {"vendor-dir": "./`+vendor+`"}}`)
			}
			// Like Symfony's bootstrap80.php: valid only for runtimes that will load it.
			put(t, source, vendor+"/symfony/polyfill/bootstrap99.php", "<?php\nfunction future( {\n")
			cfg := config.Defaults()
			disabled := false
			cfg.Composer.Enabled = &disabled
			result, err := Synchronize(context.Background(), prepareFixture(t, source, cfg), shop, false, upgradeOptions(t, "all"))
			if err != nil || result.Upgrade.Status != "upgraded" {
				t.Fatalf("vendor lint blocked the upgrade: %+v %v", result.Upgrade, err)
			}
		})
	}
}

func TestLifecycleDryRunDoesNotExecutePHP(t *testing.T) {
	shop := testshop.Create(t)
	snapshot := lifecycleSnapshot(t, "", "return false;")
	before := read(t, shop, "config/state.json")
	opts := SyncOptions{PrestaShop: &config.PrestaShop{Upgrade: true, ShopContext: "all", PHP: []string{"/must-not-execute-php"}}}
	result, err := Synchronize(context.Background(), snapshot, shop, true, opts)
	if err != nil || result.Upgrade.Status != "planned" {
		t.Fatalf("%+v %v", result.Upgrade, err)
	}
	if got := read(t, shop, "config/state.json"); got != before {
		t.Fatal("dry run changed state")
	}
	if _, err := os.Stat(filepath.Join(shop, "modules", ".presto-locks")); !os.IsNotExist(err) {
		t.Fatal("dry run created lock")
	}
}

func TestUninstalledAndDisabledModulesRemainSo(t *testing.T) {
	for _, installed := range []bool{true, false} {
		t.Run(map[bool]string{true: "disabled", false: "uninstalled"}[installed], func(t *testing.T) {
			shop := testshop.Create(t)
			testshop.Set(t, shop, "installed", installed)
			testshop.Set(t, shop, "enabled", []int{})
			result, err := Synchronize(context.Background(), lifecycleSnapshot(t, "", "return true;"), shop, false, upgradeOptions(t, "all"))
			if err != nil {
				t.Fatal(err)
			}
			if !installed && result.Upgrade.Status != "not_installed" {
				t.Fatal(result.Upgrade.Status)
			}
			state := testshop.State(t, shop)
			if len(state["enabled"].([]any)) != 0 || state["installed"] != installed {
				t.Fatal("installed or enabled module unexpectedly")
			}
		})
	}
}

func TestReceiveRunsSameLifecycle(t *testing.T) {
	snapshot := lifecycleSnapshot(t, "", "return false;")
	var archive bytes.Buffer
	if err := WriteArchive(context.Background(), snapshot, &archive, true); err != nil {
		t.Fatal(err)
	}
	shop := testshop.Create(t)
	result, err := Receive(context.Background(), bytes.NewReader(archive.Bytes()), shop, false, upgradeOptions(t, "group"))
	if err == nil || !result.Upgrade.Disabled || !strings.Contains(result.Error, "module upgrade failed") {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestCanceledUpgradeRecoversAndHoldsLockThroughFinalization(t *testing.T) {
	if os.Getenv("PRESTO_TEST_PHP_COMMAND") != "" {
		t.Skip("cancellation test requires direct PHP process ownership")
	}
	shop := testshop.Create(t)
	snapshot := lifecycleSnapshot(t, "", "file_put_contents(_PS_MODULE_DIR_.'examplemodule/upgrade-started', '1'); sleep(5); return true;")
	opts := upgradeOptions(t, "shop")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type response struct {
		result Result
		err    error
	}
	done := make(chan response, 1)
	go func() { result, err := Synchronize(ctx, snapshot, shop, false, opts); done <- response{result, err} }()
	marker := filepath.Join(shop, "modules", "examplemodule", "upgrade-started")
	deadline := time.After(8 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case early := <-done:
			t.Fatalf("upgrade ended before cancellation: %+v %v", early.result, early.err)
		case <-deadline:
			t.Fatal("upgrade did not reach its blocking migration")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				break wait
			}
		}
	}
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil || !strings.Contains(err.Error(), "cannot lock") {
		t.Fatalf("lock released before migration finished: %v", err)
	}
	cancel()
	select {
	case finished := <-done:
		if finished.err == nil || finished.result.Upgrade == nil || !finished.result.Upgrade.Disabled {
			t.Fatalf("cancellation skipped deactivation: %+v %v", finished.result, finished.err)
		}
		if _, err := os.Stat(filepath.Join(shop, "modules", ".presto-locks", "examplemodule")); !os.IsNotExist(err) {
			t.Fatal("operation lock remained after recovery")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not complete after cancellation")
	}
}
