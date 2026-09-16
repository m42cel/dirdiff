// Package workqueue provides a generic, key-deduplicated priority queue
// used to back dirdiff's listing and comparison worker pools (SPEC.md §8).
//
// Jobs are identified by a string key (a RelPath). Pushing a job whose key
// is already queued merges into the existing entry instead of duplicating
// work. Pop order is driven entirely by proximity to a live "focus" path
// (SPEC.md §8.3): descendants of the focus path always pop before anything
// else, and within each of those two groups, jobs closer to the focus path
// (in tree-edge distance) pop first. SetFocus changes the focus path and
// re-establishes the heap invariant under the new ordering, so navigating
// reprioritizes an entire subtree — at any depth, not just the focus's
// direct children — without walking or touching individual queued jobs.
package workqueue

import (
	"container/heap"
	"strings"
	"sync"
)

type item[T any] struct {
	key     string
	seq     int64
	payload T
	index   int
}

// pathSegments splits a RelPath into its "/"-joined components; the root
// path "" has zero segments.
func pathSegments(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// isUnder reports whether key is focus itself or lies somewhere in focus's
// subtree. focus == "" (the root) is under everything, so an unset focus
// leaves every job in a single group and pop order falls through to plain
// distance-from-root, then arrival order.
func isUnder(focus, key string) bool {
	return focus == "" || key == focus || strings.HasPrefix(key, focus+"/")
}

// distance is the number of tree edges between the two RelPaths: up from
// key to their lowest common ancestor, then down to focus (or vice versa).
// A child of focus is distance 1; a sibling of focus is distance 2 (one
// edge up to the shared parent, one back down); unrelated branches are
// farther still. It's computed straight off the two path strings — no
// tree traversal or locking needed — which is what makes SetFocus cheap
// enough to call on every navigation.
func distance(focus, key string) int {
	fs, ks := pathSegments(focus), pathSegments(key)
	shared := 0
	for shared < len(fs) && shared < len(ks) && fs[shared] == ks[shared] {
		shared++
	}
	return (len(fs) - shared) + (len(ks) - shared)
}

type itemHeap[T any] struct {
	items []*item[T]
	focus string
}

func (h itemHeap[T]) Len() int { return len(h.items) }
func (h itemHeap[T]) Less(i, j int) bool {
	a, b := h.items[i].key, h.items[j].key
	ua, ub := isUnder(h.focus, a), isUnder(h.focus, b)
	if ua != ub {
		return ua
	}
	da, db := distance(h.focus, a), distance(h.focus, b)
	if da != db {
		return da < db
	}
	return h.items[i].seq < h.items[j].seq
}
func (h itemHeap[T]) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].index, h.items[j].index = i, j
}
func (h *itemHeap[T]) Push(x any) {
	it := x.(*item[T])
	it.index = len(h.items)
	h.items = append(h.items, it)
}
func (h *itemHeap[T]) Pop() any {
	old := h.items
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	h.items = old[:n-1]
	return it
}

// Queue is a priority queue of jobs of type T, deduplicated by key, with a
// blocking Pop for worker goroutines and a live focus path that reorders
// still-queued jobs by proximity.
type Queue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	byKey  map[string]*item[T]
	active map[string]struct{}
	heap   itemHeap[T]
	seq    int64
	closed bool
}

// New creates an empty Queue, focused on the root path.
func New[T any]() *Queue[T] {
	q := &Queue[T]{
		byKey:  map[string]*item[T]{},
		active: map[string]struct{}{},
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Upsert adds a job under key. If key is already queued (not yet popped),
// merge is called on its existing payload to combine it with the new one
// (e.g. to keep the deeper of two requested comparison levels); merge may
// be nil if payload combination isn't needed (the newer payload is then
// simply discarded in favor of the queued one). created reports whether
// this call inserted a brand-new entry (false means it merged into an
// already-queued job), so callers tracking derived per-job state (e.g. a
// subtree pending count) know whether to count it.
func (q *Queue[T]) Upsert(key string, payload T, merge func(old T) T) (created bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if it, ok := q.byKey[key]; ok {
		if merge != nil {
			it.payload = merge(it.payload)
		}
		return false
	}
	q.seq++
	it := &item[T]{key: key, seq: q.seq, payload: payload}
	heap.Push(&q.heap, it)
	q.byKey[key] = it
	q.cond.Signal()
	return true
}

// SetFocus changes the path that pop order is measured against: every
// queued job in focus's subtree (at any depth) moves ahead of every job
// outside it, and each group is then ordered by tree-edge distance from
// focus. Already-running jobs (popped but not yet Done) are unaffected —
// like Boost before it, reprioritization only ever touches work that
// hasn't started (SPEC.md §8.3). This is O(n) in the queue's current
// length (a full heap.Init under the new ordering) rather than O(log n)
// per job, which is the right trade for a queue reordered a few times a
// second at most, on user navigation, against jobs numbering at most in
// the tens of thousands.
func (q *Queue[T]) SetFocus(focus string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.heap.focus == focus {
		return
	}
	q.heap.focus = focus
	heap.Init(&q.heap)
}

// Pop blocks until a job is available or the queue is closed. On success
// it marks the job's key active (in-flight) until Done is called.
func (q *Queue[T]) Pop() (payload T, key string, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.heap.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.heap.items) == 0 {
		return payload, "", false
	}
	it := heap.Pop(&q.heap).(*item[T])
	delete(q.byKey, it.key)
	q.active[it.key] = struct{}{}
	return it.payload, it.key, true
}

// PopUnless is Pop, except it also stops waiting and returns ok=false if
// stop reports true. stop is (re-)checked immediately before every wait,
// so a Wake() call — which by itself only re-evaluates already-blocked
// waiters, the same as a job arriving would — lets a caller tracking some
// externally-owned condition (e.g. a resizable worker pool's shrunk
// target) notice it promptly instead of only the next time Pop would
// otherwise hand back a job. stop is invoked while the queue's own lock
// is held, so it must not call back into this Queue.
func (q *Queue[T]) PopUnless(stop func() bool) (payload T, key string, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.heap.items) == 0 && !q.closed {
		if stop() {
			return payload, "", false
		}
		q.cond.Wait()
	}
	if len(q.heap.items) == 0 {
		return payload, "", false
	}
	it := heap.Pop(&q.heap).(*item[T])
	delete(q.byKey, it.key)
	q.active[it.key] = struct{}{}
	return it.payload, it.key, true
}

// Wake unblocks every call currently waiting in Pop/PopUnless so each
// re-checks its own wait condition, the same as a new job arriving would
// — it doesn't close the queue or add/drop any job. Used to let a
// resizable worker pool's idle-but-blocked workers notice a shrunk
// target immediately rather than only the next time a job arrives.
func (q *Queue[T]) Wake() {
	q.mu.Lock()
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Done marks key as no longer in-flight.
func (q *Queue[T]) Done(key string) {
	q.mu.Lock()
	delete(q.active, key)
	q.mu.Unlock()
}

// IsPending reports whether key is queued or in-flight.
func (q *Queue[T]) IsPending(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, queued := q.byKey[key]
	_, inFlight := q.active[key]
	return queued || inFlight
}

// PendingCount and ActiveCount back the status bar (SPEC.md §4.4 / §8.2).
func (q *Queue[T]) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byKey)
}

func (q *Queue[T]) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.active)
}

// Clear drops all not-yet-started jobs (the global cancel key, SPEC.md
// §5.4) and returns their keys, so a caller tracking derived per-job
// state (e.g. a subtree pending count) can unwind it for jobs that will
// now never produce a result. In-flight jobs already popped by a worker
// are unaffected — they run to completion.
func (q *Queue[T]) Clear() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	keys := make([]string, 0, len(q.byKey))
	for k := range q.byKey {
		keys = append(keys, k)
	}
	q.heap.items = nil
	q.byKey = map[string]*item[T]{}
	return keys
}

// Close unblocks all workers currently waiting in Pop, which then return
// ok=false.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}
