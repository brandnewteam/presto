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

func matches(pattern, name string) bool {
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.Trim(strings.TrimSuffix(pattern, "/**"), "/")
	for candidate := name; candidate != "." && candidate != ""; candidate = path.Dir(candidate) {
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

func anyMatch(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if matches(pattern, name) {
			return true
		}
	}
	return false
}

// Preserved paths are neither shipped nor changed, irrespective of include rules.
func (r Rules) Preserved(name string) bool { return anyMatch(r.Preserve, name) }

func (r Rules) Selected(name string) bool {
	if r.Preserved(name) || reserved(name) {
		return false
	}
	return !anyMatch(r.Exclude, name) || anyMatch(r.Include, name)
}

func reserved(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == "presto.toml" || part == "presto.local.toml" || part == ".presto-request.json" || part == ".presto-locks" {
			return true
		}
	}
	return false
}
