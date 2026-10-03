package dealer

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func testVersions3() []ValuationVersion {
	mk := func(id string, sedanLow, sedanHigh, suv int64) ValuationVersion {
		return ValuationVersion{
			ID: id,
			Records: []ValuationRecord{
				{ModelKey: "sedan", MileageMin: 0, MileageMax: 50000, ValueCents: sedanLow},
				{ModelKey: "sedan", MileageMin: 50000, MileageMax: 1 << 60, ValueCents: sedanHigh},
				{ModelKey: "suv", MileageMin: 0, MileageMax: 1 << 60, ValueCents: suv},
			},
		}
	}
	return []ValuationVersion{
		mk("A", 1200000, 800000, 2000000),
		mk("B", 900000, 500000, 2100000),
		mk("C", 1100000, 700000, 1900000),
	}
}

func versionByID(t *testing.T, versions []ValuationVersion, id string) ValuationVersion {
	t.Helper()
	for _, v := range versions {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("version %q not found", id)
	return ValuationVersion{}
}

func TestSwitchPublishesNewVersion(t *testing.T) {
	versions := testVersions3()
	customers := testCustomers()
	l, err := New(customers, versions, "A")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	h, err := l.Switch("B")
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	out := h.Wait()
	if out.Status != SwitchPublished {
		t.Fatalf("got %v, want published", out.Status)
	}
	b := l.Query()
	if b.Version != "B" || b.Revision != 0 {
		t.Fatalf("got %q/%d, want B/0", b.Version, b.Revision)
	}
	want := independentBoard(customers, versionByID(t, versions, "B"))
	if !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("entries mismatch:\n got %+v\nwant %+v", b.Entries, want)
	}
}

func TestSwitchUnknownVersion(t *testing.T) {
	l, err := New(testCustomers(), testVersions3(), "A")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	if _, err := l.Switch("nope"); err != ErrUnknownVersion {
		t.Fatalf("got %v, want ErrUnknownVersion", err)
	}
}

// TestModifyDuringRecompute: a modification committed while a recompute is
// in flight must be visible under the old version immediately, and must
// still be reflected after the new version is published.
func TestModifyDuringRecompute(t *testing.T) {
	versions := testVersions3()
	customers := testCustomers()
	l, err := New(customers, versions, "A")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	l.computeHook = func(string) {
		close(entered)
		<-release
	}

	h, err := l.Switch("B")
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	<-entered // recompute of B is now in flight

	payoff := int64(100000)
	rev, err := l.Modify([]CustomerUpdate{{ID: 1, PayoffCents: &payoff}})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}
	if rev != 1 {
		t.Fatalf("got revision %d, want 1", rev)
	}

	updated := testCustomers()
	updated[0].PayoffCents = 100000

	// The modification is visible under the still-published old version.
	b := l.Query()
	if b.Version != "A" || b.Revision != 1 {
		t.Fatalf("got %q/%d, want A/1", b.Version, b.Revision)
	}
	if want := independentBoard(updated, versionByID(t, versions, "A")); !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("old-version board mismatch:\n got %+v\nwant %+v", b.Entries, want)
	}

	close(release)
	out := h.Wait()
	if out.Status != SwitchPublished || out.Revision != 1 {
		t.Fatalf("got %v/%d, want published at revision 1", out.Status, out.Revision)
	}

	// The published board under B still includes the modification.
	b = l.Query()
	if b.Version != "B" || b.Revision != 1 {
		t.Fatalf("got %q/%d, want B/1", b.Version, b.Revision)
	}
	if want := independentBoard(updated, versionByID(t, versions, "B")); !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("new-version board mismatch:\n got %+v\nwant %+v", b.Entries, want)
	}
}

// TestSupersedeABA: switches A, B, A are accepted in quick succession while
// the first computation is blocked. Only the last accepted request may be
// published; the others must report superseded.
func TestSupersedeABA(t *testing.T) {
	versions := testVersions3()
	customers := testCustomers()
	l, err := New(customers, versions, "C")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	l.computeHook = func(string) {
		once.Do(func() {
			close(entered)
			<-release
		})
	}

	h1, err := l.Switch("A")
	if err != nil {
		t.Fatalf("Switch A: %v", err)
	}
	<-entered // A's computation is in flight and blocked
	h2, err := l.Switch("B")
	if err != nil {
		t.Fatalf("Switch B: %v", err)
	}
	h3, err := l.Switch("A")
	if err != nil {
		t.Fatalf("Switch A again: %v", err)
	}
	if !(h1.Seq < h2.Seq && h2.Seq < h3.Seq) {
		t.Fatalf("acceptance sequence not increasing: %d, %d, %d", h1.Seq, h2.Seq, h3.Seq)
	}

	// Concurrent queries while computations are pending and finishing.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b := l.Query()
			want := independentBoard(customers, versionByID(t, versions, b.Version))
			if !reflect.DeepEqual(b.Entries, want) {
				t.Errorf("mixed or stale board for %q/%d", b.Version, b.Revision)
				return
			}
		}
	}()

	close(release) // let the blocked A computation finish (out of order)

	if out := h1.Wait(); out.Status != SwitchSuperseded {
		t.Errorf("h1: got %v, want superseded", out.Status)
	}
	if out := h2.Wait(); out.Status != SwitchSuperseded {
		t.Errorf("h2: got %v, want superseded", out.Status)
	}
	if out := h3.Wait(); out.Status != SwitchPublished {
		t.Errorf("h3: got %v, want published", out.Status)
	}
	close(stop)
	wg.Wait()

	b := l.Query()
	if b.Version != "A" {
		t.Fatalf("final version = %q, want A", b.Version)
	}
	want := independentBoard(customers, versionByID(t, versions, "A"))
	if !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("final board mismatch:\n got %+v\nwant %+v", b.Entries, want)
	}
}

// TestConcurrentConsistency interleaves modifications, switches and
// concurrent queries. Every observed (version, revision) board must equal
// an independent from-scratch computation over the customer state recorded
// at that revision.
func TestConcurrentConsistency(t *testing.T) {
	versions := testVersions3()
	base := testCustomers()
	l, err := New(base, versions, "A")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// states[revision] is the full customer list at that revision.
	states := map[int64][]Customer{0: append([]Customer(nil), base...)}

	results := make(chan Board, 4096)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				select {
				case results <- l.Query():
				case <-stop:
					return
				}
			}
		}()
	}

	handles := make([]*SwitchHandle, 0, 4)
	payoff := int64(700000)
	for round := 0; round < 30; round++ {
		payoff += 997
		p := payoff
		rev, err := l.Modify([]CustomerUpdate{{ID: int64(round%4 + 1), PayoffCents: &p}})
		if err != nil {
			t.Fatalf("Modify: %v", err)
		}
		next := append([]Customer(nil), states[rev-1]...)
		next[round%4].PayoffCents = p
		states[rev] = next
		switch round {
		case 5:
			h, _ := l.Switch("B")
			handles = append(handles, h)
		case 10:
			h, _ := l.Switch("A")
			handles = append(handles, h)
		case 15:
			h, _ := l.Switch("C")
			handles = append(handles, h)
		case 20:
			h, _ := l.Switch("A")
			handles = append(handles, h)
		}
	}
	for _, h := range handles {
		h.Wait()
	}
	// Drain in-flight queries, then stop.
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(results)

	checked := 0
	for b := range results {
		state, ok := states[b.Revision]
		if !ok {
			t.Fatalf("query returned unknown revision %d", b.Revision)
		}
		want := independentBoard(state, versionByID(t, versions, b.Version))
		if !reflect.DeepEqual(b.Entries, want) {
			t.Fatalf("board for %q/%d does not match independent computation", b.Version, b.Revision)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no query results collected")
	}
	t.Logf("validated %d concurrent query snapshots", checked)

	// The last accepted switch must be the one that published.
	final := l.Query()
	last := handles[len(handles)-1]
	out := last.Wait()
	if out.Status != SwitchPublished || final.Version != last.Version {
		t.Fatalf("final version %q, last accepted %q (%v)", final.Version, last.Version, out.Status)
	}
}
