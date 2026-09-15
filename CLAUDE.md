# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`dirdiff` is a read-only, interactive terminal UI for comparing two
directory trees side by side, built in Go with Bubble Tea. The full design
is in `SPEC.md` at the repo root — read it before making architectural
changes; it's the source of truth for behavior, not this file.

The tool must never delete, copy, move, or write to either compared tree.
Any change that adds filesystem mutation is out of scope by design.

## Commands

```sh
go build ./...              # build everything
go build -o dirdiff ./cmd/dirdiff   # build the binary (gitignored, don't commit it)
go run ./cmd/dirdiff <left-dir> <right-dir>   # run without building

go vet ./...
gofmt -l .                  # list files needing formatting
gofmt -w .                  # apply formatting

go test ./...                        # run all tests
go test ./internal/diffmodel/...     # run one package's tests
go test ./internal/diffmodel/ -run TestName   # run a single test

go mod tidy                 # after adding/removing an import — go get alone
                             # leaves new deps marked `// indirect` until tidy
                             # reconciles direct vs transitive requirements
```

## Current state vs. spec

As of now, only a scaffold exists to prove out the toolchain: `cmd/dirdiff`
does real flag parsing and startup validation, but `internal/ui` just lists
the top level of both directories synchronously and renders a static view.
None of the following from `SPEC.md` are implemented yet:

- Background BFS listing scan, the two separate worker pools (listing vs.
  checksum), and the priority queue with navigation-driven reprioritization
  (§8)
- The three opt-in comparison levels (size, size+mtime, checksum) and their
  per-directory / recursive triggers (§5)
- Directory status rollup from descendants (§3.3)
- The details panel, help overlay, one-sided-navigation placeholder, and
  "jump to next difference" (§4, §9)

When implementing these, keep the architecture split the spec implies: a
scanning/comparison layer that runs independently of the UI and reports
results via messages, not a UI that blocks on I/O.

## Architecture

- **`cmd/dirdiff`** — thin entrypoint. Parses `--compare-level` and
  `--workers`, validates both root paths exist and are directories
  (hard error to stderr, exit 1, *before* the TUI starts — see spec §2.2),
  then launches the Bubble Tea program.
- **`internal/diffmodel`** — shared vocabulary with no dependencies on UI
  or I/O: `EntryType` (File/Dir/Symlink — matched independently per spec
  §3.1, so a file and a directory with the same name never merge into one
  row), `Presence` (Both/LeftOnly/RightOnly), `CompareLevel` and
  `CompareResult`. Scanning, comparison, and UI code should all build on
  these types rather than inventing parallel ones.
- **`internal/ui`** — the Bubble Tea model (Init/Update/View). Will own
  the two-pane rendering, cursor/navigation state, and (once built) the
  glue to the background scan/compare layer via `tea.Cmd`/`tea.Msg`.
  Status glyphs always pair a distinct glyph with a distinct color (spec
  §6) — never rely on color alone when adding new statuses.

Entry matching (spec §3.1) is exact byte-for-byte name comparison — no
case-insensitive or Unicode-normalized matching — and directories sort
before files, then alphabetically (spec §4.1 / current `mergeListing` in
`internal/ui/ui.go`). Keep new sorting/matching logic consistent with this
rather than introducing a second convention.

## Platform target

macOS and Linux only (spec §11) — no Windows path/ACL special-casing.
