package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	mod := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	for _, tc := range []struct {
		ldflags string
		info    *debug.BuildInfo
		want    string
	}{
		{"1.2.3", mod("v0.1.0"), "1.2.3"},
		{"", mod("v0.1.0"), "0.1.0"},
		{"", mod("(devel)"), "dev"},
		{"", nil, "dev"},
	} {
		if got := resolveVersion(tc.ldflags, tc.info); got != tc.want {
			t.Errorf("resolveVersion(%q, %v) = %q, want %q", tc.ldflags, tc.info, got, tc.want)
		}
	}
}
