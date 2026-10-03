package dealer

import (
	"reflect"
	"testing"
)

func TestFloorDiv(t *testing.T) {
	cases := []struct {
		a, b, want int64
	}{
		{0, 10000, 0},
		{1, 10000, 0},
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
		{150001, TierPositive},
		{150000, TierPositive},
		{149999, TierBorderline},
		{0, TierBorderline},
		{-150000, TierBorderline},
		{-150001, TierNegative},
	}
	for _, c := range cases {
		if got := tierOf(c.equity); got != c.want {
			t.Errorf("tierOf(%d) = %v, want %v", c.equity, got, c.want)
		}
	}
}

func TestScoreOf(t *testing.T) {
	cases := []struct {
		equity   int64
		pastTerm bool
		want     int
	}{
		{0, false, 50},
		{9999, false, 50},
		{10000, false, 51},
		{-1, false, 49},
		{-10000, false, 49},
		{-10001, false, 48},
		{0, true, 60},
		{-10001, true, 58},
		{500000, false, 100},
		{500001, false, 100},
		{450000, true, 100}, // 50+45+10 clamps to 100
		{-500000, false, 0},
		{-500001, false, 0},
		{-550000, true, 5},
	}
	for _, c := range cases {
		if got := scoreOf(c.equity, c.pastTerm); got != c.want {
			t.Errorf("scoreOf(%d, %v) = %d, want %d", c.equity, c.pastTerm, got, c.want)
		}
	}
}

func TestComputeBoardOrderingAndTies(t *testing.T) {
	cv := compileVersion(ValuationVersion{
		ID: "v1",
		Records: []ValuationRecord{
			{ModelKey: "m", MileageMin: 0, MileageMax: 1 << 60, ValueCents: 0},
		},
	})
	customers := []Customer{
		// Same score (50), tie broken by equity desc.
		{ID: 1, ModelKey: "m", PayoffCents: -9999},
		{ID: 2, ModelKey: "m", PayoffCents: -1},
		// Same score and same equity, tie broken by customer ID asc.
		{ID: 9, ModelKey: "m", PayoffCents: -5000},
		{ID: 3, ModelKey: "m", PayoffCents: -5000},
		// Highest score wins regardless of equity.
		{ID: 4, ModelKey: "m", PayoffCents: -10000},
		// Past-term bonus lifts score above higher-equity customer.
		{ID: 5, ModelKey: "m", PayoffCents: -1, PastTerm75: true},
	}
	got := computeBoard(customers, cv)
	wantIDs := []int64{5, 4, 1, 3, 9, 2}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d entries, want %d", len(got), len(wantIDs))
	}
	for i, id := range wantIDs {
		if got[i].CustomerID != id {
			t.Fatalf("entry %d: got customer %d, want %d (full board: %+v)", i, got[i].CustomerID, id, got)
		}
	}
}

func TestComputeBoardTop50(t *testing.T) {
	cv := compileVersion(ValuationVersion{
		ID: "v1",
		Records: []ValuationRecord{
			{ModelKey: "m", MileageMin: 0, MileageMax: 1 << 60, ValueCents: 0},
		},
	})
	customers := make([]Customer, 0, 60)
	for i := 0; i < 60; i++ {
		customers = append(customers, Customer{
			ID:          int64(i + 1),
			ModelKey:    "m",
			PayoffCents: int64(i), // higher ID => lower equity
		})
	}
	got := computeBoard(customers, cv)
	if len(got) != boardSize {
		t.Fatalf("got %d entries, want %d", len(got), boardSize)
	}
	for i, e := range got {
		if e.CustomerID != int64(i+1) {
			t.Fatalf("entry %d: got customer %d, want %d", i, e.CustomerID, i+1)
		}
	}
}

func TestCompiledVersionLookup(t *testing.T) {
	cv := compileVersion(ValuationVersion{
		ID: "v1",
		Records: []ValuationRecord{
			{ModelKey: "a", MileageMin: 0, MileageMax: 10000, ValueCents: 100},
			{ModelKey: "a", MileageMin: 10000, MileageMax: 20000, ValueCents: 200},
			{ModelKey: "b", MileageMin: 0, MileageMax: 5000, ValueCents: 300},
		},
	})
	cases := []struct {
		model   string
		mileage int64
		want    int64
		ok      bool
	}{
		{"a", 0, 100, true},
		{"a", 9999, 100, true},
		{"a", 10000, 200, true}, // left-closed, right-open
		{"a", 19999, 200, true},
		{"a", 20000, 0, false},
		{"b", 4999, 300, true},
		{"c", 0, 0, false},
	}
	for _, c := range cases {
		got, ok := cv.lookup(c.model, c.mileage)
		if got != c.want || ok != c.ok {
			t.Errorf("lookup(%q, %d) = (%d, %v), want (%d, %v)",
				c.model, c.mileage, got, ok, c.want, c.ok)
		}
	}
}

func TestComputeBoardMatchesSpec(t *testing.T) {
	cv := compileVersion(ValuationVersion{
		ID: "v1",
		Records: []ValuationRecord{
			{ModelKey: "sedan", MileageMin: 0, MileageMax: 50000, ValueCents: 1200000},
			{ModelKey: "sedan", MileageMin: 50000, MileageMax: 1 << 60, ValueCents: 800000},
		},
	})
	customers := []Customer{
		{ID: 1, ModelKey: "sedan", Mileage: 10000, PayoffCents: 1050000},                  // equity 150000 => positive
		{ID: 2, ModelKey: "sedan", Mileage: 60000, PayoffCents: 950001, PastTerm75: true}, // equity -150001 => negative
	}
	got := computeBoard(customers, cv)
	want := []Entry{
		{CustomerID: 1, EquityCents: 150000, Tier: TierPositive, Score: 65},
		{CustomerID: 2, EquityCents: -150001, Tier: TierNegative, Score: 44},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
