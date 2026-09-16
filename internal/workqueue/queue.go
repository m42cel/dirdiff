// Package workqueue provides a generic, key-deduplicated priority queue
// used to back dirdiff's listing and comparison worker pools (SPEC.md §8).
//
// Jobs are identified by a string key (a RelPath). Pushing a job whose key
// is already queued merges into the existing entry instead of duplicating
// work, and always keeps the higher of the two priorities. Reprioritizing
// an in-flight navigation target is a Boost call, which is a no-op if the
// job already started or was never queued.
package workqueue

import (
	"container/heap"
	"sync"
)

// Priority determines pop order; lower values pop first. The same three
// tiers are reused by both the listing and comparison pools (SPEC.md §8.3):
// the thing the user is looking at right now, the thing they'll probably
// look at next, and everything else running ambiently in the background.
type Priority int

const (
	High Priority = iota
	Medium
	Low
)

type item[T any] struct {
	key      string
	priority Priority
	seq      int64
	payload  T
	index    int
}

type itemHeap[T any] []*item[T]

func (h itemHeap[T]) Len() int { return len(h) }
func (h itemHeap[T]) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority < h[j].priority
	}
	return h[i].seq < h[j].seq
}
func (h itemHeap[T]) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *itemHeap[T]) Push(x any) {
	it := x.(*item[T])
	it.index = len(*h)
	*h = append(*h, it)
}
func (h *itemHeap[T]) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return it
}

// Queue is a priority queue of jobs of type T, deduplicated by key, with a
// blocking Pop for worker goroutines and live priority/payload updates for
// jobs still waiting to run.
type Queue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	byKey  map[string]*item[T]
	active map[string]struct{}
	heap   itemHeap[T]
	seq    int64
	closed bool
}

// New creates an empty Queue.
func New[T any]() *Queue[T] {
	q := &Queue[T]{
		byKey:  map[string]*item[T]{},
		active: map[string]struct{}{},
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Upsert adds a job under key at the given priority. If key is already
// queued (not yet popped), merge is called on its existing payload to
// combine it with the new one (e.g. to keep the deeper of two requested
// comparison levels), and the job's priority is raised if the new
// priority is higher. merge may be nil if payload combination isn't
// needed (the newer payload is then simply discarded in favor of the
// queued one). created reports whether this call inserted a brand-new
// entry (false means it merged into an already-queued job), so callers
// tracking derived per-job state (e.g. a subtree pending count) know
// whether to count it.
func (q *Queue[T]) Upsert(key string, priority Priority, payload T, merge func(old T) T) (created bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if it, ok := q.byKey[key]; ok {
		if merge != nil {
			it.payload = merge(it.payload)
		}
		if priority < it.priority {
			it.priority = priority
			heap.Fix(&q.heap, it.index)
		}
		return false
	}
	q.seq++
	it := &item[T]{key: key, priority: priority, seq: q.seq, payload: payload}
	heap.Push(&q.heap, it)
	q.byKey[key] = it
	q.cond.Signal()
	return true
}

// Boost raises the priority of key if it is still queued (not yet
// popped). It is a no-op if the job isn't queued (already running,
// already done, or never enqueued) — SPEC.md §8.3 navigation
// reprioritization only ever affects work that hasn't started yet.
func (q *Queue[T]) Boost(key string, priority Priority) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if it, ok := q.byKey[key]; ok && priority < it.priority {
		it.priority = priority
		heap.Fix(&q.heap, it.index)
	}
}

// Pop blocks until a job is available or the queue is closed. On success
// it marks the job's key active (in-flight) until Done is called.
func (q *Queue[T]) Pop() (payload T, key string, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.heap) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.heap) == 0 {
		return payload, "", false
	}
	it := heap.Pop(&q.heap).(*item[T])
	delete(q.byKey, it.key)
	q.active[it.key] = struct{}{}
	return it.payload, it.key, true
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
	q.heap = nil
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
