# Plan — independent side trees and sub-compares

Status: proposal. Not yet reflected in `SPEC.md`; §-references below point at
`SPEC.md` as it stands today.

## 1. What we want

Let the user pick a directory in the left tree and a directory in the right
tree — at *different* paths — and open a **sub-compare**: a second comparison
view pairing those two subtrees, rendered exactly like the main view.

The motivating case: a directory was moved or renamed on one side, so today it
shows as `left-only` at the old path and `right-only` at the new one, and
there's no way to ask "are these two actually the same?".

Three things fall out of the restructuring this needs, and they're worth as
much as the feature itself:

1. **Sub-compares cost no I/O** over subtrees that have already been scanned
   and compared — the pairing is a pure in-memory merge.
2. **Directory totals cover both sides.** Today size is only ever what a
   comparison happened to read (§4.2), and a one-sided entry is never
   compared, so a left-only subtree reports `size ?` forever. Metadata is now
   collected per side for *every* entry in a compared scope, one-sided ones
   included.
3. **The metadata level becomes a per-directory, per-side stat job** instead
   of a per-file, two-sided compare job — the same syscalls, a small fraction
   of the jobs, and wider coverage (§7.1). Comparing two statted files at the
   metadata level is then an in-memory equality test, identical in any
   pairing.

Listing itself stays `readdir`-only, and `--level` keeps its three current
meanings. See §7.1 for why metadata collection stays triggered rather than
being folded into the listing pass.

## 2. Why the current model can't do it

Three properties of the current design block it, all of them load-bearing
elsewhere:

- **Listing is inherently two-sided.** `scan.DoList` takes `LeftAbs` and
  `RightAbs`, reads both, and returns one merged `[]ListedChild`. A pairing of
  `left/old-name` with `right/new-name` has no single `RelPath` to list.
- **Everything is keyed by one `RelPath`.** The work queues
  (`workqueue.Queue` keys and its focus/distance math), `session.nodeIndex`,
  `session.dirIndex`, and `tree.Node.RelPath` all assume one path names the
  same row on both sides. Under a sub-compare, one row has two different
  paths.
- **Metadata only exists where a comparison ran.** `tree.ownContribution`
  gates size on `HaveStat`, which `ApplyCompareResult` is the only setter of.
  A sub-compare of two subtrees that were never compared would know nothing
  about them but their names.

## 3. Target architecture

```
                    ┌──────────────────────────────────────────┐
  scan.ListJob      │ internal/sidetree                        │
  {Side, RelPath}   │   Tree{Root *Node; Index map[string]*Node}│
        │           │   Node{Name,Type,Size,Mtime,LinkTarget,   │
        ▼           │        Children,Listed,ListErr,           │
  scan.DoList ──────▶        Totals, PendingListing}            │
  (one side,        │   two of these: Left, Right               │
   stats entries)   └──────────────┬───────────────────────────┘
                                   │  (a side node may be referenced
                                   │   by any number of pairings)
                    ┌──────────────▼───────────────────────────┐
  scan.CompareJob   │ internal/pairtree                        │
  {PairingID,       │   Pairing{ID, Root *Node,                │
   PairRel, L/R Abs}│            index, leftIdx, rightIdx}     │
        │           │   Node{Left,Right *sidetree.Node,        │
        ▼           │        Presence,Type,PairRel,            │
  scan.DoCompare ───▶        Level,Result,descMatches,         │
  (content only)    │        PendingCompare, PendingRecursive} │
                    └──────────────┬───────────────────────────┘
                                   │
                    ┌──────────────▼───────────────────────────┐
                    │ internal/session                         │
                    │   two side trees, N pairings,            │
                    │   listQ (side-namespaced keys),          │
                    │   cmpQ (pairing-namespaced keys)         │
                    └──────────────┬───────────────────────────┘
                                   │
                    ┌──────────────▼───────────────────────────┐
                    │ internal/ui — pairing stack, marks       │
                    └──────────────────────────────────────────┘
```

### 3.1 `internal/scan` — three job kinds on two axes

Listing reads names and types only; metadata is read by a separate,
*triggered* stat job (§7.1). The three kinds split cleanly into per-side work
and per-pairing work:

```go
// PER SIDE. readdir only — ~1 syscall per directory, as cheap as today.
type ListJob struct {
    Side    diffmodel.Side // Left | Right
    RelPath string         // relative to that side's root
    Abs     string
}
type ListedEntry struct {
    Name string
    Type diffmodel.EntryType
}

// PER SIDE, PER FILE. lstat one entry; readlink too, if it's a symlink.
// Directories are never statted: a directory contributes a count to the
// totals, not a size, and its own mtime is not compared.
type StatJob struct {
    Side    diffmodel.Side
    RelPath string
    Abs     string
    Type    diffmodel.EntryType
}
type StatResult struct {
    Side       diffmodel.Side
    RelPath    string
    Size       int64
    Mtime      time.Time
    LinkTarget string // symlinks only (§7)
    Err        error
}

// BOTH SIDES, ONE FILE. Today's CompareJob, addressed within a pairing.
type CompareJob struct {
    Pairing           PairingID
    PairRel           string
    LeftAbs, RightAbs string
    Type              diffmodel.EntryType
}
```

`DoList` no longer merges — merging by `(name, type)` moves into `pairtree`.
`compareSymlink` goes away entirely: a link target is read by the stat job and
compared in memory, so a symlink never reaches the compare pool.

`DoCompare` gains a **size precheck**: two files of different lengths cannot
have equal content, so it returns `Differs` without reading a byte. Today's
code streams both files even when the sizes differ, reading until the shorter
one ends. §3.5 hoists this check further still — to the point where the job
would be enqueued at all.

### 3.2 `internal/sidetree` — new package, one plain tree per side

One node per real filesystem entry on that side. No presence, no compare
result, no pairing knowledge — it is a cached `readdir`+`lstat` of one tree.

It keeps, incrementally (same `subtreeDelta`-style upward walk as today's
`tree`):

- `Totals{Dirs, Files, Symlinks, Size, SizedFiles}` per subtree — the same
  shape as today's `tree.SideTotals`, but kept per side and filled from stat
  results rather than compare results. §4.2's `≥1.4 MiB (12/40 files sized)`
  display therefore stays exactly as it is; the difference is that it now
  converges to exact for one-sided entries too, instead of being permanently
  short for them.
- `HaveStat`, `Size`, `Mtime`, `LinkTarget` per node, set when its stat
  result lands.
- `PendingListing` — one counter per node, not the left/right pair today's
  `tree.Node` carries. Each side's tree tracks its own listing work, which is
  exactly the per-pane semantics §3.3 already describes.

`Index map[string]*Node` replaces `session.nodeIndex`/`dirIndex` **and their
collision hack**: within one real filesystem directory a name identifies
exactly one entry, so a side tree's `RelPath` index needs no dirs-only mirror.
The file/directory collision only ever existed because the merged tree
overlaid two filesystems.

### 3.3 `internal/pairtree` — today's `internal/tree`, reshaped

A `Pairing` is (left side node, right side node) plus the merged tree over
them. The root pairing is `(Left.Root, Right.Root)`; a sub-compare is any
other pair of directory nodes.

```go
type Node struct {
    Left, Right *sidetree.Node // either may be nil → one-sided
    Type        diffmodel.EntryType
    PairRel     string // path relative to this pairing's roots
    Parent      *Node
    Children    []*Node

    Level  diffmodel.CompareLevel
    LevelMixed bool
    Result diffmodel.CompareResult
    Err    error

    PendingCompare        int
    PendingRecursiveLevel diffmodel.CompareLevel
    descMatches           [diffmodel.RowStatusCount]int32
}

func (n *Node) Presence() diffmodel.Presence // derived from Left/Right nil-ness
func (n *Node) Name() string                 // Left's or Right's
```

What survives unchanged in spirit: `ClassifyRow`, `descMatches` and the
`AddChild`-only invariant, `rollupResult`/`rollupLevel`/`worstResult`,
`resultRank`, level monotonicity, `PendingCompare` upward walks, and the
"mutated only from the Update loop, no locks" rule.

What moves out: `descLeft`/`descRight` and `SideTotals`. A pair node's
left-side totals are *exactly* `n.Left.Totals()` — per-side counts don't
depend on how the two sides were matched, so there is nothing to aggregate
per pairing. This deletes a whole aggregate and its oracle test from the
pairing layer and moves a simpler one into `sidetree`.

What's new:

- **`Merge(n *Node)`** — build/extend `n`'s children from the union of
  `n.Left.Children` and `n.Right.Children` by `(name, type)`.
- **In-memory metadata verdicts.** Once both sides of a file have been
  statted, its `SizeMtime` verdict is an equality test over values already in
  the side trees — no job, no I/O, and the same answer in every pairing. A
  symlink compares its two `LinkTarget`s the same way. Monotonicity (§5.3)
  still applies: a stored `Checksum` result outranks it.

**Merge gating.** `n`'s children are created only once every side `n`
actually has is `Listed`. The alternative — show the left side's entries
immediately as `left-only` and upgrade them to `both` when the right side
lists — would flash a screen of yellow `←` glyphs that then turn green, and
would introduce the first-ever mutation of a node's `Presence`, which
`descMatches` would have to learn to track. Gating keeps presence fixed at
creation (as today) and matches today's behavior exactly, since `DoList`
already waited for both sides. Listed as a follow-up in §9.

### 3.4 `internal/workqueue` — namespaced keys, multiple foci

Keys gain a namespace segment, chosen so the existing `/`-segment distance
math keeps working unchanged:

| Queue (pool) | Job | Key |
|---|---|---|
| scan | list | `"L/" + rel`, `"R/" + rel` (roots: `"L"`, `"R"`) |
| compare | stat | `"L/" + rel`, `"R/" + rel` |
| compare | content | `"<pairingID>/" + pairRel` (e.g. `"p0/sub/f.txt"`) |

Stat and list keys can share a spelling because they live in different
queues, and within the compare queue a stat key (`L/…`, `R/…`) can never
collide with a content key (`p0/…`). No escape character or suffix is needed.

Because a pairing's jobs share its id as their key prefix, closing one is a
prefix drop: `Queue.ClearPrefix(prefix)` — `Clear()`'s logic over a subset,
returning the dropped keys so the session can unwind their pending counts the
same way `CancelPendingCompares` already does.

**Stat jobs run on the compare pool, not the scan pool.** They are triggered
work — the product of `c`/`C` or of `--level`'s ambient arming — so `x` must
cancel them (§5.4), and they must not compete with the ambient listing that
keeps navigation responsive. This makes §8.2's two-pool split more principled
than it is today: it divides **ambient discovery** (readdir) from **triggered
examination** (stat + content), rather than merely "cheap" from "expensive".
It also means the compare queue holds keys from three namespaces at once;
`Clear()` hands them back mixed, and the session routes each by its prefix.

Two panes now stand in two different paths, so one focus string no longer
expresses "what the user is looking at". `SetFocus(string)` becomes:

```go
// One focus per namespace. A key is measured only against the focus
// sharing its first segment — never against any other.
func (q *Queue[T]) SetFoci(foci ...string)
```

**Each focus governs its own namespace, and only its own.** The keys in one
queue come from unconnected trees, so a "distance" between an `L/…` key and an
`R/…` focus is not a tree distance at all — with no shared prefix it
degenerates to `depth(focus) + depth(key)`, which can undercut a genuine
same-side distance and rank a right-side job by how deep the *left* pane
happens to be standing. A concrete case: with foci `L` and `R/very/deep/dir`,
the key `R/other` is a real distance 4 from the right focus but a spurious 3
from the left one, so a min-over-all-foci comparator would pick the meaningless
number.

So `Less` resolves the key's namespace from its first segment, then applies
today's `isUnder`/`distance` against that one focus. A namespace with no focus
— a suspended pairing's leftover jobs, or any namespace before the first
`Navigate` — is simply never "under", and falls back to depth within its own
tree, which is the breadth-first order §8.1 wants for the initial scan anyway.
Cross-namespace comparisons then never happen, and `SetFoci` still just
re-`heap.Init`s.

Compare keys are collision-free without a type suffix: a compare job only ever
targets a both-sided entry, and both-sided requires the same `(name, type)` on
both sides, so two both-sided pair nodes can't share a `PairRel`. The
pairing's compare index therefore indexes both-sided nodes only.

### 3.5 `internal/session` — two trees, N pairings, one router

```go
func (s *Session) OpenPairing(left, right *sidetree.Node) (PairingID, error)
func (s *Session) ClosePairing(id PairingID)
func (s *Session) Navigate(id PairingID, dir *pairtree.Node)
func (s *Session) TriggerCompare(id PairingID, dir *pairtree.Node, level diffmodel.CompareLevel, recursive bool)
```

`OnListResult` and `OnStatResult` share one two-step router — update the side
tree, then fan out to every pairing that contains the touched node:

```
1. sidetree.ApplyListing(...)   or   sidetree.ApplyStat(...)
      → children / metadata, plus totals and pending counts upward
2. (listing only) enqueue listing for each new child directory, that side only
3. for each open pairing p:
       node := p.leftIdx[sideNode] or p.rightIdx[sideNode]
       pairtree.Merge(node) → new children, in-memory metadata verdicts,
                              descMatches upward, rollup upward,
                              re-apply PendingRecursiveLevel
```

`p.leftIdx map[*sidetree.Node]*pairtree.Node` makes step 3 O(1) per pairing;
a side node appears in at most one pair node per pairing.

A stat result therefore fans out exactly the way a list result does — it fills
in metadata instead of children. The only new transition is a both-sided
file's `Result` moving `Unknown → Same|Differs` when its second side lands,
and `descMatches` already handles precisely that transition today via
`ApplyCompareResult`. No new mutation path.

**What a trigger enqueues.** A trigger has a *target* (this plan's §4.2) and,
for a directory target, a scope set by the `r` toggle — that directory's
children, or its whole subtree:

| Level | Enqueued |
|---|---|
| metadata | one stat job per file/symlink in scope, per side — including entries that exist on **one** side only |
| content | the same stat jobs, plus a content job per both-sided file whose two sides are known to be the same size |

Arming stat jobs at the content level too is not redundant: a one-sided entry
never gets a content job, so without it `--level=content` would leave exactly
the entries that make totals incomplete unstatted.

**The size precheck is hoisted to the enqueue decision.** A size mismatch is
already a conclusive content-level verdict, so a file whose two sides are
statted and differ in size never needs a content job at all — the verdict is
recorded in memory and the expensive job is never created. Only size-matching
files reach the compare pool. This falls out of machinery that already exists:
`PendingRecursiveLevel` is re-checked whenever new information arrives about a
directory, which now includes stat results as well as listings.
`DoCompare`'s in-job precheck (§3.1) remains the fallback for when a trigger
outruns its own stat jobs, or when a stat failed outright. On a tree with many
differing files, this removes the large majority of content jobs.

`OnCompareResult` routes by the job's `PairingID`, and drops results for a
pairing that has since been closed.

**Sub-compares nest.** `p` from inside a sub-compare pushes another one; `←`
past its root-parent row pops back to the one you came from, not to the root
pairing. Nothing extra is needed for this — marks point at `sidetree` nodes,
which are global, and pairing ids are a monotonic counter, so a pairing has no
idea whether it was opened from the root view or from three levels of
sub-compare down. Every pairing in the stack stays alive; only the one you
pop is freed.

**Pairings are transient.** A sub-compare lives only while you're in it:
popping the stack calls `ClosePairing`, which drops that pairing's tree, its
content verdicts, and its still-queued content jobs. In-flight jobs finish
(the same "let it finish" policy as §5.4) and their results are discarded by
the router. Only the stack is ever alive, so the root pairing plus whatever
you've descended into — in practice two.

Re-entering the same pair rebuilds it from scratch, but that's cheaper than it
sounds: **content verdicts are the only thing a pairing owns.** Listing and
stat results live in the side trees and are shared, so a rebuild is instant
for existence and metadata, and loses only bytes actually read — nothing at
all at the default `--level=metadata`.

**Listing is never duplicated across pairings.** Opening a sub-compare over a
not-yet-listed subtree enqueues nothing new — it re-focuses the side-listing
jobs that the ambient BFS was going to run anyway. Only content comparisons
are per-pairing, because a byte-for-byte verdict is a property of the *pair*,
not of either file.

### 3.6 `internal/ui` — a stack of pairings

`Model` grows a `[]pairingView` stack, each holding what's per-view today:
`pairingID`, `cursorDir`, `cursorIdx`, `scrollOffset`, `atRootParent`.
`compareLevel`, `recursive` and `filter` stay global settings (§4.7's "third
persistent setting" reasoning applies across pairings too).

## 4. The sub-compare UX — marks

Panes navigate in lockstep (§4.1), so there is no moment at which the user is
"standing in" two unrelated directories — which is exactly what picking a
pairing requires. The way out is to **mark one side at a time**, from wherever
you happen to be, and pair the two marks afterwards. Lockstep navigation is
never broken; there is only ever one cursor.

| Key | Action |
|---|---|
| `[` | Mark the left side of the row under the cursor as the left anchor |
| `]` | Mark the right side of the row under the cursor as the right anchor |
| `p` | Open a sub-compare of the two marks (pushes a pairing; works from inside a sub-compare too) |
| `←`/`Backspace` past a sub-compare's root-parent row | Pop back to whichever view you opened it from |

- Marks are on `sidetree` nodes, so they're independent of which pairing was
  on screen when they were made. Navigate to `left/old-name`, press `[`; go
  find `right/new-name`, press `]`; press `p`.
- `[` on a row that doesn't exist on the left is a no-op (with a status-bar
  note); same for `]`.
- Both marks must be directories in v1 (pairing two files is a one-row view
  of no real value).
- The status bar shows the pending marks; `p` clears them on success.
- The root-parent row (§4.3.1) is markable too, so a whole root can be one
  half of a pairing.
- A mark survives navigation and pairing changes — it's a pointer to a
  `sidetree` node, not to a row or a view.

### 4.1 Rendering a sub-compare

Almost nothing changes, because pair nodes already carry both sides:

- Pane titles show each side's real path — they simply diverge now.
- The pairing's root-parent row (§4.3.1) shows the two directory names side by
  side, which is precisely what that level was built for; `←` from there pops
  the stack instead of doing nothing.
- The details panel's directory totals read `n.Left.Totals()` /
  `n.Right.Totals()`, and lose the `(12/40 files sized)` qualifier. `≥` now
  means "this subtree is still being listed" (`PendingListing > 0`), not
  "some files haven't been compared".
- Status bar gains a breadcrumb when the stack is deeper than one, e.g.
  `[sub-compare: old-name ↔ new-name]`.

### 4.2 Compare targeting: `c` and `C`

`c` currently compares the whole current directory, and §5.2's third bullet —
"triggering a level on a single selected file row compares just that file" —
is **specified but not implemented**: `ui.triggerCompare` always passes
`m.cursorDir`, and `session.TriggerCompare` returns early on a non-directory.
The retarget implements it:

| Key | Target |
|---|---|
| `c` | the **selected row**: a file/symlink compares just itself; a directory compares its children, or its subtree when `r` is on |
| `C` | the **current directory** — today's `c` |

One session entry point serves both — `TriggerCompare(pairing, target, level,
recursive)`, where `c` passes the selected node and `C` passes `cursorDir`.
The only change inside is handling a file target instead of rejecting it.
At the above-the-roots level (§4.3.1) the selected row *is* the root, so `c`
there compares the root — which is what §4.3.1 already specifies.

### 4.3 Settled behaviour

**Settled: `--level` does not change.** `none` / `metadata` / `content` keep
their current meanings exactly. Metadata is read by triggered stat jobs, so
`none` still means "touch nothing beyond readdir", `metadata` still means
"stat the tree in the background", and `content` still means "and read the
bytes". The flag was always the knob for how much I/O to spend; it just
couldn't previously express "stat things that have no counterpart to be
compared against", which is the gap this closes.

**Settled: `C` ignores the filter.** It applies to the full current working
directory, every child, whether or not the active filter hides it. The filter
is a view concern; a recursive trigger reaches hidden descendants anyway.
§5.2's "the currently displayed directory's *visible* entries" wording is the
thing to fix — the code already behaves this way.

**Settled: pairings are freed on leaving.** A sub-compare is temporary — it
exists while you're in it and is dropped when you pop back (§3.5). No pairing
cache, no eviction policy, no re-entry identity to track, and memory bounded
by stack depth rather than by how many sub-compares you've opened today.

**Settled: `x` clears every pending comparison, in all live pairings** —
including queued stat jobs, which are now triggered compare-pool work. With
pairings freed on leaving, "all live pairings" means the stack, so this is
barely a scope choice any more; it stays the panic button it is today
(§5.4).

## 5. Phased implementation

Each phase ends green on `go build ./... && go vet ./... && go test ./... -race`
and leaves the tool fully usable. Phases 1–2 are the restructuring, 2b is an
independent keybinding change, and the sub-compare feature itself is phases
3–4.

### Phase 1 — one-sided listing + side trees (the big one)

**Pure refactor: no user-visible change at all.** Metadata still arrives from
two-sided compare jobs exactly as today; it just lands in the two side trees
(via the pair node's side pointers) instead of on a merged node. Landing this
on its own means the risky restructuring is reviewable against "behaves
identically", with no feature argument mixed in.

- `diffmodel`: add `Side` (`Left`/`Right`).
- `scan`: split `DoList` per side, names and types only; delete the merge.
- New `internal/sidetree`: node, `ApplyListing`, `ApplyStat`, incremental
  `Totals`, `PendingListing`, `Index`.
- `git mv internal/tree internal/pairtree`; rewrite `Node` to hold
  `Left/Right *sidetree.Node`; derive `Presence`/`Name`; delete
  `descLeft`/`descRight`/`SideTotals` (totals now read off `sidetree`); add
  `Merge` with both-sides-listed gating.
- `workqueue`: `SetFoci`, namespaced list keys.
- `session`: two side trees, the two-step router, listing enqueued per side.
- `ui`: totals read from `sidetree`; per-side listing spinner reads the side
  node; `IsListPending` takes a side.

Tests: `sidetree` totals oracle (port `tree/totals_test.go`); `pairtree` merge
by `(name,type)` including the file/dir collision case and the gating
(one-side-listed shows nothing yet); `descendants_test.go` oracle ported
as-is; `session` pump tests updated for per-side results; `scan` tests for
one-sided listing incl. a missing side.

### Phase 2 — stat jobs replace metadata compare jobs

- `scan`: `StatJob`/`StatResult`/`DoStat`; `DoCompare` gains the size
  precheck; `compareSymlink` deleted.
- `sidetree`: metadata filled from stat results; `HaveStat`.
- `pairtree`: `Merge` computes the metadata verdict from the two side nodes.
- `session`: triggers enqueue per-file, per-side stat jobs instead of
  two-sided metadata compare jobs; content jobs enqueued only where the two
  sizes are known to match; `OnStatResult` added; `x` clears stat jobs too.
- `ui`: a file row's spinner covers "my stat job is pending on a side I exist
  on"; the gutter rollup spinner covers stat work, which *is* triggered
  comparison work.

Behavior changes: one-sided entries get sizes, so totals complete under
`--level=metadata`; content jobs are skipped wherever sizes already differ.
`--level`'s three values keep their meanings.

Tests: `--level=metadata` reaches full verdicts with **zero content jobs**;
a size mismatch never opens a file; a one-sided file is sized; monotonicity —
a stored content result survives a later stat result.

### Phase 2b — `c` / `C` retarget

Small and independent of the restructuring, but it needs phase 2's
single-entry trigger path: `TriggerCompare` accepting a file target,
`ui.triggerCompare` passing the selected row, `C` bound to the current
directory, and help/status-bar/README updated. Finally implements §5.2's
single-file bullet.

### Phase 3 — multiple pairings in the model, no UI

- `pairtree.Pairing` with its own id, indexes and pair-relative paths.
- `session.OpenPairing`/`ClosePairing`, compare keys namespaced by pairing,
  `Navigate`/`TriggerCompare` taking a pairing id, `OnCompareResult` routing
  and dropping results for closed pairings.
- `SetFoci` wired: navigation sets both side foci on `listQ` and the pairing
  focus on `cmpQ`.

Tests (pump-driven, `-race`): open a pairing over two already-scanned
subtrees and assert no new list jobs are enqueued; open one over a subtree
still being listed and assert it fills in; content-compare in one pairing
doesn't leak into another; results for a closed pairing are dropped without
touching the tally counters.

### Phase 4 — the UI

- Marks (`[`, `]`), `p` to open, stack push/pop, breadcrumb, help overlay and
  status-bar hints, `README.md` keybinding + glyph tables, `SPEC.md` sections.
- pty smoke test (`scripts/`) driving: mark left → navigate → mark right →
  `p` → navigate inside → `←` back out. Key handling is exactly where the
  multi-rune bug lived, so this is not optional.

Rough effort: phase 1 is the bulk (a substantial rewrite of `tree` plus a new
package); 2 is small; 3 is moderate; 4 is moderate and mostly rendering.

## 6. What `SPEC.md` has to say afterwards

- **§3** — new "two side trees + pairings" data model; `(name, type)` matching
  now explicitly a per-pairing operation.
- **§4.2** — totals are per side and cover one-sided entries; the sized-files
  qualifier stays, but now converges instead of being permanently short. The
  "no extra `stat()` is ever issued just to total a directory" rule relaxes to
  "no `stat()` is issued that a triggered comparison didn't ask for" — which
  remains true.
- **§4.3.1** — the root-parent level is per pairing; `←` there pops.
- **new §4.9** — sub-compares: marks, opening, the stack, what's shared
  (settings, side trees, stat results) and what isn't (content results).
- **§5.1** — the metadata level is a per-side stat job covering entries with
  no counterpart, rather than a two-sided compare job; the content level gains
  a size precheck, applied both at enqueue time and in the job.
- **§5.2** — `c` targets the selected row, `C` the current directory; settle
  the "visible entries" wording (this plan's §4.3, item 1).
- **§8.2** — the two pools divide ambient discovery from triggered
  examination; stat jobs run on the compare pool.
- **§8.3** — focus is a set of paths; key namespaces documented.
- **§2.1** — `--level` is unchanged, and the spec should say so explicitly,
  since what the levels *do* underneath changes considerably.
- **§9** — the keybinding table gains `[`, `]`, `p`, and `C`.

`CLAUDE.md`'s architecture section needs the same treatment — it lists six
packages that become seven with different responsibilities.

## 7. Risks and costs

### 7.1 Why metadata is triggered work, not part of listing

An earlier draft of this plan had the list job stat every entry it found.
That was wrong, and the reasons are worth recording, because the question
comes back every time someone notices that listing "already has the directory
open".

**Complete totals are a payoff, not a justification.** Totals fall out of
having metadata; they can't be used to argue for the cost of collecting it.
Demoting them removes the only argument that made unconditional statting look
*necessary* rather than merely convenient.

**Locality doesn't favor inline statting as much as it appears to.** A
directory's entry list and its children's inodes are different on-disk
objects: `readdir` reads the directory's own data blocks, `lstat` reads inodes
from an inode table elsewhere. Locality is *typical* but never guaranteed —
on ext4, files created in one go land in the parent's block group and pack 16
inodes to a 4K block, so statting a freshly-unpacked directory is a short seek
plus near-sequential reads; it degrades with aged directories, near-full
filesystems, very large directories, and files moved or hardlinked in from
elsewhere, and ext4 deliberately spreads top-level directories across block
groups. But the decisive point is that the head movement comes from statting
*at all*, not from *when*: deferring the same stats doesn't reduce the inode
reads, it only reorders them. Only a pass sorted by inode number would
genuinely beat inline, and that is not something dirdiff would do. So the way
to spend fewer seeks on a spinning disk is to stat *less* — a scheduling
decision, which therefore has to stay under the user's control.

**`--level` is already that control.** No new flag and no second axis:
`none` stats nothing, `metadata` arms stat jobs across the tree, `content`
additionally reads bytes. The flag always meant "how much I/O to spend"; it
simply couldn't express "stat entries that have no counterpart to be compared
against", which is the one thing this plan adds to it.

What the restructuring buys anyway:

- **Coverage widens.** One-sided entries get sizes, which is what makes
  totals complete and sub-compares free.
- **Listing stays ~1 syscall per directory**, so first paint is never delayed
  by metadata, on any filesystem.
- **Symlinks leave the compare pool** — a link target is read by the stat job
  and compared in memory.
- **Content jobs are skipped where sizes already differ** (§3.5), which on a
  divergent tree is the largest single I/O saving in this plan.

Two cost notes to keep honest:

- **Stat jobs are per file, so job count stays O(files)** — the same profile
  as today's per-file compare jobs. Per-directory batching would have cut
  queue entries and result messages by the file-to-directory ratio, but it
  can't survive `c` targeting a single row (§4.2): statting one file must not
  stat its 10,000 siblings. Per-file result traffic is tracked separately as
  issue #13 (every result is a Bubble Tea message and therefore a `View()`
  render) and is deliberately out of scope here — it predates this plan and
  is fixable on the receiving side without touching the queues.
- **`--scan-workers` default of 1** is unaffected — listing's cost profile
  doesn't change — but the compare pool now carries stat work as well as
  content work, so its sizing is worth re-measuring.

One place the earlier draft was right: on NFS the Linux client heuristically
uses READDIRPLUS, so attributes ride along with enumeration and inline
statting is nearly free, while a separate stat pass pays a GETATTR per file.
If dirdiff is ever tuned for network filesystems, folding stat into the list
job *for that case specifically* is the optimization to reach for — measured,
not assumed.

### 7.2 Metadata becomes a snapshot

Today a metadata verdict is stat-ed at compare time; afterwards it's whatever
listing saw, possibly minutes earlier on a big tree. For a read-only viewer
this is acceptable, but it argues for a future `refresh` key (re-list a
subtree). Out of scope here; worth a line in the spec.

### 7.3 Memory

Side trees carry size+mtime+link target for every entry (~100 B/node). Pair
nodes add ~100 B per entry per open pairing. At 1M entries per side that's a
few hundred MB for the root pairing — comparable to today, since the merged
tree already holds a node per entry and this plan *removes* two `SideTotals`
structs from each of them. Sub-compares are typically small subtrees, and
they're freed on leaving (§3.5), so the live total is the stack — in practice
the root pairing plus one.

If the root pairing's build ever becomes the bottleneck (it's the only O(whole
tree) allocation), the escape hatch is a directories-only pair tree with file
rows derived on demand — file compare results would live in a per-pairing map
keyed by `PairRel`. Don't do this up front: it complicates `descMatches` and
buys nothing until measured.

### 7.4 Content results don't transfer between pairings

A byte-for-byte comparison (§5.1 — deliberately not a hash) is a statement
about a *pair* of files, so comparing `A` with `B` tells you nothing about
`A` vs `C`. Two pairings over overlapping subtrees each do their own content
work. Making content results transferable would mean switching to per-side
content hashes, which gives up short-circuiting on first difference and always
reads both files in full — a bad trade for the common case. Note it, don't
fix it.

### 7.5 Ordering hazards to test explicitly

- A pairing opened while one of its sides is mid-listing (`Merge` must fire
  from `OnListResult`, not only at open time).
- A recursive content trigger armed on a directory that hasn't listed yet,
  in a *sub*-pairing (`PendingRecursiveLevel` re-check must run per pairing).
- A compare result arriving after its pairing was closed.
- `SetFoci` with foci in different namespaces (`"L/a"`, `"R/b"`, `"p1/c"`) —
  assert each subtree outranks everything else in *its own* namespace, and
  that no key is ever ranked against another namespace's focus (the
  `R/other` case in §3.4).
- A namespace with no focus at all (a suspended pairing's leftover jobs)
  still pops in depth order rather than jumping the queue.

## 8. What deliberately does not change

Read-only, no mutation of either tree. `--level`'s three values and their
meanings. Exact byte-for-byte `(name, type)` matching. Directories-before-
files ordering. Symlinks never followed. Glyph + color always paired. The single-goroutine-mutation rule for tree state. The
row filter's semantics, including that a directory never matches
Equal/Different directly. `View()` stays O(visible rows).

## 9. Follow-ups (explicitly out of scope)

- Progressive merge (show one side's entries before the other side lists,
  upgrading presence in place) instead of the gating in §3.3.
- Persistent pairings with a switcher (tabs) instead of a transient stack —
  only worth it if re-running content compares on re-entry proves annoying.
- READDIRPLUS-aware listing on network filesystems (§7.1).
- Reducing one-render-per-result (issue #13) — independent of this plan.
- **Auto-suggested pairings**: with two side trees in memory, a `left-only`
  directory and a `right-only` directory with the same basename (or similar
  totals) are an obvious move/rename candidate — the UI could offer the
  pairing rather than making the user mark both ends. This is the feature the
  restructuring really unlocks; it's just not v1.
- A `refresh`/re-list key (§7.2).
- Persisting pairings across runs (§10 says no config, so: no).
