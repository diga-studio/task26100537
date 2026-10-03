package equityrank

import "sort"

// valuationIndex 是一份估值版本的查询索引, 按车型键分组, 区间按起点排序。
type valuationIndex struct {
	byModel map[string][]ValuationRecord
}

// buildValuationIndex 由一份估值记录构建索引。
func buildValuationIndex(records []ValuationRecord) *valuationIndex {
	ix := &valuationIndex{byModel: make(map[string][]ValuationRecord)}
	for _, r := range records {
		ix.byModel[r.ModelKey] = append(ix.byModel[r.ModelKey], r)
	}
	for _, recs := range ix.byModel {
		sort.Slice(recs, func(i, j int) bool { return recs[i].MileageLo < recs[j].MileageLo })
	}
	return ix
}

// lookup 返回车型在指定里程下的估值(分), 输入保证唯一命中。
func (ix *valuationIndex) lookup(modelKey string, mileage int64) (int64, bool) {
	recs := ix.byModel[modelKey]
	i := sort.Search(len(recs), func(i int) bool { return recs[i].MileageLo > mileage }) - 1
	if i < 0 || mileage >= recs[i].MileageHi {
		return 0, false
	}
	return recs[i].Value, true
}
