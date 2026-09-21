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

Seven packages, layered bottom-up; each only depends on the ones below it:

- **`internal/diffmodel`** — shared vocabulary, no I/O, no dependencies:
  `EntryType` (File/Dir/Symlink — matched independently per spec §3.1, so
  a file and a directory with the same name never merge into one row),
  `Side` (Left/Right — listing and metadata are per-side work),
  `Presence`, `CompareLevel`/`CompareResult` (ordered shallowest-to-deepest
  so callers compare levels with plain `<`), `ListedEntry`, `StatInfo`,
  `EntryLess` (the single definition of the dirs-first-then-alphabetical
  order every tree stores entries in, spec §4.1), and
  `RowStatus`/`ClassifyRow` — the single definition of which status a
  row has for filtering (spec §4.7), used both by the UI's filter and by
  the per-subtree tallies in `pairtree`, so the two can't drift apart.
- **`internal/workqueue`** — generic, key-deduplicated priority queue
  (`Queue[T]`) backing both worker pools. Pop order is driven by
  tree-edge distance from a live focus path rather than fixed tiers
  (spec §8.3): descendants of the focus path always pop before anything
  else, and within each group, closer jobs pop first. `SetFocus` (one
  focus for the whole queue) and `SetFoci` (one per key namespace, for a
  queue whose keys span unconnected trees — the listing queue's `L/…` and
  `R/…`) change the focus and re-heapify (`heap.Init`, O(n)) to reorder
  already-queued jobs — cheap enough since it only runs on user
  navigation, not per-job. A key is only ever ranked against the focus
  sharing its own namespace: two side trees share no ancestor, so a
  "distance" across them is the sum of two depths, not a tree distance.
  `Upsert` merges a job already queued under the
  same key instead of duplicating it; `Pop`/`Done` track in-flight jobs
  so `IsPending` reports queued-or-running for the UI's pending glyph.
- **`internal/scan`** — pure filesystem I/O (`DoList`, `DoStat`,
  `DoCompare`). Every function takes absolute paths and returns a result;
  nothing here touches shared state, so it's safe to call concurrently
  from workers. `DoList` and `DoStat` read **one** side — matching the
  two sides happens a layer up, so a directory paired with one at an
  entirely different path needs no listing of its own, and a metadata
  verdict is an in-memory equality test over two `DoStat` results rather
  than a job. `DoCompare` therefore only ever runs the content level, and
  opens nothing when the two sizes already differ. Symlinks are never
  followed: `DoStat` reads the `readlink` target and the two targets are
  compared as strings (spec §7), so a symlink never reaches `DoCompare`.
- **`internal/sidetree`** — one plain `Node` tree per side, one node per
  real filesystem entry: names and types from listing, size/mtime/link
  target from stat results, and nothing about the other side. A side node
  is
  shared by every pairing that covers it, which is what makes listing
  never repeat per pairing. Its per-subtree `Totals` (counts + size, spec
  §4.2) are kept incrementally via one upward walk per mutation, never a
  subtree walk at render time; `ownContribution` is the single definition
  of what one node counts for, so the oracle test in `sidetree_test.go`
  has exactly one place to disagree with. `Tree.Index` is a plain RelPath
  map with no dirs-only mirror: within one real directory a name
  identifies exactly one entry, so the file/directory collision that
  needed one only ever existed because a merged tree overlaid two
  filesystems.
- **`internal/pairtree`** — the merged `Node` tree of one *pairing* (two
  directories matched entry by entry) and the rollup logic (spec §3.3).
  A node holds `Left`/`Right *sidetree.Node` rather than a name or
  metadata of its own; `Presence`/`Name`/`Listed`/`SideTotals` are
  derived from those. `Merge`, `ApplyMetadata` and `ApplyCompareResult`
  are the only mutators: `Merge` extends a directory's children from the
  union of its
  two sides by `(name, type)` — **gated on every side it has being
  listed**, so a row's `Presence` is fixed at creation and never mutates
  — `ApplyMetadata` records the metadata verdict once both sides of a row
  have been statted (in memory, no job — it's an equality test, and gives
  the same answer in every pairing), and `ApplyCompareResult` is a no-op
  if the incoming level isn't deeper than what's already known (spec §5.3
  monotonicity). Metadata itself is deliberately *not* stored here: a
  size is a fact about one side's file, so it lives in `sidetree` and is
  shared with every other pairing over the same files. **Nodes are mutated exclusively from the UI's Update
  loop** (a single goroutine) — nothing in this package takes a lock.
  Each node carries `descMatches`, a per-`RowStatus` tally of its
  descendants that answers the filter's "is there a matching row below?"
  in O(1) instead of a subtree walk per render; `AddChild` is therefore
  the only supported way to link a node into the tree (assigning
  `Children` directly leaves the tallies stale), and `ApplyCompareResult`
  is the only thing that moves a node between statuses afterwards. The
  recursive walk lives on as the oracle in `descendants_test.go` — any
  new mutator must keep the two in agreement.
- **`internal/session`** — owns the two side trees and the pairing over
  them, orchestrates the two worker pools, and decides what to enqueue
  and when (`Navigate` sets both side foci on the listing queue and the
  pairing focus on the compare queue, `TriggerCompare`/`armRecursive` for
  opt-in comparison, spec §5.2/§5.4). `OnListResult` is a two-step
  router: apply the listing to the side tree it came from and enqueue
  that side's new subdirectories, then fan out to the pairing, where
  `Merge` turns whatever now has a counterpart into rows. `merge`
  descends into a new directory row whose two sides are *already* listed
  — a row created after its subtree was scanned has no listing result
  left to arrive. `OnStatResult` is the same two-step router for
  metadata, and is where the content level's size precheck lands:
  `examine` only ever creates a content job once both sides are statted
  *and* their sizes match — a mismatch is already a conclusive content
  verdict, recorded in memory — so on a divergent tree most content jobs
  are never created at all. A row whose metadata isn't in yet records
  what it was asked for in `ArmedLevel` and is re-examined when its stats
  land, the same way `PendingRecursiveLevel` is re-checked on a listing.
  The two pools divide **ambient discovery** (listing) from **triggered
  examination** (stat + content), so `x` cancels stat work too; the
  examination queue therefore holds keys from two namespaces at once
  (`L/…`/`R/…` for stats, `p/…` for this pairing's content jobs) and
  `CancelPendingCompares` routes each dropped key by its prefix.
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
  `View()` runs once per Bubble Tea message — every scan/compare result
  and every spinner tick — so anything it does per row is on a very hot
  path: keep it O(visible rows), never O(subtree). That's why the row
  filter reads `pairtree`'s per-status tallies and the details panel's
  directory totals read `sidetree`'s, instead of walking the subtree
  themselves. `atRootParent`
  is the one view with no `pairtree.Node` of its own (spec §4.3.1):
  `cursorDir` stays the root and `visibleChildren` synthesizes a single
  unfilterable row for it, so the two roots' whole-tree totals are
  selectable without inventing a parent node the scanner would then try
  to list and compare.
- **`cmd/dirdiff`** — flag parsing (`--level`, `--scan-workers`,
  `--compare-workers`, `--version` — handled before the positional-argument
  check so it works on its own, and the release version comes from
  `-ldflags "-X main.version=…"`, see `version.go`), startup path
  validation (hard error to stderr, exit 1, before the TUI starts — spec
  §2.2), wires up `session.New` + `ui.New` + `tea.Program`.

Entry matching (spec §3.1) is exact byte-for-byte name comparison — no
case-insensitive or Unicode-normalized matching, and by `(name, type)`,
so a file and a directory of the same name are two unrelated rows.
Directories sort before files, then alphabetically (spec §4.1), via the
one `diffmodel.EntryLess` every layer that stores entries calls.

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
  I/O). `scripts/pty-smoke.py` drives the real binary through a pty
  (answering the terminal capability queries Bubble Tea blocks on before
  its first paint) and prints what each keypress painted — worth running
  for any change to key handling or rendering, since that's exactly how
  the multi-rune key bug above was found.

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

## Keeping README.md in sync

`README.md` is user-facing documentation, not derived from the code, so
it drifts silently. Whenever a change touches anything it describes —
flags, keybindings, status glyphs/colors, default settings — update the
corresponding section of `README.md` in the same change. This has
already gone stale once: the status-glyph table listed the one-sided
arrows backwards (`→`/`←` swapped relative to what `statusGlyph` in
`internal/ui/view.go` actually renders) and still showed a retired
"not yet compared" glyph (`·` instead of the current `?`).
