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

// The binary must always be able to identify itself, whatever the
// toolchain did or didn't stamp.
func TestCurrentVersionStringNonEmpty(t *testing.T) {
	if got := currentVersionString(); got == "" {
		t.Error("currentVersionString() is empty")
	}
}
