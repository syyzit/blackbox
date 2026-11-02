package main

import (
	"runtime/debug"
	"strings"
)

// version is set at release time with -ldflags "-X main.version=1.2.3".
var version string

// versionString reports the release version, or the version Go records at build
// time: the tag for `go install ...@v1.2.3`, a pseudo-version for a local checkout,
// or the VCS revision when the module version is unavailable.
func versionString() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
		var revision, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if revision != "" {
			if len(revision) > 12 {
				revision = revision[:12]
			}
			if modified == "true" {
				revision += "-dirty"
			}
			return revision
		}
	}
	return "dev"
}
