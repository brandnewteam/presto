package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiCyan  = "\x1b[36m"
)

// colorEnabled resolves --color; auto colors only a terminal without NO_COLOR.
func colorEnabled(mode string, w io.Writer) (bool, error) {
	file, isFile := w.(*os.File)
	switch mode {
	case "always":
		if isFile {
			enableVirtualTerminal(file)
		}
		return true, nil
	case "never":
		return false, nil
	case "auto":
	default:
		return false, fmt.Errorf("invalid --color %q: use auto, always or never", mode)
	}
	if !isFile || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false, nil
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, nil
	}
	return enableVirtualTerminal(file), nil
}

// printDiff indents one Result.Diffs entry below its path. Headers are the ---/+++
// lines before the first hunk; notes such as "binary file differs" stay plain.
func printDiff(w io.Writer, text string, color bool) {
	inHunks := false
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		style := ""
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunks, style = true, ansiCyan
		case !inHunks && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
			style = ansiBold
		case inHunks && strings.HasPrefix(line, "+"):
			style = ansiGreen
		case inHunks && strings.HasPrefix(line, "-"):
			style = ansiRed
		}
		line = printable(line)
		if color && style != "" {
			line = style + line + ansiReset
		}
		fmt.Fprintf(w, "    %s\n", line)
	}
}

// printable keeps file content from driving the terminal; tabs and a CRLF line end stay.
func printable(line string) string {
	body, end := line, ""
	if strings.HasSuffix(body, "\r") {
		body, end = body[:len(body)-1], "\r"
	}
	return strings.Map(func(r rune) rune {
		if r != '\t' && unicode.IsControl(r) {
			return '�'
		}
		return r
	}, body) + end
}
