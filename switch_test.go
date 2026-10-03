package equityrank

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// gatedHook 返回一个 RecomputeHook 及其放行函数, 用于控制各请求的重算完成顺序。
func gatedHook() (func(seq uint64, version string), func(seq uint64)) {
	gates := map[uint64]chan struct{}{}
	var mu sync.Mutex
	hook := func(seq uint64, version string) {
		mu.Lock()
		ch, ok := gates[seq]
		if !ok {
			ch = make(chan struct{})
			gates[seq] = ch
		}
		mu.Unlock()
		<-ch
	}
	release := func(seq uint64) {
		mu.Lock()
		ch, ok := gates[seq]
		if !ok {
			ch = make(chan struct{})
			gates[seq] = ch
		}
		mu.Unlock()
		close(ch)
	}
	return hook, release
}

func switchVersions() map[string][]ValuationRecord {
	models := []string{"sedan", "suv"}
	return map[string][]ValuationRecord{
		"A": testRecords(models, func(m string, band int) int64 { return 1000000 + int64(band)*1000 }),
		"B": testRecords(models, func(m string, band int) int64 { return 3000000 - int64(band)*2000 }),
	}
}

func switchCustomers() []Customer {
	var customers []Customer
	for i := 0; i < 200; i++ {
		customers = append(customers, Customer{
			ID:       int64(i + 1),
			ModelKey: []string{"sedan", "suv"}[i%2],
			Mileage:  int64((i * 997) % 200000),
			Payoff:   int64((i * 3131) % 2000000),
			Past75:   i%5 == 0,
		})
	}
	return customers
}

func TestSwitchOutOfOrderCompletion(t *testing.T) {
	hook, release := gatedHook()
	s, err := New(switchCustomers(), switchVersions(), "A", &Options{RecomputeHook: hook})
	if err != nil {
		t.Fatal(err)
	}
	// 连续受理 A(回切当前版本)、B、A, 三个请求都阻塞在重算中。
	r1, _ := s.Switch("A")
	r2, _ := s.Switch("B")
	r3, _ := s.Switch("A")
	if r1.Seq != 1 || r2.Seq != 2 || r3.Seq != 3 {
		t.Fatalf("unexpected seqs: %d %d %d", r1.Seq, r2.Seq, r3.Seq)
	}
	// 受理即取代: 前两个请求应已标记为被取代。
	if st := r1.Status(); st != SwitchSuperseded {
		t.Fatalf("r1 status = %v, want superseded", st)
	}
	if st := r2.Status(); st != SwitchSuperseded {
		t.Fatalf("r2 status = %v, want superseded", st)
	}
	// 乱序放行: 先放行 r2 与 r1, 它们的计算即使完成也不得发布。
	release(2)
	release(1)
	if st := r1.Wait(); st != SwitchSuperseded {
		t.Fatalf("r1 final = %v, want superseded", st)
	}
	if st := r2.Wait(); st != SwitchSuperseded {
		t.Fatalf("r2 final = %v, want superseded", st)
	}
	if v := s.PublishedVersion(); v != "A" {
		t.Fatalf("published version changed to %q before r3 completes", v)
	}
	// 最后放行 r3, 只有它能发布。
	release(3)
	if st := r3.Wait(); st != SwitchPublished {
		t.Fatalf("r3 final = %v, want published", st)
	}
	if v := s.PublishedVersion(); v != "A" {
		t.Fatalf("published = %q, want A", v)
	}
}

func TestSwitchUnknownVersion(t *testing.T) {
	s, err := New(switchCustomers(), switchVersions(), "A", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Switch("nope"); err != ErrUnknownVersion {
		t.Fatalf("want ErrUnknownVersion, got %v", err)
	}
	if _, err := New(switchCustomers(), switchVersions(), "nope", nil); err != ErrUnknownVersion {
		t.Fatalf("New: want ErrUnknownVersion, got %v", err)
	}
}

// TestUpdateSurvivesSwitch 验证: 重算期间的修改先在旧版本下可见, 切换发布后仍然保留。
func TestUpdateSurvivesSwitch(t *testing.T) {
	hook, release := gatedHook()
	versions := switchVersions()
	customers := switchCustomers()
	s, err := New(customers, versions, "A", &Options{RecomputeHook: hook})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Switch("B")
	if err != nil {
		t.Fatal(err)
	}
	// 重算进行期间提交修改。
	rev, err := s.ApplyUpdates([]CustomerUpdate{{ID: 1, Payoff: ptr(int64(0))}})
	if err != nil {
		t.Fatal(err)
	}
	customers[0].Payoff = 0
	// 发布前查询: 旧版本 A + 新修订号, 修改已可见。
	lb := s.Leaderboard()
	if lb.Version != "A" || lb.Revision != rev {
		t.Fatalf("during recompute: got (%s, %d), want (A, %d)", lb.Version, lb.Revision, rev)
	}
	assertEntriesEqual(t, lb.Entries, referenceLeaderboard(customers, versions["A"]))
	// 放行重算, 发布后修改必须保留。
	release(req.Seq)
	if st := req.Wait(); st != SwitchPublished {
		t.Fatalf("switch final = %v, want published", st)
	}
	lb = s.Leaderboard()
	if lb.Version != "B" || lb.Revision != rev {
		t.Fatalf("after publish: got (%s, %d), want (B, %d)", lb.Version, lb.Revision, rev)
	}
	assertEntriesEqual(t, lb.Entries, referenceLeaderboard(customers, versions["B"]))
}

// TestConcurrentQueriesConsistent 验证: 发布前后并发查询返回的每个
// (版本, 修订号) 组合都与该修订号对应客户清单的独立计算一致。
func TestConcurrentQueriesConsistent(t *testing.T) {
	hook, release := gatedHook()
	versions := switchVersions()
	base := switchCustomers()
	s, err := New(base, versions, "A", &Options{RecomputeHook: hook})
	if err != nil {
		t.Fatal(err)
	}
	// 预生成每个修订号对应的完整客户清单快照。
	const numUpdates = 40
	snapshots := make([][]Customer, numUpdates+1)
	snapshots[0] = append([]Customer(nil), base...)
	cur := append([]Customer(nil), base...)
	for i := 1; i <= numUpdates; i++ {
		idx := i % len(cur)
		cur[idx].Payoff = int64(i * 12345)
		cur[idx].Mileage = int64((i * 5432) % 200000)
		snapshots[i] = append([]Customer(nil), cur...)
	}

	req, err := s.Switch("B")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan string, 1024)
	// 并发查询: 每次查询结果必须与对应快照的独立计算一致。
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				lb := s.Leaderboard()
				if lb.Revision > numUpdates {
					errCh <- fmt.Sprintf("bad revision %d", lb.Revision)
					return
				}
				want := referenceLeaderboard(snapshots[lb.Revision], versions[lb.Version])
				if len(lb.Entries) != len(want) {
					errCh <- fmt.Sprintf("(%s,%d): len %d != %d", lb.Version, lb.Revision, len(lb.Entries), len(want))
					return
				}
				for i := range want {
					if lb.Entries[i] != want[i] {
						errCh <- fmt.Sprintf("(%s,%d) entry %d mismatch", lb.Version, lb.Revision, i)
						return
					}
				}
			}
		}()
	}
	// 单线程提交全部修改, 随后放行重算。
	for i := 1; i <= numUpdates; i++ {
		idx := i % len(base)
		if _, err := s.ApplyUpdates([]CustomerUpdate{{
			ID:      base[idx].ID,
			Payoff:  ptr(int64(i * 12345)),
			Mileage: ptr(int64((i * 5432) % 200000)),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	release(req.Seq)
	if st := req.Wait(); st != SwitchPublished {
		t.Fatalf("switch final = %v, want published", st)
	}
	// 发布后再查一段时间, 覆盖发布后的并发查询。
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
	// 最终榜单: 版本 B + 最后一个修订号。
	lb := s.Leaderboard()
	if lb.Version != "B" || lb.Revision != numUpdates {
		t.Fatalf("final: got (%s, %d), want (B, %d)", lb.Version, lb.Revision, numUpdates)
	}
	assertEntriesEqual(t, lb.Entries, referenceLeaderboard(snapshots[numUpdates], versions["B"]))
}
