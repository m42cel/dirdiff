# dirdiff

A fast, read-only, interactive terminal UI for comparing two directory
trees side by side.

`dirdiff` never deletes, copies, moves, or writes anything on either side
— it's purely for exploring differences. It shows the first level of both
directories immediately, then scans deeper directories breadth-first in
the background without blocking the UI. Navigating into a directory
reprioritizes the background scan toward what you're looking at.
Comparing file contents is opt-in and explicit — you choose how thorough
a comparison to run (metadata, i.e. size + mtime, or a full byte-for-byte
content comparison), and where (just the current directory, or
recursively).

See [SPEC.md](SPEC.md) for the full design and rationale.

## Requirements

- Go 1.27 or newer (see `go.mod`)
- macOS or Linux (not tested on Windows)

## Building

From the repository root:

```sh
go build -o dirdiff ./cmd/dirdiff
```

This produces a `dirdiff` binary in the current directory. You can also
run it directly without a separate build step:

```sh
go run ./cmd/dirdiff <left-dir> <right-dir>
```

## Usage

```sh
dirdiff [flags] <left-dir> <right-dir>
```

### Flags

| Flag | Description |
|---|---|
| `--level=<level>` | Auto-apply a comparison level (`metadata`, `content`, or `none`) to the whole tree recursively in the background as it's discovered, instead of comparing manually. Default: `metadata`. |
| `--workers=<n>` | Size of the listing and comparison worker pools (default: number of CPUs). |

### Keybindings

| Key | Action |
|---|---|
| `↑` / `↓` | Move cursor |
| `PgUp` / `PgDn` | Move by page |
| `Home` / `End` | Jump to first / last entry |
| `→` / `Enter` | Open the directory under the cursor (both panes navigate together) |
| `←` / `Backspace` | Go up to the parent directory |
| `l` | Switch the compare level — metadata (size + mtime) ↔ content (byte-for-byte) — remembered until changed again |
| `r` | Toggle recursive mode on/off — remembered, default on |
| `c` | Compare the current directory's files at the current level/recursive setting |
| `n` / `N` | Jump to the next / previous difference in the current directory |
| `x` | Cancel all pending (not yet started) comparisons |
| `?` | Toggle the help overlay |
| `q` / `Ctrl+C` | Quit |

A directory that exists on only one side is still navigable — the
missing side shows a static placeholder. Existence is shown as soon as a
directory is listed; metadata/content comparisons only run once you
trigger them with `c`.

### Status glyphs

Every status pairs a distinct glyph with a distinct color, so it's
legible without relying on color:

| Glyph | Meaning |
|---|---|
| `=` | Same (at the deepest level compared so far) |
| `≠` | Differs |
| `←` | Only exists on the left |
| `→` | Only exists on the right |
| `!` | Error (e.g. permission denied) |
| `.` / `..` / `...` | Pending — a file, or a directory's own row, queued or currently being compared (animated) |
| `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` | Directory rollup — a comparison is still pending somewhere in this subtree (animated Braille spinner) |
| `?` | Not yet compared |

Directories additionally roll up the worst status found anywhere in
their subtree, so you can spot which subtrees contain differences
without opening every one; the Braille spinner above takes priority
over a rollup result while work is still outstanding underneath, so a
subtree in progress is never mistaken for "clean so far". Listing
progress is shown separately, per side, next to a directory's name
(animated `.` / `..` / `...`) rather than in the shared glyph column.

## Development

```sh
go build ./...      # build everything
go vet ./...
gofmt -l .           # list files needing formatting
go test ./...         # run tests
go test ./... -race   # run tests with the race detector (recommended for
                       # internal/workqueue and internal/session, which
                       # are concurrency-sensitive)
```

See [CLAUDE.md](CLAUDE.md) for a tour of the package architecture.
