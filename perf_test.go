package equityrank

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// perfFixture 构造 50000 名客户、20 个版本、每版 10000 条估值记录。
func perfFixture() ([]Customer, map[string][]ValuationRecord) {
	const (
		numCustomers = 50000
		numModels    = 50
		numBands     = 200 // 50 车型 x 200 区间 = 10000 条/版
		numVersions  = 20
	)
	models := make([]string, numModels)
	for i := range models {
		models[i] = fmt.Sprintf("model-%02d", i)
	}
	customers := make([]Customer, numCustomers)
	for i := range customers {
		customers[i] = Customer{
			ID:       int64(i + 1),
			ModelKey: models[i%numModels],
			Mileage:  int64((i * 37) % (numBands * 1000)),
			Payoff:   int64((i * 7919) % 5000000),
			Past75:   i%11 == 0,
		}
	}
	versions := make(map[string][]ValuationRecord, numVersions)
	for v := 0; v < numVersions; v++ {
		recs := make([]ValuationRecord, 0, numModels*numBands)
		for _, m := range models {
			for band := 0; band < numBands; band++ {
				recs = append(recs, ValuationRecord{
					ModelKey:  m,
					MileageLo: int64(band * 1000),
					MileageHi: int64(band+1) * 1000,
					Value:     int64(500000 + v*12345 + band*777 + len(m)*99),
				})
			}
		}
		versions[fmt.Sprintf("v%02d", v)] = recs
	}
	return customers, versions
}

func percentile(lat []time.Duration, p float64) time.Duration {
	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestPerfUnderLoad 在持续每秒 100 次修改 + 100 次查询下,
// 验证两类调用 p95 均低于 200ms, 且最后的切换在 5 秒内发布。
func TestPerfUnderLoad(t *testing.T) {
	customers, versions := perfFixture()
	s, err := New(customers, versions, "v00", nil)
	if err != nil {
		t.Fatal(err)
	}
	const duration = 3 * time.Second
	var mu sync.Mutex
	var updateLat, queryLat []time.Duration
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	go func() { // 每秒 100 次客户修改
		defer wg.Done()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				id := int64(i%len(customers) + 1)
				start := time.Now()
				if _, err := s.ApplyUpdates([]CustomerUpdate{{
					ID:      id,
					Mileage: ptr(int64((i * 53) % 200000)),
					Payoff:  ptr(int64((i * 131) % 5000000)),
				}}); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				updateLat = append(updateLat, time.Since(start))
				mu.Unlock()
				i++
			}
		}
	}()
	go func() { // 每秒 100 次排行榜查询
		defer wg.Done()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				start := time.Now()
				lb := s.Leaderboard()
				if len(lb.Entries) != 50 {
					t.Errorf("want 50 entries, got %d", len(lb.Entries))
					return
				}
				mu.Lock()
				queryLat = append(queryLat, time.Since(start))
				mu.Unlock()
			}
		}
	}()

	time.Sleep(duration)
	// 负载进行中连续受理多次切换, 停止后最后一个须在 5 秒内发布。
	if _, err := s.Switch("v07"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Switch("v13"); err != nil {
		t.Fatal(err)
	}
	last, err := s.Switch("v19")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if st := last.Wait(); st != SwitchPublished {
		t.Fatalf("last switch = %v, want published", st)
	}
	publishLat := time.Since(start)
	close(stop)
	wg.Wait()

	if v := s.PublishedVersion(); v != "v19" {
		t.Fatalf("published = %q, want v19", v)
	}
	u95 := percentile(updateLat, 0.95)
	q95 := percentile(queryLat, 0.95)
	t.Logf("updates: n=%d p95=%v; queries: n=%d p95=%v; last switch publish=%v",
		len(updateLat), u95, len(queryLat), q95, publishLat)
	if u95 > 200*time.Millisecond {
		t.Fatalf("update p95 = %v, exceeds 200ms", u95)
	}
	if q95 > 200*time.Millisecond {
		t.Fatalf("query p95 = %v, exceeds 200ms", q95)
	}
	if publishLat > 5*time.Second {
		t.Fatalf("last switch publish = %v, exceeds 5s", publishLat)
	}
}
