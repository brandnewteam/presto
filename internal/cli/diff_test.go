package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/engine"
	"github.com/brandnewteam/presto/internal/testshop"
)

func newDiffFixture(t *testing.T) syncFixture {
	t.Helper()
	f := newSyncFixture(t)
	testshop.Put(t, f.alpha, "modules/examplemodule/views/template.tpl", "old template\n\x1b[2J")
	testshop.Put(t, f.alpha, "modules/examplemodule/uploads/live.txt", "runtime data changed")
	return f
}

const templateDiff = "--- a/views/template.tpl\n+++ b/views/template.tpl\n@@ -1,2 +1 @@\n-old template\n-\x1b[2J\n\\ No newline at end of file\n+new template\n\\ No newline at end of file\n"

func TestDryRunPrintsDiffAfterPath(t *testing.T) {
	f := newDiffFixture(t)
	t.Setenv("NO_COLOR", "")
	plain := strings.Join([]string{
		"  ~ views/template.tpl",
		"    --- a/views/template.tpl",
		"    +++ b/views/template.tpl",
		"    @@ -1,2 +1 @@",
		"    -old template",
		"    -\uFFFD[2J", // escape sequences from file content are neutralized
		`    \ No newline at end of file`,
		"    +new template",
		`    \ No newline at end of file`,
		"  - obsolete.php",
	}, "\n")
	for _, mode := range []string{"auto", "never"} {
		out, err := execute(t, f.args("alpha", "--dry-run", "--color", mode)...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, plain) || strings.Contains(out, "\x1b") {
			t.Fatalf("--color=%s:\n%s", mode, out)
		}
	}
	t.Setenv("NO_COLOR", "1") // An explicit flag wins.
	out, err := execute(t, f.args("alpha", "--dry-run", "--color=always")...)
	if err != nil {
		t.Fatal(err)
	}
	colored := strings.Join([]string{
		"  ~ views/template.tpl",
		"    \x1b[1m--- a/views/template.tpl\x1b[0m",
		"    \x1b[1m+++ b/views/template.tpl\x1b[0m",
		"    \x1b[36m@@ -1,2 +1 @@\x1b[0m",
		"    \x1b[31m-old template\x1b[0m",
		"    \x1b[31m-\uFFFD[2J\x1b[0m",
		`    \ No newline at end of file`,
		"    \x1b[32m+new template\x1b[0m",
	}, "\n")
	if !strings.Contains(out, colored) || strings.Contains(out, "runtime data") {
		t.Fatalf("%q", out)
	}
	if _, err := execute(t, f.args("alpha", "--dry-run", "--color=sometimes")...); err == nil || !strings.Contains(err.Error(), "invalid --color") {
		t.Fatal(err)
	}
}

func TestDryRunJSONCarriesDiffs(t *testing.T) {
	f := newDiffFixture(t)
	out, err := execute(t, f.args("alpha", "--dry-run", "--json", "--color=always")...)
	if err != nil {
		t.Fatal(err)
	}
	var result engine.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err, out)
	}
	if len(result.Diffs) != 1 || result.Diffs["views/template.tpl"] != templateDiff {
		t.Fatalf("%q", result.Diffs)
	}
	out, err = execute(t, f.args("--dry-run", "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var results []syncTargetResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err, out)
	}
	if results[0].Diffs["views/template.tpl"] != templateDiff || results[1].Diffs != nil {
		t.Fatal(out)
	}
	out, err = execute(t, f.args("alpha", "--json")...)
	if err != nil || strings.Contains(out, `"diffs"`) {
		t.Fatalf("apply returned diffs: %s %v", out, err)
	}
}

func TestColorAutoNeedsTerminal(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	if color, err := colorEnabled("auto", file); err != nil || color {
		t.Fatal("colored a regular file", err)
	}
	if color, err := colorEnabled("always", file); err != nil || !color {
		t.Fatal("ignored --color=always", err)
	}
	if terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer terminal.Close()
		if color, _ := colorEnabled("auto", terminal); !color {
			t.Error("no color on a terminal")
		}
		t.Setenv("NO_COLOR", "1")
		if color, _ := colorEnabled("auto", terminal); color {
			t.Error("ignored NO_COLOR")
		}
	}
}
