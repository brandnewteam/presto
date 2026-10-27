package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/config"
)

func TestDryRunDiffsUpdatedFiles(t *testing.T) {
	source, shop := moduleFixture(t), shopFixture(t)
	put(t, source, "views/logo.png", "\x89PNG\x00new")
	put(t, source, "uploads/live.txt", "source copy")
	put(t, source, "new.php", "created")
	destination := filepath.Join(shop, "modules", "examplemodule")
	put(t, destination, "examplemodule.php", "<?php\n$this->version = '1.2.2';\n")
	put(t, destination, "src/code.php", "original")
	put(t, destination, "views/logo.png", "\x89PNG\x00old!")
	put(t, destination, "uploads/live.txt", "runtime data")
	put(t, destination, "obsolete.php", "removed")
	snapshot := prepareFixture(t, source, config.Defaults())

	plan, err := Synchronize(context.Background(), snapshot, shop, true, SyncOptions{Diff: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"examplemodule.php": "--- a/examplemodule.php\n+++ b/examplemodule.php\n@@ -1,2 +1,2 @@\n <?php\n-$this->version = '1.2.2';\n+$this->version = '1.2.3';\n",
		"views/logo.png":    "binary file differs: 9 → 8 bytes\n",
	}
	if len(plan.Diffs) != len(want) {
		t.Fatalf("diffs for created, deleted, preserved or unchanged files: %q", plan.Diffs)
	}
	for name, text := range want {
		if plan.Diffs[name] != text {
			t.Errorf("%s:\ngot  %q\nwant %q", name, plan.Diffs[name], text)
		}
	}
	data, err := json.Marshal(plan)
	if err != nil || !bytes.Contains(data, []byte(`"diffs":{"examplemodule.php":"--- a/examplemodule.php\n`)) {
		t.Fatalf("%s %v", data, err)
	}

	for _, run := range []struct {
		dry  bool
		opts SyncOptions
	}{{true, SyncOptions{}}, {false, SyncOptions{Diff: true}}} {
		result, err := Synchronize(context.Background(), snapshot, shop, run.dry, run.opts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Diffs != nil {
			t.Fatalf("unrequested diffs: %+v", result)
		}
		if data, _ := json.Marshal(result); bytes.Contains(data, []byte(`"diffs"`)) {
			t.Fatalf("empty diffs serialized: %s", data)
		}
	}
}

func TestDryRunDiffNotesAndCaps(t *testing.T) {
	source, shop := moduleFixture(t), shopFixture(t)
	destination := filepath.Join(shop, "modules", "examplemodule")
	var old, new strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&old, "old %d\n", i)
		fmt.Fprintf(&new, "new %d\n", i)
	}
	// Each long file produces MaxDiffLines + 1 lines, so the sixth exceeds the total.
	for i := 0; i < 6; i++ {
		put(t, source, fmt.Sprintf("vendor/lib%d.php", i), new.String())
		put(t, destination, fmt.Sprintf("vendor/lib%d.php", i), old.String())
	}
	large := strings.Repeat("x", maxDiffBytes+1)
	put(t, source, "large.js", large+"y")
	put(t, destination, "large.js", large)
	snapshot := prepareFixture(t, source, config.Defaults())
	plan, err := Synchronize(context.Background(), snapshot, shop, true, SyncOptions{Diff: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Diffs["large.js"]; got != fmt.Sprintf("file too large to diff: %d → %d bytes\n", maxDiffBytes+1, maxDiffBytes+2) {
		t.Fatal(got)
	}
	// Paths are diffed in order: large.js, then vendor/lib0..5.
	for i := 0; i < 5; i++ {
		lines := strings.Split(strings.TrimSuffix(plan.Diffs[fmt.Sprintf("vendor/lib%d.php", i)], "\n"), "\n")
		if len(lines) != MaxDiffLines+1 || lines[MaxDiffLines] != "… 1603 more lines" {
			t.Fatalf("lib%d: %d lines, last %q", i, len(lines), lines[len(lines)-1])
		}
	}
	if got := plan.Diffs["vendor/lib5.php"]; got != "diff omitted: output limit reached\n" {
		t.Fatal(got)
	}
}

func TestDryRunDiffReportsModeChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes are not compared on Windows")
	}
	source, shop := moduleFixture(t), shopFixture(t)
	if err := os.Chmod(filepath.Join(source, "src", "code.php"), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(shop, "modules", "examplemodule"), "src/code.php", "original")
	plan, err := Synchronize(context.Background(), prepareFixture(t, source, config.Defaults()), shop, true, SyncOptions{Diff: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Diffs["src/code.php"]; got != "file mode changed: 0644 → 0755\n" {
		t.Fatalf("%q", got)
	}
}

func TestReceiveReturnsDiffs(t *testing.T) {
	snapshot := prepareFixture(t, moduleFixture(t), config.Defaults())
	var wire bytes.Buffer
	if err := WriteArchive(context.Background(), snapshot, &wire, true); err != nil {
		t.Fatal(err)
	}
	shop := shopFixture(t)
	put(t, filepath.Join(shop, "modules", "examplemodule"), "src/code.php", "debugging")
	result, err := Receive(context.Background(), bytes.NewReader(wire.Bytes()), shop, true, SyncOptions{Diff: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "--- a/src/code.php\n+++ b/src/code.php\n@@ -1 +1 @@\n-debugging\n\\ No newline at end of file\n+original\n\\ No newline at end of file\n"
	if result.Diffs["src/code.php"] != want {
		t.Fatalf("%q", result.Diffs)
	}
}
