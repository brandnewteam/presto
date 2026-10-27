package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brandnewteam/presto/internal/diff"
)

// DiffCapability marks receivers that accept `receive --diff` for dry runs.
const DiffCapability = "dry-run-diff-v1"

// Output caps keep a large vendor update readable, locally and in receiver JSON.
const (
	MaxDiffLines      = 400
	MaxTotalDiffLines = 2000
	maxDiffBytes      = 4 << 20
)

// updatedDiffs describes each updated file from the destination copy to the new one.
// Preserved paths never reach Updated, so they are never read here.
func updatedDiffs(ctx context.Context, snapshot *Snapshot, destination string, updated []string, wanted map[string]File, existing map[string]fs.FileInfo) (map[string]string, error) {
	diffs := make(map[string]string, len(updated))
	total := 0
	for _, rel := range updated {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if total >= MaxTotalDiffLines {
			diffs[rel] = "diff omitted: output limit reached\n"
			continue
		}
		file, info := wanted[rel], existing[rel]
		var notes []string
		if runtime.GOOS != "windows" && info.Mode().Perm() != fs.FileMode(file.Mode) {
			notes = append(notes, fmt.Sprintf("file mode changed: %04o → %04o", info.Mode().Perm(), file.Mode))
		}
		text, err := fileDiff(rel, filepath.Join(destination, filepath.FromSlash(rel)), filepath.Join(snapshot.Root, filepath.FromSlash(rel)), info.Size(), file.Size)
		if err != nil {
			return nil, fmt.Errorf("diff %s: %w", rel, err)
		}
		if text != "" {
			notes = append(notes, strings.TrimSuffix(text, "\n"))
		}
		text = strings.Join(notes, "\n") + "\n"
		total += strings.Count(text, "\n")
		diffs[rel] = text
	}
	return diffs, nil
}

func fileDiff(rel, oldPath, newPath string, oldSize, newSize int64) (string, error) {
	sizes := fmt.Sprintf("%d → %d bytes", oldSize, newSize)
	if oldSize > maxDiffBytes || newSize > maxDiffBytes {
		return "file too large to diff: " + sizes, nil
	}
	old, err := os.ReadFile(oldPath)
	if err != nil {
		return "", err
	}
	new, err := os.ReadFile(newPath)
	if err != nil {
		return "", err
	}
	if diff.Binary(old) || diff.Binary(new) {
		return "binary file differs: " + sizes, nil
	}
	return diff.Unified(rel, old, new, MaxDiffLines), nil
}
