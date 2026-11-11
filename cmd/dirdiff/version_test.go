package main

import "testing"

func TestVersionString(t *testing.T) {
	const rev = "b1946ac92492d2347c6235b4d2611184"

	tests := []struct {
		name     string
		version  string
		rev      string
		modified bool
		want     string
	}{
		{
			name:    "release build",
			version: "v1.0.0",
			rev:     rev,
			want:    "dirdiff v1.0.0 (b1946ac92492, go1.27.1, darwin/arm64)",
		},
		{
			name:     "dirty local build",
			version:  "dev",
			rev:      rev,
			modified: true,
			want:     "dirdiff dev (b1946ac92492-dirty, go1.27.1, darwin/arm64)",
		},
		{
			name:    "no vcs stamping",
			version: "dev",
			want:    "dirdiff dev (go1.27.1, darwin/arm64)",
		},
		{
			name:    "revision shorter than the abbreviation length",
			version: "v1.0.0",
			rev:     "b1946",
			want:    "dirdiff v1.0.0 (b1946, go1.27.1, darwin/arm64)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionString(tt.version, tt.rev, tt.modified, "go1.27.1", "darwin/arm64")
			if got != tt.want {
				t.Errorf("versionString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveVersion(t *testing.T) {
	const (
		rev   = "b1946ac92492d2347c6235b4d2611184"
		pseud = "v0.0.0-20260920104418-2bec4bd714d2"
	)

	tests := []struct {
		name        string
		injected    string
		mainVersion string
		rev         string
		want        string
	}{
		{
			name:        "injected release wins over everything",
			injected:    "v1.0.0",
			mainVersion: pseud,
			rev:         rev,
			want:        "v1.0.0",
		},
		{
			name:        "module proxy install has no revision to fall back on",
			injected:    devVersion,
			mainVersion: "v1.0.0",
			want:        "v1.0.0",
		},
		{
			name:        "build from a checkout keeps dev, not its pseudo-version",
			injected:    devVersion,
			mainVersion: pseud,
			rev:         rev,
			want:        devVersion,
		},
		{
			name:        "source copy without a repository",
			injected:    devVersion,
			mainVersion: "(devel)",
			want:        devVersion,
		},
		{
			name:     "no build info at all",
			injected: devVersion,
			want:     devVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.injected, tt.mainVersion, tt.rev); got != tt.want {
				t.Errorf("resolveVersion(%q, %q, %q) = %q, want %q",
					tt.injected, tt.mainVersion, tt.rev, got, tt.want)
			}
		})
	}
}

// The binary must always be able to identify itself, whatever the
// toolchain did or didn't stamp.
func TestCurrentVersionStringNonEmpty(t *testing.T) {
	if got := currentVersionString(); got == "" {
		t.Error("currentVersionString() is empty")
	}
}
