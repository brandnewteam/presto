package diff

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestUnifiedMatchesDiffU(t *testing.T) {
	old := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n"
	new := "one\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\nthirteen\n"
	want := `--- a/views/x.tpl
+++ b/views/x.tpl
@@ -1,6 +1,6 @@
 one
 two
-three
+THREE
 four
 five
 six
@@ -10,3 +10,4 @@
 ten
 eleven
 twelve
+thirteen
`
	if got := Unified("views/x.tpl", []byte(old), []byte(new), 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestUnifiedEdgeCases(t *testing.T) {
	for _, tc := range []struct{ old, new, want string }{
		{"same\n", "same\n", ""},
		{"", "added\n", "@@ -0,0 +1 @@\n+added\n"},
		{"gone\n", "", "@@ -1 +0,0 @@\n-gone\n"},
		{"a\nb", "a\nb\n", "@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n"},
		{"a\nb\nc\nd\ne\nf\ng\nh\n", "a\nb\nc\nX\ne\nf\ng\nY\n", "@@ -1,8 +1,8 @@\n a\n b\n c\n-d\n+X\n e\n f\n g\n-h\n+Y\n"},
		{"x\r\ny\r\n", "x\r\nz\r\n", "@@ -1,2 +1,2 @@\n x\r\n-y\r\n+z\r\n"},
	} {
		got := Unified("f", []byte(tc.old), []byte(tc.new), 0)
		if tc.want != "" {
			tc.want = "--- a/f\n+++ b/f\n" + tc.want
		}
		if got != tc.want {
			t.Errorf("%q → %q:\ngot\n%s\nwant\n%s", tc.old, tc.new, got, tc.want)
		}
	}
}

func TestUnifiedCapsLines(t *testing.T) {
	var old, new strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&old, "old %d\n", i)
		fmt.Fprintf(&new, "new %d\n", i)
	}
	got := strings.Split(strings.TrimSuffix(Unified("f", []byte(old.String()), []byte(new.String()), 10), "\n"), "\n")
	// 2 headers + 1 hunk header + 200 changed lines, cut after 10.
	if len(got) != 11 || got[10] != "… 193 more lines" {
		t.Fatalf("%q", got)
	}
	if got := Unified("f", []byte("a\n"), []byte("b\n"), 5); strings.Contains(got, "more") {
		t.Fatalf("capped a diff within the limit: %q", got)
	}
}

func TestBinary(t *testing.T) {
	if Binary([]byte("plain text ü\n")) || !Binary([]byte("a\x00b")) || !Binary([]byte{0xff, 0xfe}) {
		t.Fatal("misclassified content")
	}
}

func TestLargeRewriteFallsBack(t *testing.T) {
	var old, new strings.Builder
	for i := 0; i < maxEdits; i++ {
		fmt.Fprintf(&old, "old %d\n", i)
		fmt.Fprintf(&new, "new %d\n", i)
	}
	got := Unified("f", []byte(old.String()), []byte(new.String()), 0)
	if lines := strings.Count(got, "\n"); lines != 3+2*maxEdits {
		t.Fatalf("%d lines", lines)
	}
	if !strings.HasPrefix(got, "--- a/f\n+++ b/f\n@@ -1,1500 +1,1500 @@\n-old 0\n") || !strings.HasSuffix(got, "+new 1499\n") {
		t.Fatal(got[:200])
	}
}

// Random inputs over a small alphabet: the script must rebuild both sides and be minimal.
func TestScriptIsMinimalAndApplies(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	text := func() []string {
		lines := make([]string, random.Intn(12))
		for i := range lines {
			lines[i] = string(rune('a'+random.Intn(3))) + "\n"
		}
		return lines
	}
	for round := 0; round < 2000; round++ {
		a, b := text(), text()
		script := edits(a, b)
		var gotA, gotB []string
		changes := 0
		for _, e := range script {
			if e.kind != '+' {
				gotA = append(gotA, e.line)
			}
			if e.kind != '-' {
				gotB = append(gotB, e.line)
			}
			if e.kind != ' ' {
				changes++
			}
		}
		if strings.Join(gotA, "") != strings.Join(a, "") || strings.Join(gotB, "") != strings.Join(b, "") {
			t.Fatalf("%q %q: %v", a, b, script)
		}
		if want := len(a) + len(b) - 2*lcs(a, b); changes != want {
			t.Fatalf("%q %q: %d changes, want %d", a, b, changes, want)
		}
	}
}

func lcs(a, b []string) int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	return table[0][0]
}
