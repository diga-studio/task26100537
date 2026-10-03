package dealer

import (
	"errors"
	"sync"
)

var (
	// ErrUnknownCustomer is returned when an update references a customer
	// ID that does not exist.
	ErrUnknownCustomer = errors.New("dealer: unknown customer")
	// ErrUnknownVersion is returned when a switch targets a version that
	// was not supplied at initialization.
	ErrUnknownVersion = errors.New("dealer: unknown valuation version")
	// ErrDuplicateCustomer is returned when the initial customer list
	// contains duplicate IDs.
	ErrDuplicateCustomer = errors.New("dealer: duplicate customer ID")
	// ErrDuplicateVersion is returned when two valuation versions share an
	// ID.
	ErrDuplicateVersion = errors.New("dealer: duplicate version ID")
	// ErrNoValuation is returned when a customer cannot be priced by a
	// valuation version.
	ErrNoValuation = errors.New("dealer: no valuation record matches customer")
	// ErrClosed is returned when operating on a closed Leaderboard.
	ErrClosed = errors.New("dealer: leaderboard is closed")
)

// Leaderboard is the in-memory equity opportunity leaderboard. All methods
// are safe for concurrent use.
type Leaderboard struct {
	mu sync.RWMutex

	customers map[int64]*Customer
	revision  int64

	versions         map[string]*compiledVersion
	publishedVersion string
	board            []Entry

	lastSeq int64
	pending *switchRequest
	wake    chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup
	closed  bool

	// computeHook, when set, runs at the start of every background
	// recompute. Test-only instrumentation.
	computeHook func(version string)
}

// New builds a Leaderboard from the initial customer list, the immutable
// valuation versions, and the initially published version ID.
func New(customers []Customer, versions []ValuationVersion, initialVersion string) (*Leaderboard, error) {
	l := &Leaderboard{
		customers: make(map[int64]*Customer, len(customers)),
		versions:  make(map[string]*compiledVersion, len(versions)),
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	for i := range customers {
		c := customers[i]
		if _, dup := l.customers[c.ID]; dup {
			return nil, ErrDuplicateCustomer
		}
		l.customers[c.ID] = &c
	}
	for _, v := range versions {
		if _, dup := l.versions[v.ID]; dup {
			return nil, ErrDuplicateVersion
		}
		l.versions[v.ID] = compileVersion(v)
	}
	initial, ok := l.versions[initialVersion]
	if !ok {
		return nil, ErrUnknownVersion
	}
	// Validate eagerly that every customer can be priced by every version,
	// so background recomputes never fail after construction.
	snap := l.snapshotLocked()
	for _, cv := range l.versions {
		for _, c := range snap {
			if _, ok := cv.lookup(c.ModelKey, c.Mileage); !ok {
				return nil, ErrNoValuation
			}
		}
	}
	l.publishedVersion = initialVersion
	l.board = computeBoard(snap, initial)
	l.wg.Add(1)
	go l.run()
	return l, nil
}

// Modify atomically applies a batch of customer updates and returns the new
// customer data revision. The leaderboard is recomputed under the currently
// published version before Modify returns.
func (l *Leaderboard) Modify(updates []CustomerUpdate) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, ErrClosed
	}
	for _, u := range updates {
		if _, ok := l.customers[u.ID]; !ok {
			return 0, ErrUnknownCustomer
		}
	}
	for _, u := range updates {
		c := l.customers[u.ID]
		if u.Mileage != nil {
			c.Mileage = *u.Mileage
		}
		if u.PayoffCents != nil {
			c.PayoffCents = *u.PayoffCents
		}
		if u.PastTerm75 != nil {
			c.PastTerm75 = *u.PastTerm75
		}
	}
	l.revision++
	l.board = computeBoard(l.snapshotLocked(), l.versions[l.publishedVersion])
	return l.revision, nil
}

// Query returns the current leaderboard snapshot.
func (l *Leaderboard) Query() Board {
	l.mu.RLock()
	defer l.mu.RUnlock()
	entries := make([]Entry, len(l.board))
	copy(entries, l.board)
	return Board{
		Version:  l.publishedVersion,
		Revision: l.revision,
		Entries:  entries,
	}
}

// Close stops the background recompute worker. Pending switch requests are
// reported as superseded.
func (l *Leaderboard) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	if l.pending != nil {
		l.pending.handle.finish(SwitchOutcome{
			Status:  SwitchSuperseded,
			Version: l.pending.version,
		})
		l.pending = nil
	}
	l.mu.Unlock()
	close(l.done)
	l.wg.Wait()
}

// snapshotLocked copies all customers. Callers must hold l.mu.
func (l *Leaderboard) snapshotLocked() []Customer {
	snap := make([]Customer, 0, len(l.customers))
	for _, c := range l.customers {
		snap = append(snap, *c)
	}
	return snap
}
