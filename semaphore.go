// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package semaphore provides a weighted semaphore implementation.
package semaphore

import (
	"container/list"
	"context"
	"sync"
)

type waiter struct {
	n     int64
	ready chan<- struct{} // Closed when semaphore acquired.
}

// NewWeighted creates a new weighted semaphore with the given
// maximum combined weight for concurrent access.
func NewWeighted(n int64) *Weighted {
	w := &Weighted{size: n}
	return w
}

// Weighted provides a way to bound concurrent access to a resource.
// The callers can request access with a given weight.
type Weighted struct {
	size              int64
	cur               int64
	mu                sync.Mutex
	waiters           list.List
	impossibleWaiters list.List
}

// Acquire acquires the semaphore with a weight of n, blocking until resources
// are available or ctx is done. On success, returns nil. On failure, returns
// ctx.Err() and leaves the semaphore unchanged.
//
// If ctx is already done, Acquire may still succeed without blocking.
func (s *Weighted) Acquire(ctx context.Context, n int64) error {
	s.mu.Lock()
	if s.size-s.cur >= n && s.waiters.Len() == 0 {
		s.cur += n
		s.mu.Unlock()
		return nil
	}

	var waiterList = &s.waiters

	if n > s.size {
		// Add doomed Acquire call to the Impossible waiters list.
		waiterList = &s.impossibleWaiters
	}

	ready := make(chan struct{})
	w := waiter{n: n, ready: ready}
	elem := waiterList.PushBack(w)
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		err := ctx.Err()
		s.mu.Lock()
		select {
		case <-ready:
			// Acquired the semaphore after we were canceled.  Rather than trying to
			// fix up the queue, just pretend we didn't notice the cancelation.
			err = nil
		default:
			waiterList.Remove(elem)
		}
		s.mu.Unlock()
		return err

	case <-ready:
		return nil
	}
}

// TryAcquire acquires the semaphore with a weight of n without blocking.
// On success, returns true. On failure, returns false and leaves the semaphore unchanged.
func (s *Weighted) TryAcquire(n int64) bool {
	s.mu.Lock()
	success := s.size-s.cur >= n && s.waiters.Len() == 0
	if success {
		s.cur += n
	}
	s.mu.Unlock()
	return success
}

// Release releases the semaphore with a weight of n.
func (s *Weighted) Release(n int64) {
	s.mu.Lock()
	s.cur -= n
	if s.cur < 0 {
		s.mu.Unlock()
		panic("semaphore: bad release")
	}
	for {
		next := s.waiters.Front()
		if next == nil {
			break // No more waiters blocked.
		}

		w := next.Value.(waiter)
		if s.size-s.cur < w.n {
			// Not enough tokens for the next waiter.  We could keep going (to try to
			// find a waiter with a smaller request), but under load that could cause
			// starvation for large requests; instead, we leave all remaining waiters
			// blocked.
			//
			// Consider a semaphore used as a read-write lock, with N tokens, N
			// readers, and one writer.  Each reader can Acquire(1) to obtain a read
			// lock.  The writer can Acquire(N) to obtain a write lock, excluding all
			// of the readers.  If we allow the readers to jump ahead in the queue,
			// the writer will starve — there is always one token available for every
			// reader.
			break
		}

		s.cur += w.n
		s.waiters.Remove(next)
		close(w.ready)
	}
	s.mu.Unlock()
}

// Resize semaphore.
func (s *Weighted) Resize(n int64) {
	s.mu.Lock()
	s.size = n
	if s.size < 0 {
		s.mu.Unlock()
		panic("semaphore: bad resize")
	}

	// Add the now possible waiters to waiters list.
	element := s.impossibleWaiters.Front()
	for {
		if element == nil {
			break // No more impossible waiters blocked.
		}

		w := element.Value.(waiter)
		if s.size < w.n {
			// Still Impossible. next.
			element = element.Next()
			continue
		}

		s.waiters.PushBack(w)
		toRemove := element
		element = element.Next()
		s.impossibleWaiters.Remove(toRemove)

	}

	// Add the now impossible-waiters to impossible waiters list.
	element = s.waiters.Front()
	for {
		if element == nil {
			break // No more waiters.
		}

		w := element.Value.(waiter)
		if s.size >= w.n {
			// Still Possible. next.
			element = element.Next()
			continue
		}

		s.impossibleWaiters.PushBack(w)
		toRemove := element
		element = element.Next()
		s.waiters.Remove(toRemove)
	}

	// Release Possible Waiters
	for {
		next := s.waiters.Front()
		if next == nil {
			break // No more waiters blocked.
		}

		w := next.Value.(waiter)
		if s.size-s.cur < w.n {
			// Not enough tokens for the element waiter.  We could keep going (to try to
			// find a waiter with a smaller request), but under load that could cause
			// starvation for large requests; instead, we leave all remaining waiters
			// blocked.
			break
		}

		s.cur += w.n
		s.waiters.Remove(next)
		close(w.ready)
	}
	s.mu.Unlock()
}

// Current returns the current size of semaphore.
// Returned value may instantly change after/during call. use for diagnostic and health-checking only.
func (s *Weighted) Current() int64 {
	return s.cur
}

// Size returns the maximum size of semaphore.
// Returned value may instantly change after/during call. use for diagnostic and health-checking only.
func (s *Weighted) Size() int64 {
	return s.size
}

// Waiters returns the number of currently waiting Acquire calls.
// Returned value may instantly change after/during call. use for diagnostic and health-checking only.
func (s *Weighted) Waiters() int {
	return s.waiters.Len() + s.impossibleWaiters.Len()
}
