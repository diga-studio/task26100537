package equityrank

import "testing"

func TestFloorDiv(t *testing.T) {
	cases := []struct {
		a, b, want int64
	}{
		{0, 10000, 0},
		{9999, 10000, 0},
		{10000, 10000, 1},
		{19999, 10000, 1},
		{-1, 10000, -1},
		{-9999, 10000, -1},
		{-10000, 10000, -1},
		{-10001, 10000, -2},
		{-20000, 10000, -2},
	}
	for _, c := range cases {
		if got := floorDiv(c.a, c.b); got != c.want {
			t.Errorf("floorDiv(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestTierOf(t *testing.T) {
	cases := []struct {
		equity int64
		want   Tier
	}{
		{150000, TierPositive},
		{150001, TierPositive},
		{149999, TierMarginal},
		{0, TierMarginal},
		{-150000, TierMarginal},
		{-150001, TierNegative},
		{-999999, TierNegative},
	}
	for _, c := range cases {
		if got := tierOf(c.equity); got != c.want {
			t.Errorf("tierOf(%d) = %v, want %v", c.equity, got, c.want)
		}
	}
}

func TestScoreOf(t *testing.T) {
	cases := []struct {
		equity int64
		past75 bool
		want   int
	}{
		{0, false, 50},
		{0, true, 60},
		{150000, false, 65},
		{-1, false, 49},      // 负数向下取整: -1/10000 -> -1
		{-10000, false, 49},  // 整除不额外减
		{-10001, false, 48},  // 越过整除点再减一
		{500000, false, 100}, // 上截断
		{490000, true, 100},  // 50+49+10=109 -> 截断到 100
		{-500000, false, 0},  // 下截断
		{-600000, true, 0},   // 加分后仍为负则截断到 0
		{-450000, true, 15},  // 50-45+10=15
	}
	for _, c := range cases {
		if got := scoreOf(c.equity, c.past75); got != c.want {
			t.Errorf("scoreOf(%d, %v) = %d, want %d", c.equity, c.past75, got, c.want)
		}
	}
}
