package main

import (
	"runtime/debug"
	"strings"
)

// version is set at release time with -ldflags "-X main.version=1.2.3".
var version string

// versionString reports the release version, or the module version Go records at
// build time: the tag for `go install ...@v1.2.3`, or a pseudo-version with the
// commit (and +dirty) for a local build from a git checkout.
func versionString() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}
