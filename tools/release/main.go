// Command release builds platform executables and checksums without shell scripts.
package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	version := flag.String("version", "dev", "release version")
	output := flag.String("output", "dist", "output directory")
	flag.Parse()
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`).MatchString(*version) {
		fatal(fmt.Errorf("invalid version"))
	}
	if err := os.MkdirAll(*output, 0755); err != nil {
		fatal(err)
	}
	var checksums strings.Builder
	for _, target := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		name := "presto-" + target[0] + "-" + target[1]
		if target[0] == "windows" {
			name += ".exe"
		}
		path := filepath.Join(*output, name)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+*version, "-o", path, ".")
		cmd.Env = append(os.Environ(), "GOOS="+target[0], "GOARCH="+target[1], "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fmt.Println("Building", name)
		if err := cmd.Run(); err != nil {
			fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fatal(err)
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(data), name)
	}
	if err := os.WriteFile(filepath.Join(*output, "checksums.sha256"), []byte(checksums.String()), 0644); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "release:", err); os.Exit(1) }
