package equityrank

import (
	"errors"
	"sort"
	"sync"
)

// ErrUnknownCustomer 表示修改了不存在的客户。
var ErrUnknownCustomer = errors.New("equityrank: unknown customer")

// ErrUnknownVersion 表示切换到不存在的估值版本。
var ErrUnknownVersion = errors.New("equityrank: unknown valuation version")

// Options 是 Store 的可选配置。
type Options struct {
	// RecomputeHook 若不为 nil, 会在后台重算期间、发布之前被调用。
	// 仅用于测试, 可借此控制不同切换请求的完成顺序。
	RecomputeHook func(seq uint64, version string)
}

// Store 保存全部进程内状态, 提供客户修改、版本切换与排行榜查询。
// 所有方法均并发安全。
type Store struct {
	mu          sync.RWMutex
	options     Options
	customers   map[int64]*customerState
	customerIDs []int64
	revision    uint64
	records     map[string][]ValuationRecord
	indexes     map[string]*valuationIndex
	published   string
	publishedIx *valuationIndex
	latestSeq   uint64
	pending     map[uint64]*SwitchRequest
}

type customerState struct {
	id       int64
	modelKey string
	mileage  int64
	payoff   int64
	past75   bool
}

// New 以客户清单、若干不可变估值版本和初始生效版本创建 Store。
func New(customers []Customer, versions map[string][]ValuationRecord, initialVersion string, opts *Options) (*Store, error) {
	recs, ok := versions[initialVersion]
	if !ok {
		return nil, ErrUnknownVersion
	}
	s := &Store{
		customers: make(map[int64]*customerState, len(customers)),
		records:   versions,
		indexes:   map[string]*valuationIndex{initialVersion: buildValuationIndex(recs)},
		published: initialVersion,
		pending:   make(map[uint64]*SwitchRequest),
	}
	if opts != nil {
		s.options = *opts
	}
	for _, c := range customers {
		s.customers[c.ID] = &customerState{
			id:       c.ID,
			modelKey: c.ModelKey,
			mileage:  c.Mileage,
			payoff:   c.Payoff,
			past75:   c.Past75,
		}
		s.customerIDs = append(s.customerIDs, c.ID)
	}
	s.publishedIx = s.indexes[initialVersion]
	return s, nil
}

// ApplyUpdates 将一批客户修改作为整体提交, 返回新的客户数据修订号。
// 任一修改指向不存在的客户时, 整批不生效。
func (s *Store) ApplyUpdates(updates []CustomerUpdate) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range updates {
		if _, ok := s.customers[u.ID]; !ok {
			return s.revision, ErrUnknownCustomer
		}
	}
	for _, u := range updates {
		c := s.customers[u.ID]
		if u.Mileage != nil {
			c.mileage = *u.Mileage
		}
		if u.Payoff != nil {
			c.payoff = *u.Payoff
		}
		if u.Past75 != nil {
			c.past75 = *u.Past75
		}
	}
	s.revision++
	return s.revision, nil
}

// Switch 受理一次估值版本切换请求, 立即返回句柄, 后台异步重算并发布。
// 后受理的请求会取代此前尚未发布的请求。
func (s *Store) Switch(version string) (*SwitchRequest, error) {
	s.mu.Lock()
	if _, ok := s.records[version]; !ok {
		s.mu.Unlock()
		return nil, ErrUnknownVersion
	}
	s.latestSeq++
	req := &SwitchRequest{Seq: s.latestSeq, Version: version, done: make(chan struct{})}
	for seq, p := range s.pending {
		p.finish(SwitchSuperseded)
		delete(s.pending, seq)
	}
	s.pending[req.Seq] = req
	s.mu.Unlock()
	go s.recompute(req)
	return req, nil
}

// recompute 在后台为切换请求构建估值索引, 完成后尝试发布。
// 若请求已被后受理的请求取代, 计算结果直接丢弃。
func (s *Store) recompute(req *SwitchRequest) {
	s.mu.RLock()
	ix, cached := s.indexes[req.Version]
	s.mu.RUnlock()
	if !cached {
		ix = buildValuationIndex(s.records[req.Version])
	}
	if s.options.RecomputeHook != nil {
		s.options.RecomputeHook(req.Seq, req.Version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[req.Seq]; !ok {
		return // 已被取代
	}
	delete(s.pending, req.Seq)
	if !cached {
		s.indexes[req.Version] = ix
	}
	s.published = req.Version
	s.publishedIx = ix
	req.finish(SwitchPublished)
}

// Leaderboard 返回当前已发布版本下、当前客户数据修订号对应的前 50 名。
func (s *Store) Leaderboard() Leaderboard {
	s.mu.RLock()
	version, ix, revision := s.published, s.publishedIx, s.revision
	snap := make([]customerState, len(s.customerIDs))
	for i, id := range s.customerIDs {
		snap[i] = *s.customers[id]
	}
	s.mu.RUnlock()
	// 快照与 (版本, 修订号) 在同一临界区内捕获, 之后的计算不再持锁,
	// 结果等价于该时刻用此版本与修订号对应的完整客户清单从头计算。
	entries := make([]Entry, 0, len(snap))
	for _, c := range snap {
		value, ok := ix.lookup(c.modelKey, c.mileage)
		if !ok {
			continue
		}
		equity := value - c.payoff
		entries = append(entries, Entry{
			CustomerID: c.id,
			Equity:     equity,
			Tier:       tierOf(equity),
			Score:      scoreOf(equity, c.past75),
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
	return Leaderboard{Version: version, Revision: revision, Entries: entries}
}

// Revision 返回当前客户数据修订号。
func (s *Store) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// PublishedVersion 返回当前已发布的估值版本。
func (s *Store) PublishedVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.published
}
