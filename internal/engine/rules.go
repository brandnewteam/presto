package engine

import (
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/brandnewteam/presto/internal/config"
)

type Rules struct {
	Exclude  []string `json:"exclude"`
	Include  []string `json:"include"`
	Preserve []string `json:"preserve"`
}

func NewRules(files config.Files) Rules {
	return Rules{append([]string{}, files.Exclude...), append([]string{}, files.Include...), append([]string{}, files.Preserve...)}
}

// matches applies a pattern to a path. A trailing / or /** restricts the pattern to
// directories, so the path itself only matches when dir reports that it is one;
// its ancestors are always directories.
func matches(pattern, name string, dir bool) bool {
	anchored := strings.HasPrefix(pattern, "/")
	directoryOnly := strings.HasSuffix(pattern, "/") || strings.HasSuffix(pattern, "/**")
	pattern = strings.Trim(strings.TrimSuffix(pattern, "/**"), "/")
	for candidate := name; candidate != "." && candidate != ""; candidate = path.Dir(candidate) {
		if directoryOnly && candidate == name && !dir {
			continue
		}
		matchName := candidate
		if !anchored && !strings.Contains(pattern, "/") {
			matchName = path.Base(candidate)
		}
		if ok, _ := doublestar.Match(pattern, matchName); ok {
			return true
		}
	}
	return false
}

func anyMatch(patterns []string, name string, dir bool) bool {
	for _, pattern := range patterns {
		if matches(pattern, name, dir) {
			return true
		}
	}
	return false
}

// Preserved paths are neither shipped nor changed, irrespective of include rules.
func (r Rules) Preserved(name string) bool { return anyMatch(r.Preserve, name, false) }

// PreservedDir is Preserved for a directory path.
func (r Rules) PreservedDir(name string) bool { return anyMatch(r.Preserve, name, true) }

// Selected reports whether a file is part of the module.
func (r Rules) Selected(name string) bool { return r.selected(name, false) }

// SelectedDir is Selected for a directory path.
func (r Rules) SelectedDir(name string) bool { return r.selected(name, true) }

func (r Rules) selected(name string, dir bool) bool {
	if anyMatch(r.Preserve, name, dir) || reserved(name) {
		return false
	}
	return !anyMatch(r.Exclude, name, dir) || anyMatch(r.Include, name, dir)
}

func reserved(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == "presto.toml" || part == "presto.local.toml" || part == ".presto-request.json" || part == ".presto-locks" {
			return true
		}
	}
	return false
}
