package dealer

import (
	"container/heap"
	"sort"
)

// tierThresholdCents is the absolute equity boundary between tiers.
const tierThresholdCents int64 = 150000

// boardSize is the maximum number of entries returned by a query.
const boardSize = 50

// interval is one half-open mileage band [min, max) with a fixed value.
type interval struct {
	min   int64
	max   int64
	value int64
}

// compiledVersion is a valuation table indexed for fast lookup.
type compiledVersion struct {
	id     string
	models map[string][]interval
}

// compileVersion indexes records by model key with intervals sorted by min.
func compileVersion(v ValuationVersion) *compiledVersion {
	models := make(map[string][]interval)
	for _, r := range v.Records {
		models[r.ModelKey] = append(models[r.ModelKey], interval{
			min:   r.MileageMin,
			max:   r.MileageMax,
			value: r.ValueCents,
		})
	}
	for _, ivs := range models {
		sort.Slice(ivs, func(i, j int) bool { return ivs[i].min < ivs[j].min })
	}
	return &compiledVersion{id: v.ID, models: models}
}

// lookup returns the vehicle value for the given model and mileage.
func (cv *compiledVersion) lookup(model string, mileage int64) (int64, bool) {
	ivs, ok := cv.models[model]
	if !ok {
		return 0, false
	}
	i := sort.Search(len(ivs), func(i int) bool { return ivs[i].min > mileage }) - 1
	if i < 0 || mileage >= ivs[i].max {
		return 0, false
	}
	return ivs[i].value, true
}

// floorDiv returns floor(a/b) for b > 0, rounding toward negative infinity.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// tierOf classifies equity into negative, borderline, or positive.
func tierOf(equity int64) Tier {
	switch {
	case equity >= tierThresholdCents:
		return TierPositive
	case equity < -tierThresholdCents:
		return TierNegative
	default:
		return TierBorderline
	}
}

// scoreOf computes the opportunity score: 50 + floor(equity/10000), plus 10
// when past 75% of the loan term, clamped to [0, 100].
func scoreOf(equity int64, pastTerm bool) int {
	s := 50 + floorDiv(equity, 10000)
	if pastTerm {
		s += 10
	}
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	return int(s)
}

// computeBoard scores every customer and returns the top boardSize entries
// ordered by score desc, equity desc, customer ID asc.
func computeBoard(customers []Customer, cv *compiledVersion) []Entry {
	top := make(entryHeap, 0, boardSize)
	for _, c := range customers {
		value, ok := cv.lookup(c.ModelKey, c.Mileage)
		if !ok {
			continue
		}
		equity := value - c.PayoffCents
		e := Entry{
			CustomerID:  c.ID,
			EquityCents: equity,
			Tier:        tierOf(equity),
			Score:       scoreOf(equity, c.PastTerm75),
		}
		if len(top) < boardSize {
			heap.Push(&top, e)
		} else if better(e, top[0]) {
			top[0] = e
			heap.Fix(&top, 0)
		}
	}
	sort.Slice(top, func(i, j int) bool { return better(top[i], top[j]) })
	return top
}

// better reports whether a ranks ahead of b: score desc, equity desc,
// customer ID asc.
func better(a, b Entry) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.EquityCents != b.EquityCents {
		return a.EquityCents > b.EquityCents
	}
	return a.CustomerID < b.CustomerID
}

// entryHeap is a min-heap of entries ordered by better; the root is the
// worst entry currently selected for the board.
type entryHeap []Entry

func (h entryHeap) Len() int            { return len(h) }
func (h entryHeap) Less(i, j int) bool  { return better(h[j], h[i]) }
func (h entryHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *entryHeap) Push(x interface{}) { *h = append(*h, x.(Entry)) }
func (h *entryHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
}
