// Command dirdiff is a read-only, interactive TUI for comparing two
// directory trees. See SPEC.md at the repo root for the full design.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/m42cel/dirdiff/internal/ui"
)

func main() {
	compareLevel := flag.String("compare-level", "", "initial comparison level to auto-apply: size|size-mtime|checksum")
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "worker pool size for listing and comparison")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <left-dir> <right-dir>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(1)
	}
	leftDir, rightDir := flag.Arg(0), flag.Arg(1)

	if err := validateCompareLevel(*compareLevel); err != nil {
		fmt.Fprintln(os.Stderr, "dirdiff:", err)
		os.Exit(1)
	}
	if *workers < 1 {
		fmt.Fprintln(os.Stderr, "dirdiff: --workers must be >= 1")
		os.Exit(1)
	}
	if err := validateDir(leftDir); err != nil {
		fmt.Fprintln(os.Stderr, "dirdiff:", err)
		os.Exit(1)
	}
	if err := validateDir(rightDir); err != nil {
		fmt.Fprintln(os.Stderr, "dirdiff:", err)
		os.Exit(1)
	}

	m := ui.New(leftDir, rightDir)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "dirdiff:", err)
		os.Exit(1)
	}
}

func validateCompareLevel(level string) error {
	switch level {
	case "", "size", "size-mtime", "checksum":
		return nil
	default:
		return fmt.Errorf("invalid --compare-level %q (want size|size-mtime|checksum)", level)
	}
}

func validateDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", path)
	}
	return nil
}
