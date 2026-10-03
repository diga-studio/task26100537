package dealer

import "sync"

// switchRequest is an accepted, not yet published version switch.
type switchRequest struct {
	seq     int64
	version string
	handle  *SwitchHandle
}

// SwitchHandle tracks the terminal state of one switch request.
type SwitchHandle struct {
	// Seq is the acceptance sequence number; later accepted requests have
	// larger numbers and supersede earlier unpublished ones.
	Seq int64
	// Version is the requested valuation version ID.
	Version string

	done    chan struct{}
	once    sync.Once
	outcome SwitchOutcome
}

// Wait blocks until the request is published or superseded.
func (h *SwitchHandle) Wait() SwitchOutcome {
	<-h.done
	return h.outcome
}

// Poll reports the outcome without blocking; ok is false while pending.
func (h *SwitchHandle) Poll() (outcome SwitchOutcome, ok bool) {
	select {
	case <-h.done:
		return h.outcome, true
	default:
		return SwitchOutcome{}, false
	}
}

// finish records the terminal outcome exactly once.
func (h *SwitchHandle) finish(o SwitchOutcome) {
	h.once.Do(func() {
		h.outcome = o
		close(h.done)
	})
}

// Switch accepts a request to publish the given valuation version. Switching
// to a new version, rolling back to a historical one, or re-selecting the
// current version all follow the same rules: the request is assigned an
// increasing acceptance sequence number and replaces any earlier request
// that has not been published yet.
func (l *Leaderboard) Switch(versionID string) (*SwitchHandle, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrClosed
	}
	if _, ok := l.versions[versionID]; !ok {
		l.mu.Unlock()
		return nil, ErrUnknownVersion
	}
	l.lastSeq++
	h := &SwitchHandle{
		Seq:     l.lastSeq,
		Version: versionID,
		done:    make(chan struct{}),
	}
	if l.pending != nil {
		l.pending.handle.finish(SwitchOutcome{
			Status:  SwitchSuperseded,
			Version: l.pending.version,
		})
	}
	l.pending = &switchRequest{seq: l.lastSeq, version: versionID, handle: h}
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return h, nil
}

// run is the background recompute worker loop.
func (l *Leaderboard) run() {
	defer l.wg.Done()
	for {
		select {
		case <-l.done:
			return
		case <-l.wake:
		}
		for {
			l.mu.Lock()
			req := l.pending
			l.pending = nil
			l.mu.Unlock()
			if req == nil {
				break
			}
			l.computeAndPublish(req)
		}
	}
}

// computeAndPublish recomputes the leaderboard for one accepted request and
// publishes it unless a later request has superseded it.
func (l *Leaderboard) computeAndPublish(req *switchRequest) {
	if l.computeHook != nil {
		l.computeHook(req.version)
	}
	l.mu.RLock()
	rev := l.revision
	cv := l.versions[req.version]
	snap := make([]Customer, 0, len(l.customers))
	for _, c := range l.customers {
		snap = append(snap, *c)
	}
	l.mu.RUnlock()

	board := computeBoard(snap, cv)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		req.handle.finish(SwitchOutcome{Status: SwitchSuperseded, Version: req.version})
		return
	}
	if req.seq != l.lastSeq {
		// A later accepted request supersedes this one; its computation
		// must not be published even though it finished.
		req.handle.finish(SwitchOutcome{Status: SwitchSuperseded, Version: req.version})
		return
	}
	if rev != l.revision {
		// Customer data changed while recomputing. Recompute under the
		// write lock so the published board always includes every
		// modification committed before publish.
		board = computeBoard(l.snapshotLocked(), cv)
	}
	l.publishedVersion = req.version
	l.board = board
	req.handle.finish(SwitchOutcome{
		Status:   SwitchPublished,
		Version:  req.version,
		Revision: l.revision,
	})
}
