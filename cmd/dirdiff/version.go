package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// devVersion is the placeholder for a build nobody stamped a release
// onto.
const devVersion = "dev"

// version identifies the release. Release builds inject the tag with
// -ldflags "-X main.version=v1.2.3"; anything built without that — a
// local `go build`, `go run` — keeps the placeholder and relies on the
// VCS details below to say which commit it actually is.
var version = devVersion

// shortRevLen is the git abbreviation length the Go toolchain itself uses
// in module pseudo-versions.
const shortRevLen = 12

// buildInfo reports the module version recorded for the main module, the
// commit the binary was built from, and whether the working tree was
// dirty at the time. The latter two come from the toolchain's VCS
// stamping, which is absent when building outside a repository or with
// -buildvcs=false, so an empty revision is a normal result.
func buildInfo() (mainVersion, rev string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return info.Main.Version, rev, modified
}

// resolveVersion picks the version to report. An injected version always
// wins. Otherwise the recorded module version is used only when the
// toolchain stamped no revision, which is what a module-proxy build looks
// like (`go install <module>@v1.0.0`) — there Main.Version is the only
// identity the binary has. A build from a checkout does stamp a revision
// and keeps the placeholder, because the version Go derives there is a
// pseudo-version that would read like a release.
func resolveVersion(injected, mainVersion, rev string) string {
	if injected != devVersion || rev != "" {
		return injected
	}
	if mainVersion == "" || mainVersion == "(devel)" {
		return injected
	}
	return mainVersion
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
	mainVersion, rev, modified := buildInfo()
	return versionString(
		resolveVersion(version, mainVersion, rev),
		rev, modified,
		runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH,
	)
}
