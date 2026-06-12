// Package version exposes build metadata injected at link time via -ldflags.
// It lets the running program report exactly which build it is, so a stale
// binary is immediately obvious.
package version

import (
	"fmt"
	"runtime/debug"
)

// These are overridden at build time:
//
//	go build -ldflags "-X github.com/datageek/agentdash/internal/version.Version=v0.1.0 \
//	  -X github.com/datageek/agentdash/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/datageek/agentdash/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Short returns a compact identifier like "v0.1.0 (a1b2c3d)" or, for an
// un-stamped `go run`/`go build` without ldflags, falls back to the module's
// VCS build info so the value is never just "dev".
func Short() string {
	v, c := Version, Commit
	if v == "dev" || c == "none" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 && c == "none" {
					c = s.Value[:7]
				}
			}
		}
	}
	return fmt.Sprintf("%s (%s)", v, c)
}

// Full returns version, commit, and build date for `agentdash version`.
func Full() string {
	return fmt.Sprintf("agentdash %s\n  commit: %s\n  built:  %s", Version, Commit, Date)
}
