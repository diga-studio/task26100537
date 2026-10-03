package dealer

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

const (
	perfCustomers       = 50000
	perfVersions        = 20
	perfRecordsPerVer   = 10000
	perfModels          = 10
	perfIntervalsPerMod = perfRecordsPerVer / perfModels
	perfMileageSpan     = int64(1_000_000_000)
)

// buildPerfData deterministically generates the acceptance-scale dataset:
// 50000 customers, 20 versions of 10000 valuation records each.
func buildPerfData(t *testing.T) ([]Customer, []ValuationVersion) {
	t.Helper()
	rng := rand.New(rand.NewSource(1))

	versions := make([]ValuationVersion, 0, perfVersions)
	for v := 0; v < perfVersions; v++ {
		recs := make([]ValuationRecord, 0, perfRecordsPerVer)
		width := perfMileageSpan / perfIntervalsPerMod
		for m := 0; m < perfModels; m++ {
			for i := int64(0); i < perfIntervalsPerMod; i++ {
				recs = append(recs, ValuationRecord{
					ModelKey:   fmt.Sprintf("model-%d", m),
					MileageMin: i * width,
					MileageMax: (i + 1) * width,
					ValueCents: 500000 + rng.Int63n(4_500000),
				})
			}
		}
		versions = append(versions, ValuationVersion{
			ID:      fmt.Sprintf("v%02d", v),
			Records: recs,
		})
	}

	customers := make([]Customer, 0, perfCustomers)
	for i := 0; i < perfCustomers; i++ {
		customers = append(customers, Customer{
			ID:          int64(i + 1),
			ModelKey:    fmt.Sprintf("model-%d", rng.Intn(perfModels)),
			Mileage:     rng.Int63n(perfMileageSpan),
			PayoffCents: rng.Int63n(4_000000),
			PastTerm75:  rng.Intn(4) == 0,
		})
	}
	return customers, versions
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestPerfAcceptance sustains 100 customer modifications and 100 leaderboard
// queries per second at full scale, then verifies the last accepted switch
// publishes within 5 seconds while modifications continue.
func TestPerfAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping perf test in -short mode")
	}
	customers, versions := buildPerfData(t)
	l, err := New(customers, versions, "v00")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// local mirrors the committed customer state for the final check.
	local := make(map[int64]Customer, len(customers))
	for _, c := range customers {
		local[c.ID] = c
	}

	const loadDuration = 3 * time.Second
	const tick = 10 * time.Millisecond // 100 ops/sec per stream

	modLat := make([]time.Duration, 0, 2*int(loadDuration/tick))
	queryLat := make([]time.Duration, 0, 2*int(loadDuration/tick))

	stop := make(chan struct{})
	queryDone := make(chan struct{})
	go func() {
		defer close(queryDone)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			start := time.Now()
			b := l.Query()
			queryLat = append(queryLat, time.Since(start))
			if len(b.Entries) > boardSize {
				t.Errorf("query returned %d entries", len(b.Entries))
				return
			}
		}
	}()

	rng := rand.New(rand.NewSource(2))
	modTicker := time.NewTicker(tick)
	deadline := time.Now().Add(loadDuration)
	for time.Now().Before(deadline) {
		<-modTicker.C
		id := int64(rng.Intn(perfCustomers) + 1)
		mileage := rng.Int63n(perfMileageSpan)
		payoff := rng.Int63n(4_000000)
		past := rng.Intn(4) == 0
		start := time.Now()
		if _, err := l.Modify([]CustomerUpdate{{
			ID:          id,
			Mileage:     &mileage,
			PayoffCents: &payoff,
			PastTerm75:  &past,
		}}); err != nil {
			t.Fatalf("Modify: %v", err)
		}
		modLat = append(modLat, time.Since(start))
		c := local[id]
		c.Mileage, c.PayoffCents, c.PastTerm75 = mileage, payoff, past
		local[id] = c
	}
	modTicker.Stop()

	// Issue the final switch while modifications continue at 100/sec.
	switchDone := make(chan struct{})
	var publishLatency time.Duration
	go func() {
		defer close(switchDone)
		h, err := l.Switch("v17")
		if err != nil {
			t.Errorf("Switch: %v", err)
			return
		}
		start := time.Now()
		out := h.Wait()
		publishLatency = time.Since(start)
		if out.Status != SwitchPublished {
			t.Errorf("final switch: got %v, want published", out.Status)
		}
	}()
	modTicker = time.NewTicker(tick)
	for i := 0; i < 20; i++ {
		<-modTicker.C
		id := int64(rng.Intn(perfCustomers) + 1)
		payoff := rng.Int63n(4_000000)
		if _, err := l.Modify([]CustomerUpdate{{ID: id, PayoffCents: &payoff}}); err != nil {
			t.Fatalf("Modify: %v", err)
		}
		c := local[id]
		c.PayoffCents = payoff
		local[id] = c
	}
	modTicker.Stop()
	<-switchDone
	close(stop)
	<-queryDone

	sort.Slice(modLat, func(i, j int) bool { return modLat[i] < modLat[j] })
	sort.Slice(queryLat, func(i, j int) bool { return queryLat[i] < queryLat[j] })
	modP95 := percentile(modLat, 0.95)
	queryP95 := percentile(queryLat, 0.95)
	t.Logf("modifies: n=%d p50=%v p95=%v max=%v", len(modLat),
		percentile(modLat, 0.50), modP95, modLat[len(modLat)-1])
	t.Logf("queries:  n=%d p50=%v p95=%v max=%v", len(queryLat),
		percentile(queryLat, 0.50), queryP95, queryLat[len(queryLat)-1])
	t.Logf("final switch published in %v", publishLatency)

	if modP95 > 200*time.Millisecond {
		t.Errorf("modify p95 = %v, exceeds 200ms", modP95)
	}
	if queryP95 > 200*time.Millisecond {
		t.Errorf("query p95 = %v, exceeds 200ms", queryP95)
	}
	if publishLatency > 5*time.Second {
		t.Errorf("final switch publish latency = %v, exceeds 5s", publishLatency)
	}

	// The published board must match an independent full computation over
	// the final customer state.
	b := l.Query()
	if b.Version != "v17" {
		t.Fatalf("final version = %q, want v17", b.Version)
	}
	final := make([]Customer, 0, len(local))
	for _, c := range local {
		final = append(final, c)
	}
	want := independentBoard(final, versionByID(t, versions, "v17"))
	if !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("final board does not match independent computation")
	}
}
