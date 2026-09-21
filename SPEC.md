# dirdiff — Specification

## 1. Overview

`dirdiff` is a fast, read-only, interactive terminal UI (TUI) for comparing
two directory trees side by side. It never modifies, deletes, or copies
anything on either side. It is built in Go using the Bubble Tea framework.

Core design goals:

- **Instant startup feedback.** The first level of both directory trees is
  shown as soon as it's read — no upfront deep scan blocks the UI.
- **Background, prioritizable work.** Directory listing (breadth-first, full
  tree, both roots) and content comparison run as background jobs on
  priority queues. Navigating the UI reprioritizes pending work toward what
  you're actually looking at, without blocking on it.
- **Opt-in comparison depth.** Comparing file contents is expensive, so the
  user explicitly chooses how thorough a comparison to run, and where.
- **Read-only.** No delete, copy, move, or write operations of any kind
  against either compared tree.

## 2. Invocation

```
dirdiff [flags] <left-dir> <right-dir>
```

### 2.1 Flags

| Flag | Description |
|---|---|
| `--level=<level>` | Initial comparison level to auto-apply recursively across the whole tree as results come in. One of `metadata`, `content`, `none`. Default: `metadata`. `none` opts back into existence-only (listing/matching, no auto-compare). |
| `--scan-workers=<n>` | Concurrency of the listing worker pool. Default: `2` (§8.2). |
| `--compare-workers=<n>` | Concurrency of the checksum/compare worker pool. Default: `GOMAXPROCS` (§8.2). |
| `--version` | Print version, commit and platform to stdout and exit 0, without entering the TUI. Valid on its own — the two directory arguments are not required with it. |

`--level`'s three values keep exactly the meanings above, whatever
changes underneath: `none` touches nothing beyond `readdir`, `metadata`
reads sizes and dates across the tree, `content` additionally reads
bytes. The flag has always been the knob for how much I/O to spend.

No other flags in v1 (no hidden-file toggle — dotfiles are always shown, no
config persistence, no export). Row-status filtering is available at
runtime via the `f` key (§4.7); there's no flag to preset it at launch.

### 2.2 Startup validation

Before entering the TUI, `dirdiff` validates both path arguments:

- Both paths must exist and be directories.
- If either check fails, print a clear error to stderr and exit with a
  non-zero status. The TUI is never entered in this case.
- Comparing a directory to itself (same path given twice) is allowed and
  proceeds normally — everything will simply show as identical.

## 3. Core data model

State is held as **two side trees and one or more pairings over them.**

A side tree is one compared tree's own cached view of itself: one node
per real filesystem entry, holding what `readdir` reported (name, type)
and what a metadata read added (size, mtime, link target). It knows
nothing about the other side. Listing and metadata are therefore per-side
work, and a subtree read once is read once no matter how many pairings
end up covering it.

A pairing is two directories matched against each other, entry by entry,
plus the merged tree of rows over them. The root pairing is the two
compared roots; a sub-compare (§4.9) is any other two directories, at
unrelated paths. A row points at the two side nodes it pairs, so what it
says about *presence* and *existence* is derived rather than stored, and
the same side node can appear in any number of pairings at once.

A row's children are built only once every side it actually has has been
listed. Showing one side's entries before the other has listed would
flash them as one-sided and then upgrade them, so a row's presence is
fixed when the row is created and never changes afterwards.

What a pairing owns alone is its comparison verdicts — see §4.9.

### 3.1 Entry matching

For a given pair of directories, entries from the left and right listings
are matched by **exact, byte-for-byte name comparison** (no
case-insensitive or Unicode normalization matching). This is intentional:
filesystems on the two sides may differ in case-sensitivity, and exact
matching is the simplest, most predictable behavior. Matching is a
per-pairing operation over the two side trees, which is what lets the same
directory be matched against different counterparts at the same time.

**Files and directories are matched independently by (name, type).** If the
left side has a *file* named `foo` and the right side has a *directory*
named `foo`, these are treated as two unrelated entries:

- Row: directory `foo` — exists on right only, missing on left.
- Row: file `foo` — exists on left only, missing on right.

There is no special "type conflict" status; this falls naturally out of
matching by (name, type) instead of by name alone.

Symlinks are their own type, distinct from files and directories (see §7).

### 3.2 Row status

Each row (a matched or unmatched (name, type) pair) has a status, which is
the union of two independent axes:

**Presence:**
- `both` — entry exists on both sides
- `left-only` — missing on the right
- `right-only` — missing on the left

**Comparison result** (only meaningful when presence is `both`; only
applies to files — directories use rollup, see §3.3):
- `unknown` — not yet compared at any level
- `same` — identical at the deepest level compared so far
- `differs` — different at the deepest level compared so far
- `error` — could not be compared (e.g. permission denied reading one side)

A file's comparison result always reflects the **deepest level compared so
far** (see §5.3, "level monotonicity") — re-running a shallower level never
downgrades or overwrites a deeper known result.

### 3.3 Directory status rollup

Once any descendant of a directory has a known comparison result (or
presence status other than `both`), the directory row displays a rolled-up
indicator summarizing the worst-known status found anywhere beneath it:

- If any descendant is `left-only`/`right-only`/`differs`/`error` (at any
  depth, however that result was obtained — ad-hoc single-file compare,
  a per-level directory compare, or a recursive job), the directory shows a
  "contains differences" marker.
- If all descendants examined so far are `same` (or `both`-existing
  directories whose own descendants are all clean), the directory shows a
  "clean so far" marker.
- If nothing under the directory has been compared yet, it shows "not yet
  known" (distinct from "clean" — this is *absence of information*, not a
  positive result).

Rollup updates live and incrementally as background results stream in; it
only ever reflects work that has actually completed, never implies
completeness of the subtree.

While any comparison job is queued or in flight anywhere in a
directory's subtree, that directory (and every ancestor above it, up to
the root) shows a pending indicator in the gutter in place of its
rollup glyph, so an in-progress subtree is never misread as "clean so
far" or "not yet known" (§6).

Listing-pending is shown separately from the rollup, not as a
replacement for it: each side's tree is listed independently, so a
directory whose subtree still has a listing job outstanding on a given
side shows an animated indicator next to its name **in that side's pane
specifically** — not in the shared gutter, and not necessarily on the
other side, since a one-sided descendant's listing job only ever does
real work on the side it exists on. Listing- and comparing-pending
indicators can be visible at once: a directory whose own comparisons
are still queued shows the gutter spinner while a still-listing
descendant on one side additionally animates that side's name, at the
same time.

## 4. UI layout

### 4.1 Panes

Two fixed 50/50-width panes (left tree, right tree), each showing the
current directory's entries as a virtualized, scrollable list (only visible
rows are rendered/built, so directories with thousands of entries stay
smooth to scroll).

Left and right panes **always navigate in lockstep**: they always show the
corresponding directory in each tree, and the cursor row is always the same
index in both panes conceptually (see §4.3 for the one-sided case).

Each row shows: entry name, type glyph (file/dir/symlink), and a status
glyph+color (see §6). No inline size/mtime/checksum columns — that detail
appears in the details panel instead, to keep rows compact and both panes
aligned.

### 4.2 Details panel

A panel (e.g. bottom of screen) shows full metadata for the row under the
cursor, once known: size, mtime, and comparison level/result for both
sides. For a file/symlink row, it names the deepest level actually run so
far (e.g. "metadata" vs. "content") so the user can tell a cheap
size+date match from an actual byte-for-byte verification. A directory
row shows the same "compared by" line, rolled up from its descendants
(§3.3): the level shared by everything compared under it so far, or
"mixed" if descendants were compared at different levels. Populates
progressively as background jobs resolve that data (shows "—" / a pending
marker for fields not yet fetched).

Sizes are always shown in base-2 units (B, KiB, MiB, GiB, …), the ones
that match how filesystems actually allocate. A single file's size also
names its exact byte count, since the metadata level calls two files
different on an exact size mismatch that rounded units would hide.

**Directory totals.** For a directory row the panel replaces the
single-entry size/mtime lines with, per side, how much the subtree
beneath it holds — directory count, file count, symlink count (only when
there are any) — and the total size of those files. Counts include every
descendant at any depth, not just direct children, and each side is
counted separately: an entry existing on only one side counts only there.
Like the rollup (§3.3), the totals are live and incremental, growing as
listing discovers entries and comparisons report sizes; they never imply
the subtree is fully scanned.

A left-only/right-only directory row (§3.2) has nothing at all under it
on its missing side — not a subtree that happens to total zero — so that
side's line names it as absent (the same "<does not exist>" wording as
the one-sided pane placeholder, §4.3) rather than being printed as a
misleading "0 directories · 0 files · 0 B".

Size, unlike the counts, is never measured for its own sake: no `stat()`
is issued that a triggered comparison didn't ask for (§5.1's metadata
level). What that level reads is per side, so it covers entries with no
counterpart as well — a one-sided subtree reports a real size rather than
being permanently unmeasurable. Consequently:

- A file not yet reached by a metadata (or deeper) level contributes no
  size; once it is, it does, whether or not it exists on both sides.
- A symlink never contributes a size at all — comparing one reads its
  link target, not a file (§7) — which is why symlinks are counted apart
  from files rather than folded into the file count.
- While any file under the directory is unsized, the total is shown as a
  lower bound, alongside how many of the files are actually sized
  ("≥1.4 MiB (12/40 files sized)"), so a partial figure can't be mistaken
  for a complete one. It converges to the exact figure as the level
  sweeps the subtree.
- When none of them is sized — the steady state under `--level=none`,
  where nothing is ever measured — the size shows as `?` rather than a
  misleading `0 B`. A directory that genuinely holds no files shows `0 B`,
  since nothing about it is unknown.

### 4.3 One-sided navigation

If the directory under the cursor doesn't exist on one side (or is a
`left-only`/`right-only` row), you can still press Enter/right-arrow to
navigate down as long as it exists on *at least one* side:

- The existing side shows its real listing, fully interactive.
- The missing side shows a static, inert placeholder ("does not exist")
  with no rows. Cursor movement, selection, and comparison actions apply
  only to the existing side. Pressing "up a level" (parent) still works
  normally from this state — it re-syncs both panes to the shared parent.

### 4.3.1 Above the pairing

Pressing "up a level" while already at the top of a pairing goes up one
further level, so its two directories can be selected as a row in their
own right and their whole-subtree totals (§4.2) read in the details
panel. For the root pairing those two directories are the compared
roots; under a sub-compare (§4.9) they are whatever was paired.

This level is **not** a listing of the two directories' real parents:
those are unrelated to each other, are never listed, and nothing in them
is ever compared. The panes show exactly one row — the left directory in
the left pane, the right one in the right pane, each named as its own
parent would name it, which under a sub-compare means two different names
— with the pairing root's own rolled-up status glyph in the gutter and
its per-side listing-pending indicator, the same as any other directory
row. Each pane's path title shows that side's parent directory, the level
being stood in.

Enter/right-arrow on that row descends back into the pairing. "Up a
level" from there leaves a sub-compare for whatever view it was opened
from (§4.9); at the root pairing there is nothing above, so it does
nothing. The row status filter (§4.7) never applies to this row — it's
the only way back down. Comparing (`c`) from this level applies to the
pairing's root, exactly as it would from inside it.

### 4.4 Status bar

A persistent bottom status bar shows:
- Current path (relative to each root) for both panes.
- Background queue depth / activity summary, e.g.
  `Listing: 1,204 pending · Comparing: 3 active, 42 pending`.
- Context-relevant key hints for labeled/letter keybindings.

### 4.5 Help overlay

Pressing `?` toggles a full-screen overlay listing every keybinding and
what it does. The status bar's hint area only shows a relevant subset at
any time; `?` is the full reference.

### 4.6 Resize behavior

On terminal resize (`tea.WindowSizeMsg`), the layout (pane widths, viewport
heights, details panel) reflows immediately. No enforced minimum size in
v1.

### 4.7 Row status filter

Pressing `f` opens a popup for choosing a **multi-select** row-status
filter, applied to both panes at once, over four statuses: **Left-only**,
**Right-only**, **Equal**, **Different**. `↑`/`↓` move the highlighted
row, `Space` toggles that row's membership in the selection, `Enter`
confirms the selection and closes the popup, `Esc` closes without
changing the current filter (discarding any toggles made since the popup
was opened). `Enter` is a no-op while the in-progress selection is empty
— the popup stays open, since committing an empty set would hide every
row with no way back in from the popup itself. There's no separate "All"
option: selecting all four statuses (the default) is the unfiltered,
show-everything state.

The filter is a third persistent setting alongside compare level (`l`) and
recursive (`r`, §5.2): it's remembered across directory navigation until
changed again, and resets to all-selected on the next launch (no
persisted config, §10).

When the selection isn't all four statuses, a row is visible in the
current directory's listing if either:

- **It matches directly.** Left-only/Right-only match a row's own
  `Presence` — this applies to directories too, independent of anything
  below them: a directory genuinely present on only one side matches
  Left-only/Right-only directly, the same as a file would. Equal/Different
  only ever match a **file or symlink's** own comparison result: `Same`
  for Equal, `Differs`/`error` for Different. A directory never matches
  Equal or Different directly, even when its own rolled-up status (§3.3)
  would resolve that way — a directory's rollup mixes together whatever
  its descendants' presence and comparison statuses happen to be, so
  treating it as a direct match let a directory rolled up to `Differs`
  purely by a left-only/right-only child show under the Different filter
  while looking empty once you navigated into it (that child only
  matches Left-only/Right-only, not Different). A directory only ever
  surfaces for Equal/Different through a matching descendant, below. A
  row whose result is still `unknown` never matches Equal or Different.
- **It has a descendant matching any selected status**, at any depth —
  the four statuses combine as OR, since they're mutually exclusive per
  row. Such a directory is still shown — dimmed, to distinguish it from a
  direct match — purely so you can navigate down to what matched inside
  it.

A directory with neither a direct match nor a matching descendant is
hidden completely, along with everything under it — there's nothing to
navigate to there under the active filter, so unlike the empty-directory
case (§4.1) it isn't shown as an empty, navigable listing. The one-sided
navigation placeholder (§4.3) is unaffected by filtering.

Because comparison and listing results stream in from background jobs
(§8), the filtered view re-evaluates live: a row can appear or disappear
while it's on screen as new results arrive, including a directory that was
hidden suddenly gaining a matching descendant. If the cursor's row is
filtered out from under it, the cursor moves to the nearest row still
visible.

### 4.8 Worker-count popup

Pressing `w` opens a popup listing both worker pools (§8.2) — "Scan
workers" and "Compare workers" — each showing its currently configured
size. `↑`/`↓` move the selection between the two rows; `Enter` opens a
second, separate popup named after the selected row, with its own numeric
input (`0`-`9` only, `Backspace` to correct). `Enter` in that input popup
applies the typed value and closes both popups, returning straight to the
main screen — adjusting the other pool means pressing `w` again. `Esc` in
the input popup cancels without changing anything and returns only to the
row list (not all the way out), so a mis-typed entry can be retried
without reopening the whole thing; `Esc` on the row list closes it the
same way. Applying an empty value (`Enter` pressed without typing any
digit) is a no-op, not a reset to some default.

This resizes the live pool immediately — it's not a setting that's merely
remembered for a future action the way compare level/recursive/filter are.
Growing spawns the additional workers right away; shrinking never
interrupts a job already in flight (the same "let it finish" policy as the
global cancel key, §5.4) — the excess workers simply stop picking up new
jobs once their current one completes, or immediately if they're already
idle.

### 4.9 Sub-compares

A directory that was moved or renamed shows up twice and unhelpfully:
left-only at the old path, right-only at the new one, with nothing to say
whether the contents match. A **sub-compare** is a second comparison view
pairing those two subtrees — rendered exactly like the main one, with the
same panes, glyphs, details panel and keys.

**Choosing.** The panes navigate in lockstep (§4.1), so there is no
moment at which the cursor stands in two unrelated directories — which is
what choosing a pairing would otherwise require. Instead one side is
chosen at a time: `p` starts a selection, `Space` takes the directory
under the cursor for the side currently being chosen, and the second
choice opens the pairing. `Esc` — or `p` again — cancels, restoring the
view to where `p` was pressed, since navigating around to find a
directory was incidental to an operation that didn't happen.

The selection **adds** a key rather than rebinding any: every movement
key, `→`/`Enter` to descend included, means exactly what it means outside
the mode. That is how a directory outside the current listing is reached
— one-sided directories are navigable (§4.3), so lockstep navigation
reaches every directory in either tree — and it means there is nothing to
unlearn on the way in or out and no way to confirm a choice by reflex.

Choosing a side a row doesn't have, or anything but a directory, is a
no-op with a note in the status bar (pairing two files would be a one-row
view of no value); the selection stays open so the next candidate is one
keypress away. The row above the pairing (§4.3.1) can be chosen too, so a
whole root can be one half. Leaving the pairing entirely (§4.3.1's `←`)
is refused while a selection is running, since it would close the very
view the selection started in.

**The stack.** `p` pushes the new pairing; `←` past its top row (§4.3.1)
pops back to whichever view it was opened from. Sub-compares nest: `p`
from inside one pushes another, and popping returns to the one it was
opened from rather than to the root. The root pairing is the bottom of
the stack and is never popped.

**Lifetime.** A sub-compare lives only while you're in it: leaving drops
its rows, its content verdicts and its still-queued content jobs. Jobs
already in flight run to completion and their results are discarded
(§5.4's "let it finish"). Memory is therefore bounded by stack depth, not
by how many sub-compares have been opened.

**What is shared and what isn't.** Listing and metadata are read per side
(§3, §5.1) and shared by every pairing over the same directories, so
opening a sub-compare over already-scanned subtrees costs no I/O at all —
the pairing is a pure in-memory merge, and existence and metadata are
there immediately. The settings (§5.2's level and recursive, §4.7's
filter) are global too. What a pairing owns alone is its content
verdicts: a byte-for-byte comparison is a statement about a *pair* of
files, so comparing A with B says nothing about A vs C. That is why they
are not transferable, and why re-entering a pair rebuilds instantly for
everything except bytes you had asked to read.

**Rendering.** Each pane's path title shows that side's own real path,
which under a sub-compare simply diverge. The status bar names the
pairing while one is open.

While a selection is running, the side **not** currently being chosen is
faded, shifting the eye to the column the choice is coming from, and the
status bar says which side `Space` would take. The directory already
chosen is marked with its own glyph and color (§6's never-color-alone
rule applies to it as much as to a status) wherever it is on screen, and
that marking wins over the fade — the point of it is that it stays
findable. Since it is usually scrolled out of sight by the time its
counterpart is being hunted for, the status bar carries it too, named by
its path below its own root so two candidates sharing a basename are
told apart.

## 5. Comparison levels and triggering

### 5.1 Levels

| Level | What it checks | Cost |
|---|---|---|
| *(baseline, automatic)* Existence | Entry present on both sides, by name+type | Free — a byproduct of directory listing, not a triggered action |
| Metadata | File size **and** modification time equal (one `lstat()` per file **per side**; both fields must match) | Cheap, one syscall per file per side |
| Content | Streaming byte-for-byte comparison, reading both files in parallel chunks and short-circuiting on first difference (not a hash/checksum — a direct read of both sides) | Expensive — full (or partial, on early mismatch) file read of both sides |

The metadata level is a per-side read, not a two-sided comparison: each
side's entry is `lstat()`ed once into that side's own state, and the
verdict is then an equality test over the two results — no I/O, and the
same answer however the two sides were matched. A symlink is settled the
same way, by the two link targets the reads returned (§7), so it never
reaches the compare pool at all. Because the read is per side, it also
covers entries that exist on one side only, which is what makes §4.2's
totals complete.

The content level inherits a precheck from that: two files of different
lengths cannot have equal content, so a size mismatch is already a
conclusive content verdict and the read is never performed. The check
applies where the job would be created — a file whose two sides are known
to differ in size never becomes a job — and again inside the job, for a
file that changed since it was measured.

Because metadata is read once and kept, what a row says is a snapshot of
when it was read rather than of now — possibly minutes ago on a large
tree. For a read-only viewer that is acceptable, but it is the argument
for a future refresh key (re-list and re-measure a subtree), which v1
does not have.

Size and mtime are a single combined level ("metadata"), not two separate
ones — a file only counts as "same" at this level if both match.

Existence is never a user-triggered action — it's simply the state every
row is in immediately once its parent directory's listing has completed on
both sides.

### 5.2 Triggering

- Two persistent settings, changed independently of triggering and
  remembered across triggers: the **compare level** (metadata or content,
  default metadata) and the **recursive** toggle (default on). `l`
  switches the compare level; `r` toggles recursive on/off. Neither key
  enqueues any work by itself.
- `c` runs a comparison at the current level/recursive setting on the
  **row under the cursor**. A file or symlink row is compared on its own
  — recursive means nothing for it. A directory row compares its direct
  entries, or, with recursive on, the entire subtree rooted at it,
  feeding the background priority queue. Above the roots (§4.3.1) the
  selected row is the root pair, so `c` there compares the roots.
- `C` runs the same thing on the **current working directory** — the one
  being stood in — whatever the cursor happens to be on. It applies to
  every entry of that directory, including any the active filter is
  hiding: the filter is a view concern, and a recursive trigger reaches
  hidden descendants anyway.

### 5.3 Level monotonicity

Re-triggering a shallower level on a row that already has a deeper result
never downgrades the displayed status (§3.2). It may still redundantly
re-verify the shallow condition, but the UI continues showing the deepest
known result.

### 5.4 Job lifecycle and cancellation

- Triggered comparisons (single-dir or recursive) are enqueued as jobs on
  the examination worker pool, prioritized above ambient background
  listing work. So are the metadata reads a level needs (§5.1) — they are
  triggered work in exactly the same sense.
- Navigating away from a directory with in-flight or queued jobs does
  **not** cancel them — they keep running at lower priority (deprioritized
  below whatever you navigate into next) and their results fill in
  whenever you scroll back, updating rollup status live.
- There is a global cancel/clear-queue key (`x`) to drop all pending (not
  yet started) queued examination jobs, metadata reads included.

## 6. Status indicators

Every status uses **both a distinct glyph and a distinct color** — never
color alone — so the UI remains usable for colorblind users and in
limited-color terminals.

| Status | Example glyph | Color |
|---|---|---|
| Same | `=` | green |
| Differs | `≠` | red |
| Missing on right (left-only) | `←` (points to the side it's on) | yellow |
| Missing on left (right-only) | `→` (points to the side it's on) | yellow |
| Error / unreadable | `!` | magenta |
| Pending / in-progress (single file, own comparison) | animated Braille spinner (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`) | gray/blue |
| Unknown (not yet compared) | `?` (dim) | dim/gray |
| Directory rollup: contains differences | e.g. bold `≠` | red |
| Directory rollup: clean so far | e.g. dim `=` | green |
| Directory rollup: not yet known | (no rollup glyph) | dim/gray |
| Directory rollup: comparing pending anywhere in subtree (gutter) | animated Braille spinner (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`) | blue |
| Directory: listing pending on a given side's subtree (next to the name, that pane only) | animated `.` → `..` → `...` | blue |

The single-file and directory-rollup pending indicators intentionally
share the same Braille spinner — there's no user-visible distinction
between "this file is comparing" and "something inside this directory
is comparing".

The listing-pending indicator is per-pane, not per-row: a directory
missing on one side never shows it on that (nonexistent) side.

The two subtree-pending rollup glyphs take priority over the
completed-result rollup glyphs above them, and over each other: listing
pending wins over comparing pending, since a descendant can't be queued
for comparison before it's been discovered by listing.

## 7. Symlinks

Symlinks are **never followed** and are treated as their own distinct entry
type (separate from files and directories):

- Listing shows them with a distinct type glyph.
- Comparison (any level) compares the **link target string itself**, not
  the target's content — e.g. "size" for a symlink is undefined/not
  applicable; the metadata level's per-side read (§5.1) collects the
  `readlink()` target, and the two targets are then compared as strings.
  That is the whole of what a symlink can be compared by, so the verdict
  is final rather than provisional: a later content trigger has nothing
  deeper to read and never opens anything.
- A symlink's own size is never counted toward a directory's totals
  (§4.2), since it measures the link rather than what it names.
- This avoids symlink-cycle handling entirely and avoids ever reading data
  outside the two compared root trees.

## 8. Background scanning architecture

### 8.1 Initial listing

On startup:
1. Both root paths are `stat()`'d (already validated to exist/be dirs).
2. The **first level** of both roots is listed synchronously enough to
   render the initial UI immediately (or as the very first background job,
   at highest priority) — the user should see top-level entries right
   away.
3. All discovered subdirectories are enqueued onto the **listing** worker
   pool for breadth-first traversal, unbounded — the entire tree (both
   sides) is eventually listed to completion in the background, however
   large. Listing is cheap (directory reads only, no per-file stat calls
   beyond what the OS returns from `readdir`), so this is safe to run
   fully in the background without a depth/size cap.

### 8.2 Worker pools

Two separate pools:

- **Listing pool** — ambient discovery: directory reads (`readdir`) for
  the BFS traversal. Small, fast jobs; sized to stay responsive even under
  load from the other pool.
- **Examination pool** — triggered work: the per-side `lstat()` calls of
  the metadata level and the byte-wise content comparisons. This is where
  expensive, I/O-heavy work happens.

The split is between work nobody asked for and work somebody did, rather
than merely between cheap and expensive: everything in the examination
pool is the product of a trigger (§5.2) or of `--level`'s ambient arming,
and is therefore what `x` cancels (§5.4).

Keeping these separate ensures a large recursive checksum job doesn't
starve the ambient directory-listing scan (and vice versa), which matters
because listing is what makes the UI feel instantly responsive when you
navigate somewhere new.

The two pools are sized independently — `--scan-workers=<n>` (default `2`)
for listing, `--compare-workers=<n>` (default `GOMAXPROCS`) for comparison —
rather than sharing one knob. Listing is cheap, low-CPU directory-metadata
I/O that doesn't benefit much from scaling with core count, and on a
mechanical disk more concurrent listing jobs can mean more seeking for no
throughput gain — but it wants at least two, because a listing job reads
one side (§3) and a single worker would read a directory's two sides one
after the other; comparison, especially at the content level, does real per-byte CPU
work alongside the I/O, so scaling it with `GOMAXPROCS` is the more
defensible default of the two. Both flags only set the starting size —
either pool can be resized while dirdiff is running via the `w` popup
(§4.8), e.g. to react to how a particular pair of devices actually
performs rather than guessing correctly up front.

### 8.3 Priority queue and reprioritization

Both pools are backed by a priority queue (not FIFO), ordered by tree-edge
distance from a live "focus" path rather than by fixed tiers.

A job's key is a path prefixed by a namespace segment naming which tree
that path is in: `L/…` and `R/…` for per-side work (listing and
metadata), and one namespace per pairing for its content comparisons.
Focus is therefore a **set** of paths, one per namespace, and each is
measured against its own namespace alone. The two panes stand in two
unrelated paths under a sub-compare, so no single path expresses what the
user is looking at — and a "distance" between two unconnected trees isn't
a tree distance at all: with no shared ancestor it degenerates to the sum
of both depths, which can undercut a real same-tree distance and rank one
tree's job by how deep the other pane happens to be standing. A namespace
with no focus — a suspended pairing's leftovers, or anything before the
first navigation — is simply never "under" anything and pops in depth
order within its own tree, which is the breadth-first order §8.1 wants.

- Every queued job in its own focus path's subtree — at any depth, not
  just its direct children — pops ahead of every job outside it; within
  each of those two groups, jobs closer to the focus path (fewer tree
  edges away) pop first, and arrival order breaks remaining ties.
- **Navigating between directories** (moving the cursor's "current
  directory" — i.e. Enter/back, not just moving the cursor up/down within
  the same directory's entries) immediately makes the newly entered
  directory the focus, reordering both queues: each side of it, plus the
  pairing showing it, becomes its namespace's focus, so their listing
  jobs (if not already complete) and their whole known subtrees jump
  ahead of everything else, and jobs from wherever the focus used to be
  fall back to plain distance/arrival order.
- Moving the cursor within the same directory's already-listed entries
  does **not** trigger reprioritization — only directory changes do.
- Comparison jobs triggered by the user (§5.2) always target the
  directory the user is currently in, so they're already inside the
  current focus path and sort ahead of unrelated ambient background work
  without needing a priority of their own.

### 8.4 Error handling during scanning

Permission errors or other read failures on a specific entry (file or
directory) do not stop the scan or the affected job queue. The specific
row is marked with the `error` status (§6) and the scan/comparison
continues past it. No aggregate error log/panel in v1 — error details for
a specific row are visible via the details panel (§4.2) when that row is
selected.

## 9. Navigation and keybindings

Non-vim: arrow keys for movement, labeled letter/number keys for actions,
always with the footer hint bar (§4.4) showing what's currently available,
plus the full reference via `?` (§4.5).

| Key | Action |
|---|---|
| `↑` / `↓` | Move cursor within current directory listing (both panes move together) |
| `→` / `Enter` | Navigate into directory under cursor (both panes descend together; one-sided case per §4.3) |
| `←` / `Backspace` | Navigate to parent directory (both panes ascend together); at the pairing's root, up to its two directories as a single row (§4.3.1); past that, out of a sub-compare (§4.9) |
| `l` | Switch the persistent compare-level setting: metadata (size+date) ↔ content (byte-for-byte) (remembered until changed again) |
| `r` | Toggle the persistent recursive setting on/off (remembered; default on) |
| `f` | Open the row-status filter popup: multi-select Left-only / Right-only / Equal / Different (§4.7; remembered like `l`/`r`) |
| `w` | Open the worker-count popup: resize the scan/compare pools live (§4.8) |
| `c` | Compare the selected row at the current level/recursive setting |
| `C` | Compare the current directory at the current level/recursive setting |
| `p` | Start choosing a sub-compare; while choosing, cancel it (§4.9) |
| `Space` | While choosing a sub-compare, take the highlighted directory for the side being chosen (§4.9) |
| `Esc` | While choosing a sub-compare, cancel (§4.9) |
| `n` / `N` | Jump to next / previous entry in the current directory whose status isn't "same" (only considers entries already compared at some level) |
| `X` / `Esc` | Cancel/clear all pending (not-yet-started) queued comparison jobs |
| `?` | Toggle full keybinding help overlay |
| `q` / `Ctrl+C` | Quit |

## 10. Explicit non-goals (v1)

- No file/directory content viewing or diffing (no "show me the actual
  byte differences" pane) — status only, not a content diff tool.
- No delete, copy, move, rename, or any other filesystem mutation.
- No config file / persisted settings across runs — every run starts from
  defaults.
- No export of diff results to a file/report.
- No glob/pattern filtering of visible entries — only the fixed row-status
  filter (§4.7).
- No hidden-file toggle — dotfiles are always shown, unconditionally.
- No resizable pane split — fixed 50/50.
- No symlink-following.
- No Windows support as a design constraint (may incidentally work via
  Bubble Tea, but macOS + Linux are the only tested/targeted platforms).

## 11. Platform target

macOS and Linux only. POSIX filesystem semantics (permissions, symlinks)
are assumed; no special-casing for Windows path separators or ACLs.
