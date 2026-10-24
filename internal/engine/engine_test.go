package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brandnewteam/presto/internal/config"
)

func put(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func moduleFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "examplemodule")
	put(t, root, "examplemodule.php", "<?php\n$this->version = '1.2.3';\n")
	put(t, root, "src/code.php", "original")
	return root
}

func shopFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put(t, root, "config/config.inc.php", "<?php // fixture")
	if err := os.Mkdir(filepath.Join(root, "modules"), 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func prepareFixture(t *testing.T, source string, cfg config.Config) *Snapshot {
	t.Helper()
	snapshot, cleanup, err := Prepare(context.Background(), source, cfg, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return snapshot
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSyncOverwritesDebugEditsAndPreservesRuntime(t *testing.T) {
	source, shop := moduleFixture(t), shopFixture(t)
	put(t, source, "uploads/source.txt", "do not copy")
	put(t, source, "tests/dev.php", "do not copy")
	destination := filepath.Join(shop, "modules", "examplemodule")
	put(t, destination, "src/code.php", "modified") // Same size as original.
	put(t, destination, "obsolete.php", "delete me")
	put(t, destination, "uploads/live.txt", "keep me")
	put(t, shop, "modules/anothermodule/keep.php", "other module")
	snapshot := prepareFixture(t, source, config.Defaults())
	info, _ := os.Stat(filepath.Join(snapshot.Root, "src", "code.php"))
	if err := os.Chtimes(filepath.Join(destination, "src", "code.php"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	plan, err := Synchronize(context.Background(), snapshot, shop, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Updated, []string{"src/code.php"}) || !reflect.DeepEqual(plan.Deleted, []string{"obsolete.php"}) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if got := read(t, destination, "src/code.php"); got != "modified" {
		t.Fatalf("dry run changed file: %s", got)
	}
	if _, err := os.Stat(filepath.Join(shop, "modules", ".presto-locks")); !os.IsNotExist(err) {
		t.Fatal("dry run created a lock directory")
	}
	result, err := Synchronize(context.Background(), snapshot, shop, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun {
		t.Fatal("apply marked as dry run")
	}
	if got := read(t, destination, "src/code.php"); got != "original" {
		t.Fatal(got)
	}
	if got := read(t, destination, "uploads/live.txt"); got != "keep me" {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(destination, "uploads", "source.txt")); !os.IsNotExist(err) {
		t.Fatal("copied runtime file")
	}
	if _, err := os.Stat(filepath.Join(destination, "obsolete.php")); !os.IsNotExist(err) {
		t.Fatal("obsolete file remains")
	}
	if got := read(t, shop, "modules/anothermodule/keep.php"); got != "other module" {
		t.Fatal(got)
	}
	result, err = Synchronize(context.Background(), snapshot, shop, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Created)+len(result.Updated)+len(result.Deleted) != 0 || result.Unchanged != 2 {
		t.Fatalf("not idempotent: %+v", result)
	}
}

func TestExclusionDoesNotProtectDestinationAndIncludeOverridesExclusion(t *testing.T) {
	source, shop := moduleFixture(t), shopFixture(t)
	put(t, source, ".env.example", "template")
	put(t, source, ".env.production", "private")
	put(t, source, "views/cache/runtime.txt", "source runtime")
	destination := filepath.Join(shop, "modules", "examplemodule")
	put(t, destination, "tests/stale.php", "old development file")
	put(t, destination, "views/cache/live.txt", "live")
	cfg := config.Defaults()
	cfg.Files.Include = []string{".env.example"}
	cfg.Files.Preserve = append(cfg.Files.Preserve, "views/cache/")
	snapshot := prepareFixture(t, source, cfg)
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, destination, ".env.example"); got != "template" {
		t.Fatal(got)
	}
	if got := read(t, destination, "views/cache/live.txt"); got != "live" {
		t.Fatal(got)
	}
	for _, name := range []string{".env.production", "tests/stale.php", "views/cache/runtime.txt"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("unexpected file %s", name)
		}
	}
}

func TestDestinationGuardsAndSymlinksBeforeWrites(t *testing.T) {
	source := moduleFixture(t)
	snapshot := prepareFixture(t, source, config.Defaults())
	if _, err := Synchronize(context.Background(), snapshot, t.TempDir(), false); err == nil {
		t.Fatal("accepted non-shop")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	shop := shopFixture(t)
	destination := filepath.Join(shop, "modules", "examplemodule")
	outside := t.TempDir()
	put(t, outside, "keep.txt", "safe")
	if err := os.Symlink(outside, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil {
		t.Fatal("accepted symlink module root")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	put(t, destination, "examplemodule.php", "unchanged")
	if err := os.Symlink(outside, filepath.Join(destination, "src")); err != nil {
		t.Fatal(err)
	}
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil {
		t.Fatal("accepted code symlink")
	}
	if got := read(t, destination, "examplemodule.php"); got != "unchanged" {
		t.Fatal("wrote before preflight completed")
	}
	if got := read(t, outside, "keep.txt"); got != "safe" {
		t.Fatal("escaped module root")
	}
}

func TestSourceSymlinkAndOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	source := moduleFixture(t)
	if err := os.Symlink(filepath.Join(source, "examplemodule.php"), filepath.Join(source, "linked.php")); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := Prepare(context.Background(), source, config.Defaults(), false, io.Discard); err == nil {
		cleanup()
		t.Fatal("accepted source symlink")
	}
	if err := os.Remove(filepath.Join(source, "linked.php")); err != nil {
		t.Fatal(err)
	}
	shop := shopFixture(t)
	destination := filepath.Join(shop, "modules", "examplemodule")
	put(t, destination, "examplemodule.php", "<?php\n$this->version = '1.0.0';\n")
	snapshot := prepareFixture(t, destination, config.Defaults())
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil {
		t.Fatal("accepted source in target")
	}
}

func TestModuleLockAndTypeConflict(t *testing.T) {
	source, shop := moduleFixture(t), shopFixture(t)
	snapshot := prepareFixture(t, source, config.Defaults())
	destination := filepath.Join(shop, "modules", "examplemodule")
	unlock, err := lockModule(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil {
		t.Fatal("ignored existing lock")
	}
	unlock()
	put(t, destination, "src", "file instead of directory")
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil {
		t.Fatal("accepted type conflict")
	}
	if got := read(t, destination, "src"); got != "file instead of directory" {
		t.Fatal(got)
	}
}

func TestCaseMismatchStopsBeforeChanges(t *testing.T) {
	snapshot := prepareFixture(t, moduleFixture(t), config.Defaults())
	shop := shopFixture(t)
	put(t, shop, "modules/examplemodule/SRC/code.php", "debugged")
	put(t, shop, "modules/examplemodule/obsolete.php", "keep until successful preflight")
	if _, err := Synchronize(context.Background(), snapshot, shop, false); err == nil {
		t.Fatal("accepted a case mismatch")
	}
	if got := read(t, shop, "modules/examplemodule/obsolete.php"); got != "keep until successful preflight" {
		t.Fatal(got)
	}
}

func TestReproducibleBuildAndReceive(t *testing.T) {
	snapshot := prepareFixture(t, moduleFixture(t), config.Defaults())
	var first, second, wire bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		if err := WriteArchive(context.Background(), snapshot, out, false); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("ZIP is not reproducible")
	}
	archive, err := zip.NewReader(bytes.NewReader(first.Bytes()), int64(first.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		if !strings.HasPrefix(entry.Name, "examplemodule/") {
			t.Fatalf("unexpected release entry %q", entry.Name)
		}
	}
	if err := WriteArchive(context.Background(), snapshot, &wire, true); err != nil {
		t.Fatal(err)
	}
	shop := shopFixture(t)
	if _, err := Receive(context.Background(), bytes.NewReader(wire.Bytes()), shop, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shop, "modules", "examplemodule")); !os.IsNotExist(err) {
		t.Fatal("remote dry run created module")
	}
	if _, err := Receive(context.Background(), bytes.NewReader(wire.Bytes()), shop, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(shop, "modules", "examplemodule"), "src/code.php"); got != "original" {
		t.Fatal(got)
	}
}

func TestReceiveRejectsMalformedArchiveWithoutChangingShop(t *testing.T) {
	snapshot := prepareFixture(t, moduleFixture(t), config.Defaults())
	for _, test := range []string{"traversal", "duplicate", "checksum", "protocol", "symlink", "extra-file", "truncated"} {
		t.Run(test, func(t *testing.T) {
			shop := shopFixture(t)
			put(t, shop, "modules/examplemodule/keep.php", "keep")
			var wire bytes.Buffer
			writer := zip.NewWriter(&wire)
			req := request{Protocol, *snapshot}
			if test == "protocol" {
				req.Protocol = 99
			}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := writer.Create(requestName)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = entry.Write(data)
			for _, file := range snapshot.Files {
				h := header(snapshot.Name+"/"+file.Path, 0644)
				if test == "symlink" && file.Path == "src/code.php" {
					h.SetMode(os.ModeSymlink | 0777)
				}
				entry, err := writer.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				data := []byte(read(t, snapshot.Root, file.Path))
				if test == "checksum" && file.Path == "src/code.php" {
					data = []byte("modified")
				}
				_, _ = entry.Write(data)
			}
			switch test {
			case "traversal":
				entry, _ = writer.Create("../escape")
			case "duplicate":
				entry, _ = writer.Create(requestName)
			case "extra-file":
				entry, _ = writer.Create("examplemodule/extra.php")
			}
			if test == "traversal" || test == "duplicate" || test == "extra-file" {
				_, _ = entry.Write([]byte("bad"))
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			payload := wire.Bytes()
			if test == "truncated" {
				payload = payload[:wire.Len()/2]
			}
			if _, err := Receive(context.Background(), bytes.NewReader(payload), shop, false); err == nil {
				t.Fatalf("accepted %s", test)
			}
			if got := read(t, shop, "modules/examplemodule/keep.php"); got != "keep" {
				t.Fatal(got)
			}
			if _, err := os.Stat(filepath.Join(shop, "modules", "examplemodule", "examplemodule.php")); !os.IsNotExist(err) {
				t.Fatal("partial apply before validation")
			}
		})
	}
}

func TestComposerProcess(t *testing.T) {
	mode := os.Getenv("PRESTO_TEST_COMPOSER")
	if mode == "" {
		return
	}
	joined := strings.Join(os.Args, " ")
	for _, flag := range []string{"install", "--no-dev", "--optimize-autoloader", "--no-scripts", "--no-plugins"} {
		if !strings.Contains(joined, flag) {
			os.Exit(8)
		}
	}
	if mode == "fail" {
		os.Exit(7)
	}
	if _, err := os.Stat("vendor/dev/unsafe.php"); !os.IsNotExist(err) {
		os.Exit(9)
	}
	if err := os.MkdirAll("vendor/runtime", 0755); err != nil {
		os.Exit(10)
	}
	if err := os.WriteFile("vendor/runtime/library.php", []byte("production"), 0644); err != nil {
		os.Exit(11)
	}
	if err := os.WriteFile("composer.lock", []byte("prepared lock"), 0644); err != nil {
		os.Exit(12)
	}
	os.Exit(0)
}

func TestComposerIsolationAndFailure(t *testing.T) {
	source := moduleFixture(t)
	put(t, source, "composer.json", "{}")
	put(t, source, "composer.lock", "original lock")
	put(t, source, "vendor/dev/unsafe.php", "dev dependency")
	cfg := config.Defaults()
	cfg.Composer.Command = []string{os.Args[0], "-test.run=^TestComposerProcess$", "--"}
	t.Setenv("PRESTO_TEST_COMPOSER", "ok")
	snapshot := prepareFixture(t, source, cfg)
	if got := read(t, snapshot.Root, "vendor/runtime/library.php"); got != "production" {
		t.Fatal(got)
	}
	if got := read(t, source, "composer.lock"); got != "original lock" {
		t.Fatal("source lock changed")
	}
	if got := read(t, source, "vendor/dev/unsafe.php"); got != "dev dependency" {
		t.Fatal("source vendor changed")
	}
	t.Setenv("PRESTO_TEST_COMPOSER", "fail")
	if _, cleanup, err := Prepare(context.Background(), source, cfg, false, io.Discard); err == nil {
		cleanup()
		t.Fatal("ignored Composer failure")
	}
}

func TestRealComposerPreparation(t *testing.T) {
	if _, err := exec.LookPath("composer"); err != nil {
		t.Skip("Composer not installed")
	}
	source := moduleFixture(t)
	put(t, source, "composer.json", `{"name":"presto/fixture","version":"1.0.0","autoload":{"classmap":["src/"]}}`)
	t.Setenv("COMPOSER_HOME", t.TempDir())
	t.Setenv("COMPOSER_CACHE_DIR", t.TempDir())
	t.Setenv("COMPOSER_DISABLE_NETWORK", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var logs bytes.Buffer
	snapshot, cleanup, err := Prepare(ctx, source, config.Defaults(), false, &logs)
	if err != nil {
		t.Fatalf("real Composer: %v\n%s", err, logs.String())
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(snapshot.Root, "vendor", "autoload.php")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "vendor")); !os.IsNotExist(err) {
		t.Fatal("Composer modified original project")
	}
	if _, err := os.Stat(filepath.Join(source, "composer.lock")); !os.IsNotExist(err) {
		t.Fatal("Composer wrote original lock")
	}
}

func TestSharedSyncContract(t *testing.T) {
	data, err := os.ReadFile("../../testdata/contracts/sync.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Module              string            `json:"module"`
		Source              map[string]string `json:"source"`
		Destination         map[string]string `json:"destination"`
		Include             []string          `json:"include"`
		Expected            Result            `json:"expected"`
		ExpectedDestination map[string]string `json:"expected_destination"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), fixture.Module)
	for name, content := range fixture.Source {
		put(t, source, name, content)
	}
	shop := shopFixture(t)
	destination := filepath.Join(shop, "modules", fixture.Module)
	for name, content := range fixture.Destination {
		put(t, destination, name, content)
	}
	cfg := config.Defaults()
	cfg.Files.Include = fixture.Include
	snapshot := prepareFixture(t, source, cfg)
	result, err := Synchronize(context.Background(), snapshot, shop, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Created, fixture.Expected.Created) || !reflect.DeepEqual(result.Updated, fixture.Expected.Updated) || !reflect.DeepEqual(result.Deleted, fixture.Expected.Deleted) || result.Unchanged != fixture.Expected.Unchanged {
		t.Fatalf("contract result mismatch: %+v", result)
	}
	actual := map[string]string{}
	if err := filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(destination, path)
		if err != nil {
			return err
		}
		actual[filepath.ToSlash(rel)] = read(t, destination, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, fixture.ExpectedDestination) {
		t.Fatalf("contract destination mismatch: %#v", actual)
	}
}

func TestRealComposerCustomVendorDirectoryAndEnvironment(t *testing.T) {
	if _, err := exec.LookPath("composer"); err != nil {
		t.Skip("Composer not installed")
	}
	source := moduleFixture(t)
	put(t, source, "composer.json", `{"name":"presto/fixture","version":"1.0.0","config":{"vendor-dir":"libraries","bin-dir":"tools"},"autoload":{"classmap":["src/"]}}`)
	put(t, source, "libraries/obsolete-dev.php", "dev")
	put(t, source, "tools/obsolete-tool.php", "dev")
	outside := t.TempDir()
	t.Setenv("COMPOSER", filepath.Join(outside, "must-not-be-used.json"))
	t.Setenv("COMPOSER_VENDOR_DIR", outside)
	t.Setenv("COMPOSER_BIN_DIR", outside)
	t.Setenv("COMPOSER_HOME", t.TempDir())
	t.Setenv("COMPOSER_CACHE_DIR", t.TempDir())
	t.Setenv("COMPOSER_DISABLE_NETWORK", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var logs bytes.Buffer
	snapshot, cleanup, err := Prepare(ctx, source, config.Defaults(), false, &logs)
	if err != nil {
		t.Fatalf("%v\n%s", err, logs.String())
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(snapshot.Root, "libraries", "autoload.php")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "libraries", "obsolete-dev.php")); !os.IsNotExist(err) {
		t.Fatal("copied old vendor")
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "tools", "obsolete-tool.php")); !os.IsNotExist(err) {
		t.Fatal("copied old bin directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("Composer wrote outside preparation")
	}
}
