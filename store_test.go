package equityrank

import (
	"sort"
	"testing"
)

// referenceLeaderboard 是独立的朴素全量计算, 用于交叉核对。
func referenceLeaderboard(customers []Customer, records []ValuationRecord) []Entry {
	entries := make([]Entry, 0, len(customers))
	for _, c := range customers {
		var value int64
		found := false
		for _, r := range records {
			if r.ModelKey == c.ModelKey && c.Mileage >= r.MileageLo && c.Mileage < r.MileageHi {
				value = r.Value
				found = true
				break
			}
		}
		if !found {
			continue
		}
		equity := value - c.Payoff
		entries = append(entries, Entry{
			CustomerID: c.ID,
			Equity:     equity,
			Tier:       tierOf(equity),
			Score:      scoreOf(equity, c.Past75),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Equity != b.Equity {
			return a.Equity > b.Equity
		}
		return a.CustomerID < b.CustomerID
	})
	if len(entries) > 50 {
		entries = entries[:50]
	}
	return entries
}

// testRecords 为车型构造覆盖全部里程的区间: 每 10000 一段。
func testRecords(models []string, valueFn func(model string, band int) int64) []ValuationRecord {
	var recs []ValuationRecord
	for _, m := range models {
		for band := 0; band < 20; band++ {
			recs = append(recs, ValuationRecord{
				ModelKey:  m,
				MileageLo: int64(band * 10000),
				MileageHi: int64(band+1) * 10000,
				Value:     valueFn(m, band),
			})
		}
	}
	return recs
}

func assertEntriesEqual(t *testing.T, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, len(want)=%d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLeaderboardMatchesReference(t *testing.T) {
	models := []string{"suv", "sedan", "truck"}
	records := testRecords(models, func(m string, band int) int64 {
		return int64(len(m)*1000000 + band*77777)
	})
	var customers []Customer
	for i := 0; i < 500; i++ {
		customers = append(customers, Customer{
			ID:       int64(i + 1),
			ModelKey: models[i%len(models)],
			Mileage:  int64((i * 137) % 200000),
			Payoff:   int64((i*9187)%3000000 - 1000000), // 覆盖正/负权益
			Past75:   i%7 == 0,
		})
	}
	// 构造并列: 一批客户权益与机会分完全相同, 验证按编号升序。
	for i := 0; i < 60; i++ {
		customers = append(customers, Customer{
			ID:       int64(1000 + i),
			ModelKey: "sedan",
			Mileage:  5000,
			Payoff:   100000,
			Past75:   false,
		})
	}
	s, err := New(customers, map[string][]ValuationRecord{"v1": records}, "v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	lb := s.Leaderboard()
	if lb.Version != "v1" || lb.Revision != 0 {
		t.Fatalf("unexpected meta: %+v", lb)
	}
	assertEntriesEqual(t, lb.Entries, referenceLeaderboard(customers, records))
}

func TestApplyUpdatesAtomicAndVisible(t *testing.T) {
	records := testRecords([]string{"sedan"}, func(string, int) int64 { return 2000000 })
	customers := []Customer{
		{ID: 1, ModelKey: "sedan", Mileage: 100, Payoff: 1900000},
		{ID: 2, ModelKey: "sedan", Mileage: 100, Payoff: 100000},
	}
	s, err := New(customers, map[string][]ValuationRecord{"v1": records}, "v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 含未知客户的批次整体不生效, 修订号不变。
	if _, err := s.ApplyUpdates([]CustomerUpdate{
		{ID: 1, Payoff: ptr(int64(0))},
		{ID: 999, Payoff: ptr(int64(0))},
	}); err != ErrUnknownCustomer {
		t.Fatalf("want ErrUnknownCustomer, got %v", err)
	}
	if rev := s.Revision(); rev != 0 {
		t.Fatalf("revision changed after failed batch: %d", rev)
	}
	// 合法批次整体提交, 后续查询立即可见。
	rev, err := s.ApplyUpdates([]CustomerUpdate{
		{ID: 1, Payoff: ptr(int64(0))},
		{ID: 2, Mileage: ptr(int64(15000)), Past75: ptr(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}
	updated := []Customer{
		{ID: 1, ModelKey: "sedan", Mileage: 100, Payoff: 0},
		{ID: 2, ModelKey: "sedan", Mileage: 15000, Payoff: 100000, Past75: true},
	}
	lb := s.Leaderboard()
	if lb.Revision != 1 {
		t.Fatalf("leaderboard revision = %d, want 1", lb.Revision)
	}
	assertEntriesEqual(t, lb.Entries, referenceLeaderboard(updated, records))
}

func ptr[T any](v T) *T { return &v }
