#!/usr/bin/env bash
# Generates a left/right directory pair under testdata/playground (by
# default) for manually trying out dirdiff. The generated tree is
# gitignored — regenerate it any time by re-running this script.
#
# Usage: scripts/gen-testdata.sh [output-dir]
#
# Covers, on purpose:
#   - identical files, same-size-different-content, same-content-
#     different-mtime, different-size (distinguishes the size /
#     size+mtime / checksum comparison levels)
#   - files and directories that exist on only one side
#   - a name that's a directory on one side and a plain file on the
#     other, in both directions (independent name+type matching)
#   - an identical subtree, and a subtree with mixed differences at
#     two nesting depths (status rollup propagation)
#   - a fully identical multi-level nested subtree, several levels
#     deep, to see a clean rollup all the way up
#   - a directory that only exists on one side, containing its own
#     nested content (one-sided navigation)
#   - empty directories, both shared and one-sided
#   - a multi-level (5 deep) chain to exercise the background
#     breadth-first scan
#   - symlinks: identical target, differing target, one-sided, and a
#     symlink to a directory
#   - a directory unreadable (chmod 000) on each side, to exercise the
#     error status
#   - larger files (~200KB) that are identical, and that differ only
#     near the end, to exercise the chunked checksum comparison
#   - a directory with 40 entries to exercise scrolling/virtualization
#   - dotfiles, both identical and differing
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

OUT="${1:-$REPO_ROOT/testdata/playground}"
LEFT="$OUT/left"
RIGHT="$OUT/right"

if [ -z "$OUT" ] || [ "$OUT" = "/" ]; then
	echo "refusing to use output dir: $OUT" >&2
	exit 1
fi

# A previous run may have left behind chmod-000 directories (see the
# "unreadable" section below) — restore permissions before removing them,
# otherwise rm -rf can't descend into them.
if [ -d "$OUT" ]; then
	find "$OUT" -type d -exec chmod u+rwx {} + 2>/dev/null || true
	rm -rf "$OUT"
fi
mkdir -p "$LEFT" "$RIGHT"

# put SIDE relpath content
#   SIDE is L (left only), R (right only), or B (both, same content).
put() {
	local side="$1" rel="$2" content="$3"
	if [ "$side" = "L" ] || [ "$side" = "B" ]; then
		mkdir -p "$(dirname "$LEFT/$rel")"
		printf '%s' "$content" >"$LEFT/$rel"
	fi
	if [ "$side" = "R" ] || [ "$side" = "B" ]; then
		mkdir -p "$(dirname "$RIGHT/$rel")"
		printf '%s' "$content" >"$RIGHT/$rel"
	fi
}

# mkd SIDE reldir — create an empty directory.
mkd() {
	local side="$1" rel="$2"
	if [ "$side" = "L" ] || [ "$side" = "B" ]; then mkdir -p "$LEFT/$rel"; fi
	if [ "$side" = "R" ] || [ "$side" = "B" ]; then mkdir -p "$RIGHT/$rel"; fi
}

# symlink SIDE rel target — create a symlink (relative target).
symlink() {
	local side="$1" rel="$2" target="$3"
	if [ "$side" = "L" ] || [ "$side" = "B" ]; then
		mkdir -p "$(dirname "$LEFT/$rel")"
		ln -s "$target" "$LEFT/$rel"
	fi
	if [ "$side" = "R" ] || [ "$side" = "B" ]; then
		mkdir -p "$(dirname "$RIGHT/$rel")"
		ln -s "$target" "$RIGHT/$rel"
	fi
}

### Top-level files: one of each comparison-level distinction ###

put B same_file.txt "identical content on both sides"

put L same_size_diff_content.txt "AAAAAAAAAA"
put R same_size_diff_content.txt "BBBBBBBBBB"

put B same_content_diff_mtime.txt "identical bytes, different timestamps"
touch -t 202001010000 "$LEFT/same_content_diff_mtime.txt"
touch -t 202401010000 "$RIGHT/same_content_diff_mtime.txt"

put L diff_size.txt "short"
put R diff_size.txt "this one is quite a bit longer than the left version"

put L only_in_left.txt "exists only on the left"
put R only_in_right.txt "exists only on the right"

put B .dotfile_same "identical hidden file"
put L .dotfile_diff "left hidden content"
put R .dotfile_diff "right hidden content"

### Name collisions across types (independent name+type matching) ###

put L name_is_dir_on_left/inside.txt "content inside the left-side directory"
put R name_is_dir_on_left "a plain file on the right, same name as a left-side directory"

put L name_is_dir_on_right "a plain file on the left, same name as a right-side directory"
put R name_is_dir_on_right/inside.txt "content inside the right-side directory"

### An identical subtree ###

put B same_dir/a.txt "same a"
put B same_dir/b.txt "same b"
put B same_dir/nested/c.txt "same c"

### A shared directory with differences at two nesting depths (rollup) ###

put B diff_dir/unchanged.txt "unchanged"
put L diff_dir/changed.txt "left version of changed file"
put R diff_dir/changed.txt "right version of changed file"
put L diff_dir/left_only_inside.txt "only on the left, inside diff_dir"
put R diff_dir/right_only_inside.txt "only on the right, inside diff_dir"
put L diff_dir/subdir/deep_changed.txt "left deep changed"
put R diff_dir/subdir/deep_changed.txt "right deep changed"

### Directories that exist on only one side, with their own nested content ###

put L only_in_left_dir/file1.txt "left-only dir, file1"
put L only_in_left_dir/nested/file2.txt "left-only dir, nested file2"
put R only_in_right_dir/file1.txt "right-only dir, file1"

### Empty directories ###

mkd B empty_dir_both
mkd L empty_dir_only_left

### A fully identical, multi-level nested subtree — every file and every
### directory the same on both sides, several levels down, so a recursive
### checksum trigger should show a clean rollup all the way up. Distinct
### from same_dir/ (shallow, one nested level) and deep/ (mixes in a
### differing file).

put B identical_tree/file_a.txt "identical a"
put B identical_tree/file_b.txt "identical b"
put B identical_tree/sub1/file_c.txt "identical c"
put B identical_tree/sub1/sub2/file_d.txt "identical d"
put B identical_tree/sub1/sub2/sub3/file_e.txt "identical e"
put B identical_tree/sub1/sub2/sub3/file_f.txt "identical f"

### A multi-level chain to exercise the background BFS scan ###

put B deep/level1/level2/level3/level4/same_deep.txt "same deep content"
put L deep/level1/level2/level3/level4/diff_deep.txt "left deep version"
put R deep/level1/level2/level3/level4/diff_deep.txt "right deep version"

### Symlinks (never followed — only their target string is compared) ###

symlink B symlinks/link_same_target ../same_file.txt
symlink L symlinks/link_diff_target ../only_in_left.txt
symlink R symlinks/link_diff_target ../only_in_right.txt
symlink L symlinks/link_only_left ../same_file.txt
symlink B symlinks/link_to_dir ../same_dir

### Unreadable directories (permission denied while listing) ###

mkd B unreadable_left
mkd B unreadable_right
chmod 000 "$LEFT/unreadable_left"
chmod 000 "$RIGHT/unreadable_right"

### Larger files, to exercise the chunked checksum comparison ###

mkdir -p "$LEFT/large_files" "$RIGHT/large_files"
dd if=/dev/urandom of="$LEFT/large_files/identical_large.bin" bs=1024 count=200 status=none
cp "$LEFT/large_files/identical_large.bin" "$RIGHT/large_files/identical_large.bin"

dd if=/dev/urandom of="$LEFT/large_files/differs_near_end.bin" bs=1024 count=200 status=none
cp "$LEFT/large_files/differs_near_end.bin" "$RIGHT/large_files/differs_near_end.bin"
printf '\377' | dd of="$RIGHT/large_files/differs_near_end.bin" bs=1 seek=204700 count=1 conv=notrunc status=none

### A directory with many entries, to exercise scrolling/virtualization ###

mkd B many_entries
for i in $(seq 1 40); do
	name=$(printf "file_%02d.txt" "$i")
	if [ $((i % 2)) -eq 0 ]; then
		put B "many_entries/$name" "same content $i"
	else
		put L "many_entries/$name" "left content $i"
		put R "many_entries/$name" "right content $i"
	fi
done

echo "Generated test tree at: $OUT"
echo "Try it with:"
echo "  go run ./cmd/dirdiff \"$LEFT\" \"$RIGHT\""
