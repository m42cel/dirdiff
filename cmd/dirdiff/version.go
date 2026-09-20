package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version identifies the release. Release builds inject the tag with
// -ldflags "-X main.version=v1.2.3"; anything built without that — a
// local `go build`, `go run` — keeps the placeholder and relies on the
// VCS details below to say which commit it actually is.
var version = "dev"

// shortRevLen is the git abbreviation length the Go toolchain itself uses
// in module pseudo-versions.
const shortRevLen = 12

// buildRevision reports the commit the binary was built from and whether
// the working tree was dirty at the time. Both come from the toolchain's
// VCS stamping, which is absent when building outside a repository or
// with -buildvcs=false, so an empty revision is a normal result.
func buildRevision() (rev string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return rev, modified
}

func versionString(version, rev string, modified bool, goVersion, platform string) string {
	var detail []string
	if rev != "" {
		if len(rev) > shortRevLen {
			rev = rev[:shortRevLen]
		}
		if modified {
			rev += "-dirty"
		}
		detail = append(detail, rev)
	}
	detail = append(detail, goVersion, platform)
	return "dirdiff " + version + " (" + strings.Join(detail, ", ") + ")"
}

// currentVersionString is versionString for the running binary.
func currentVersionString() string {
	rev, modified := buildRevision()
	return versionString(version, rev, modified, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
}
