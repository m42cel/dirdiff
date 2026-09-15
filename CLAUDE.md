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

go test ./...                              # run all tests
go test ./internal/session/...             # run one package's tests
go test ./internal/session/ -run TestName  # run a single test
go test ./... -race                        # concurrency-sensitive; run with -race after
                                            # touching workqueue or session

go mod tidy                 # after adding/removing an import — go get alone
                             # leaves new deps marked `// indirect` until tidy
                             # reconciles direct vs transitive requirements
```

## Architecture

Six packages, layered bottom-up; each only depends on the ones below it:

- **`internal/diffmodel`** — shared vocabulary, no I/O, no dependencies:
  `EntryType` (File/Dir/Symlink — matched independently per spec §3.1, so
  a file and a directory with the same name never merge into one row),
  `Presence`, `CompareLevel`/`CompareResult` (ordered shallowest-to-deepest
  so callers compare levels with plain `<`), `ListedChild`, `StatInfo`.
- **`internal/workqueue`** — generic, key-deduplicated priority queue
  (`Queue[T]`) backing both worker pools. Three priority tiers (`High`/
  `Medium`/`Low`) are reused for both listing and comparison (spec §8.3).
  `Upsert` merges a job already queued under the same key instead of
  duplicating it; `Boost` raises priority on an already-queued job (no-op
  if it already started); `Pop`/`Done` track in-flight jobs so
  `IsPending` reports queued-or-running for the UI's pending glyph.
- **`internal/scan`** — pure filesystem I/O (`DoList`, `DoCompare`).
  Every function takes absolute paths and returns a result; nothing here
  touches shared state, so it's safe to call concurrently from workers.
  Symlinks are never followed — any compare level just compares the two
  `readlink` targets as strings (spec §7).
- **`internal/tree`** — the mutable `Node` tree (one node per matched/
  unmatched entry) and the rollup logic (spec §3.3). `ApplyListing` and
  `ApplyCompareResult` are the only mutators; `ApplyCompareResult` is a
  no-op on Level/Result if the incoming level isn't deeper than what's
  already known (spec §5.3 monotonicity), though stat metadata is always
  refreshed. **Nodes are mutated exclusively from the UI's Update loop**
  (a single goroutine) — nothing in this package takes a lock.
- **`internal/session`** — orchestrates the two worker pools and decides
  what to enqueue and at what priority (`Navigate` for reprioritization,
  `TriggerCompare`/`armRecursive` for opt-in comparison, spec §5.2/§5.4).
  Framework-agnostic on purpose: it exposes plain channels
  (`ListResults()`/`CompareResults()`), not `tea.Cmd`. A recursive
  compare trigger arms *both* the target directory and its children
  (`PendingRecursiveLevel`) so the intent survives even if the directory
  itself hasn't finished listing yet — `OnListResult` re-checks this flag
  every time new children appear, which is what makes a recursive compare
  reach files discovered after the trigger.
- **`internal/ui`** — the Bubble Tea model. `model.go` holds cursor/nav
  state and key handling; `view.go` renders it; `style.go` has the
  lipgloss styles. `handleKey` explicitly re-splits a `tea.KeyMsg` with
  multiple `Runes` into separate keypresses before dispatching — Bubble
  Tea can legitimately deliver quickly-typed keys (e.g. an `l` then `c`
  sequence) batched into one event, and this was an actual bug caught by
  an interactive pty smoke test, not a theoretical one. Status glyphs
  always pair a distinct glyph with a distinct color (spec §6) — never
  rely on color alone for a new status. The compare level (`l`) and
  recursive toggle (`r`) are persistent settings, not one-shot flags —
  `c` runs whatever is currently selected and doesn't reset either one.
- **`cmd/dirdiff`** — flag parsing (`--level`, `--workers`), startup path
  validation (hard error to stderr, exit 1, before the TUI starts — spec
  §2.2), wires up `session.New` + `ui.New` + `tea.Program`.

Entry matching (spec §3.1) is exact byte-for-byte name comparison — no
case-insensitive or Unicode-normalized matching — and directories sort
before files, then alphabetically (spec §4.1), consistently in both
`scan.DoList` and `tree.ApplyListing`.

## Testing notes

- `internal/workqueue` and `internal/session` tests are concurrency-
  sensitive (real goroutines, real channels) — always run them with
  `-race` after changes, not just `go test`.
- `internal/session`'s tests drive the session the same way the UI does:
  a `pump` helper loops receiving from `ListResults()`/`CompareResults()`
  and calling `OnListResult`/`OnCompareResult`, simulating the Update
  loop being the sole tree mutator. Write new session tests the same way
  rather than reaching into tree state directly.
- There's no automated test for the Bubble Tea layer itself (raw terminal
  I/O). It was manually smoke-tested by driving the real binary through a
  pty (Python's `pty` module, responding to Bubble Tea's terminal
  capability queries) — worth doing again for any change to key handling
  or rendering, since that's exactly how the multi-rune key bug above was
  found.

## Code comments

Comments should explain non-obvious *current* behavior — a hidden
invariant, a platform quirk, why a magic number is what it is — not the
history of how the code got there. Don't write comments like "this used
to be X", "previously did Y", "fixes a bug where Z", or "instead of the
old approach"; that narration belongs in the commit message, not the
source. A comment should still make sense to someone who never saw the
prior version of the code; if it only makes sense as a diff against an
earlier version, cut it.

## Platform target

macOS and Linux only (spec §11) — no Windows path/ACL special-casing.
