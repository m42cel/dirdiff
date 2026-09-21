// Command dirdiff is a read-only, interactive TUI for comparing two
// directory trees. See SPEC.md at the repo root for the full design.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/session"
	"github.com/m42cel/dirdiff/internal/ui"
)

func main() {
	levelFlag := flag.String("level", "metadata", "initial comparison level to auto-apply recursively in the background: metadata|content|none (none = existence/listing only)")
	// Two by default, one per side: a listing job reads one side's
	// directory, so a single worker would read the two sides one after
	// the other and a directory's listing would take as long as both
	// reads instead of the slower one. Beyond that, listing is cheap
	// low-CPU I/O that doesn't benefit from scaling with core count.
	scanWorkers := flag.Int("scan-workers", 2, "worker pool size for directory listing")
	compareWorkers := flag.Int("compare-workers", runtime.GOMAXPROCS(0), "worker pool size for comparison")
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <left-dir> <right-dir>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	// Answered before the argument check: --version is a question about
	// the binary, not a comparison, so it needs no directories.
	if *showVersion {
		fmt.Println(currentVersionString())
		return
	}

	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(1)
	}
	// Clean strips any trailing slash (and collapses redundant ones), so
	// a root like "foo/" doesn't show a trailing slash in the UI's path
	// header and doesn't produce "foo//sub" once RelPath is appended.
	leftDir, rightDir := filepath.Clean(flag.Arg(0)), filepath.Clean(flag.Arg(1))

	autoLevel, err := parseLevel(*levelFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dirdiff:", err)
		os.Exit(1)
	}
	if *scanWorkers < 1 {
		fmt.Fprintln(os.Stderr, "dirdiff: --scan-workers must be >= 1")
		os.Exit(1)
	}
	if *compareWorkers < 1 {
		fmt.Fprintln(os.Stderr, "dirdiff: --compare-workers must be >= 1")
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

	sess := session.New(leftDir, rightDir, *scanWorkers, *compareWorkers, autoLevel)
	defer sess.Close()

	m := ui.New(sess)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "dirdiff:", err)
		os.Exit(1)
	}
}

func parseLevel(level string) (diffmodel.CompareLevel, error) {
	switch level {
	case "none":
		return diffmodel.NotCompared, nil
	case "metadata":
		return diffmodel.SizeMtime, nil
	case "content":
		return diffmodel.Checksum, nil
	default:
		return 0, fmt.Errorf("invalid --level %q (want metadata|content|none)", level)
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
