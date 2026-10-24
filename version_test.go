package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	module := func(value string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: value}}
	}
	for _, tt := range []struct {
		name     string
		injected string
		info     *debug.BuildInfo
		want     string
	}{
		{"release override", "1.2.3", module("v1.2.2"), "1.2.3"},
		{"override without metadata", "1.2.3", nil, "1.2.3"},
		{"installed tag", "dev", module("v1.2.3"), "1.2.3"},
		{"installed prerelease", "dev", module("v1.2.3-rc.1"), "1.2.3-rc.1"},
		{"installed revision", "dev", module("v0.0.0-20260101000000-0123456789ab"), "0.0.0-20260101000000-0123456789ab"},
		{"version metadata", "dev", module("v1.2.3+dirty"), "1.2.3+dirty"},
		{"empty injection", "", module("v1.2.3"), "1.2.3"},
		{"local development", "dev", module("(devel)"), "dev"},
		{"missing module version", "dev", module(""), "dev"},
		{"missing build metadata", "dev", nil, "dev"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.injected, tt.info); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
