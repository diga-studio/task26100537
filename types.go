package equityrank

import "sync/atomic"

// Tier 表示权益分层。
type Tier int

const (
	TierNegative Tier = iota // 负权益: equity < -150000
	TierMarginal             // 临界权益: -150000 <= equity < 150000
	TierPositive             // 正权益: equity >= 150000
)

// Customer 是初始化时提供的客户数据。金额单位为整数分。
type Customer struct {
	ID       int64
	ModelKey string
	Mileage  int64
	Payoff   int64 // 贷款结清金额(分)
	Past75   bool  // 是否已超过贷款期限 75%
}

// CustomerUpdate 描述对已有客户的部分修改, nil 字段表示不修改。
type CustomerUpdate struct {
	ID      int64
	Mileage *int64
	Payoff  *int64
	Past75  *bool
}

// ValuationRecord 是一条估值记录: 车型在里程区间 [MileageLo, MileageHi) 内价值 Value 分。
type ValuationRecord struct {
	ModelKey  string
	MileageLo int64
	MileageHi int64
	Value     int64
}

// Entry 是排行榜中的一项。
type Entry struct {
	CustomerID int64
	Equity     int64
	Tier       Tier
	Score      int
}

// Leaderboard 是一次排行榜查询的结果。
type Leaderboard struct {
	Version  string // 计算所用的估值版本
	Revision uint64 // 计算所用的客户数据修订号
	Entries  []Entry
}

// SwitchStatus 表示一次切换请求的最终结果。
type SwitchStatus int

const (
	SwitchPending SwitchStatus = iota
	SwitchPublished
	SwitchSuperseded
)

// SwitchRequest 是一次版本切换请求的句柄。
type SwitchRequest struct {
	Seq     uint64
	Version string

	done   chan struct{}
	status atomic.Int32
}

// Wait 阻塞直到该请求最终发布或被取代, 返回最终状态。
func (r *SwitchRequest) Wait() SwitchStatus {
	<-r.done
	return SwitchStatus(r.status.Load())
}

// Status 返回当前状态, 不阻塞。
func (r *SwitchRequest) Status() SwitchStatus {
	return SwitchStatus(r.status.Load())
}

// finish 结束请求, 只能由 Store 在持有锁时恰好调用一次。
func (r *SwitchRequest) finish(st SwitchStatus) {
	r.status.Store(int32(st))
	close(r.done)
}
