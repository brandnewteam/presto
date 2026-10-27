// Package diff renders line-based unified diffs like `diff -u` without external tools.
package diff

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Context is the number of unchanged lines shown around each change, as in `diff -u`.
const Context = 3

// maxEdits bounds the Myers search so time and trace memory stay small. Beyond it the
// differing middle is shown as one removal followed by one addition: still correct,
// just not minimal, and such diffs exceed the per-file output cap anyway.
const maxEdits = 1500

const noNewline = `\ No newline at end of file`

// Binary reports content that should not be shown line by line.
func Binary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

// Unified returns the diff from old to new with a/ and b/ headers, or "" when both
// are equal. After maxLines output lines it stops with "… N more lines"; maxLines <= 0
// disables the cap. Callers check Binary first.
func Unified(name string, old, new []byte, maxLines int) string {
	if bytes.Equal(old, new) {
		return ""
	}
	a, b := split(old), split(new)
	lines := append([]string{"--- a/" + name, "+++ b/" + name}, hunks(edits(a, b))...)
	if maxLines > 0 && len(lines) > maxLines {
		hidden := len(lines) - maxLines
		lines = append(lines[:maxLines], fmt.Sprintf("… %d more %s", hidden, plural(hidden, "line", "lines")))
	}
	return strings.Join(lines, "\n") + "\n"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// split keeps each line's "\n" so a missing final newline is itself a difference.
func split(data []byte) []string {
	var lines []string
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n') + 1
		if end == 0 {
			end = len(data)
		}
		lines = append(lines, string(data[:end]))
		data = data[end:]
	}
	return lines
}

type edit struct {
	kind byte // ' ', '-' or '+'
	line string
}

func edits(a, b []string) []edit {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	script := make([]edit, 0, len(a)+len(b))
	for _, line := range a[:prefix] {
		script = append(script, edit{' ', line})
	}
	middleA, middleB := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	if middle, ok := myers(middleA, middleB); ok {
		script = append(script, middle...)
	} else {
		for _, line := range middleA {
			script = append(script, edit{'-', line})
		}
		for _, line := range middleB {
			script = append(script, edit{'+', line})
		}
	}
	for _, line := range a[len(a)-suffix:] {
		script = append(script, edit{' ', line})
	}
	return script
}

// myers finds a shortest edit script (Myers 1986). trace[d] holds the furthest x for
// diagonals -d..d after d edits, which is enough to walk the path back.
func myers(a, b []string) ([]edit, bool) {
	n, m := len(a), len(b)
	offset := n + m + 1
	v := make([]int, 2*offset+1)
	var trace [][]int
	for d := 0; d <= min(n+m, maxEdits); d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1] // down: insertion
			} else {
				x = v[offset+k-1] + 1 // right: deletion
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[offset+k] = x
			if x >= n && y >= m {
				// Backtracking reads only the earlier steps.
				return backtrack(a, b, append(trace, nil)), true
			}
		}
		trace = append(trace, append([]int(nil), v[offset-d:offset+d+1]...))
	}
	return nil, false
}

func backtrack(a, b []string, trace [][]int) []edit {
	var reversed []edit
	x, y := len(a), len(b)
	for d := len(trace) - 1; d > 0; d-- {
		previous := trace[d-1] // index k+(d-1)
		at := func(k int) int { return previous[k+d-1] }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x, y = x-1, y-1
			reversed = append(reversed, edit{' ', a[x]})
		}
		if x == prevX {
			reversed = append(reversed, edit{'+', b[prevY]})
		} else {
			reversed = append(reversed, edit{'-', a[prevX]})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x, y = x-1, y-1
		reversed = append(reversed, edit{' ', a[x]})
	}
	script := make([]edit, len(reversed))
	for i, e := range reversed {
		script[len(reversed)-1-i] = e
	}
	return script
}

func hunks(script []edit) []string {
	// starts[i] is the number of old and new lines before script[i].
	type position struct{ a, b int }
	starts := make([]position, len(script)+1)
	for i, e := range script {
		next := starts[i]
		if e.kind != '+' {
			next.a++
		}
		if e.kind != '-' {
			next.b++
		}
		starts[i+1] = next
	}
	var out []string
	for i := 0; i < len(script); {
		for i < len(script) && script[i].kind == ' ' {
			i++
		}
		if i == len(script) {
			break
		}
		start, end := max(i-Context, 0), i
		for j := i; j < len(script) && j-end <= 2*Context; j++ {
			if script[j].kind != ' ' {
				end = j
			}
		}
		stop := min(end+Context+1, len(script))
		from, to := starts[start], starts[stop]
		out = append(out, fmt.Sprintf("@@ -%s +%s @@", span(from.a, to.a-from.a), span(from.b, to.b-from.b)))
		for _, e := range script[start:stop] {
			out = append(out, string(e.kind)+strings.TrimSuffix(e.line, "\n"))
			if !strings.HasSuffix(e.line, "\n") {
				out = append(out, noNewline)
			}
		}
		i = stop
	}
	return out
}

// span uses diff's convention: an empty range names the line before it.
func span(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
