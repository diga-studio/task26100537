package equityrank

const (
	positiveThreshold = 150000
	negativeThreshold = -150000
	past75Bonus       = 10
	scoreBase         = 50
	scoreDivisor      = 10000
)

// floorDiv 返回 a/b 向下取整的结果, b 必须为正数。
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// tierOf 根据权益金额(分)返回分层。
func tierOf(equity int64) Tier {
	switch {
	case equity >= positiveThreshold:
		return TierPositive
	case equity < negativeThreshold:
		return TierNegative
	default:
		return TierMarginal
	}
}

// scoreOf 根据权益与期限标记计算机会分, 结果截断到 [0, 100]。
func scoreOf(equity int64, past75 bool) int {
	s := scoreBase + floorDiv(equity, scoreDivisor)
	if past75 {
		s += past75Bonus
	}
	if s < 0 {
		return 0
	}
	if s > 100 {
		return 100
	}
	return int(s)
}
