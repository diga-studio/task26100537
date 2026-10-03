package dealer

import (
	"reflect"
	"sort"
	"testing"
)

// independentBoard recomputes the expected leaderboard from scratch using a
// deliberately naive implementation (linear record scan, explicit
// comparator) to cross-check the library's optimized path.
func independentBoard(customers []Customer, v ValuationVersion) []Entry {
	entries := make([]Entry, 0, len(customers))
	for _, c := range customers {
		var value int64
		found := false
		for _, r := range v.Records {
			if r.ModelKey == c.ModelKey && c.Mileage >= r.MileageMin && c.Mileage < r.MileageMax {
				value = r.ValueCents
				found = true
				break
			}
		}
		if !found {
			continue
		}
		equity := value - c.PayoffCents
		var tier Tier
		switch {
		case equity >= 150000:
			tier = TierPositive
		case equity < -150000:
			tier = TierNegative
		default:
			tier = TierBorderline
		}
		score := 50 + equity/10000
		if equity < 0 && equity%10000 != 0 {
			score--
		}
		if c.PastTerm75 {
			score += 10
		}
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		entries = append(entries, Entry{
			CustomerID:  c.ID,
			EquityCents: equity,
			Tier:        tier,
			Score:       int(score),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.EquityCents != b.EquityCents {
			return a.EquityCents > b.EquityCents
		}
		return a.CustomerID < b.CustomerID
	})
	if len(entries) > 50 {
		entries = entries[:50]
	}
	return entries
}

func testVersions() []ValuationVersion {
	return []ValuationVersion{
		{
			ID: "v1",
			Records: []ValuationRecord{
				{ModelKey: "sedan", MileageMin: 0, MileageMax: 50000, ValueCents: 1200000},
				{ModelKey: "sedan", MileageMin: 50000, MileageMax: 1 << 60, ValueCents: 800000},
				{ModelKey: "suv", MileageMin: 0, MileageMax: 1 << 60, ValueCents: 2000000},
			},
		},
		{
			ID: "v2",
			Records: []ValuationRecord{
				{ModelKey: "sedan", MileageMin: 0, MileageMax: 50000, ValueCents: 900000},
				{ModelKey: "sedan", MileageMin: 50000, MileageMax: 1 << 60, ValueCents: 500000},
				{ModelKey: "suv", MileageMin: 0, MileageMax: 1 << 60, ValueCents: 2100000},
			},
		},
	}
}

func testCustomers() []Customer {
	return []Customer{
		{ID: 1, ModelKey: "sedan", Mileage: 10000, PayoffCents: 1050000},
		{ID: 2, ModelKey: "sedan", Mileage: 60000, PayoffCents: 950001, PastTerm75: true},
		{ID: 3, ModelKey: "suv", Mileage: 5000, PayoffCents: 1800000},
		{ID: 4, ModelKey: "sedan", Mileage: 30000, PayoffCents: 1100000},
	}
}

func TestNewAndQuery(t *testing.T) {
	versions := testVersions()
	customers := testCustomers()
	l, err := New(customers, versions, "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	b := l.Query()
	if b.Version != "v1" || b.Revision != 0 {
		t.Fatalf("got version=%q revision=%d, want v1/0", b.Version, b.Revision)
	}
	want := independentBoard(customers, versions[0])
	if !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("entries mismatch:\n got %+v\nwant %+v", b.Entries, want)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(testCustomers(), testVersions(), "nope"); err != ErrUnknownVersion {
		t.Errorf("unknown initial version: got %v", err)
	}
	dup := []Customer{{ID: 1, ModelKey: "sedan"}, {ID: 1, ModelKey: "sedan"}}
	if _, err := New(dup, testVersions(), "v1"); err != ErrDuplicateCustomer {
		t.Errorf("duplicate customer: got %v", err)
	}
	vs := testVersions()
	vs[1].ID = "v1"
	if _, err := New(testCustomers(), vs, "v1"); err != ErrDuplicateVersion {
		t.Errorf("duplicate version: got %v", err)
	}
	bad := []ValuationVersion{{ID: "v1", Records: []ValuationRecord{
		{ModelKey: "sedan", MileageMin: 0, MileageMax: 100, ValueCents: 1},
	}}}
	if _, err := New(testCustomers(), bad, "v1"); err != ErrNoValuation {
		t.Errorf("unpriceable customer: got %v", err)
	}
}

func TestModify(t *testing.T) {
	versions := testVersions()
	customers := testCustomers()
	l, err := New(customers, versions, "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	mileage := int64(45000)
	payoff := int64(100000)
	past := true
	rev, err := l.Modify([]CustomerUpdate{
		{ID: 2, Mileage: &mileage, PayoffCents: &payoff, PastTerm75: &past},
		{ID: 3, PayoffCents: &payoff},
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}
	if rev != 1 {
		t.Fatalf("got revision %d, want 1", rev)
	}

	updated := testCustomers()
	updated[1].Mileage = 45000
	updated[1].PayoffCents = 100000
	updated[1].PastTerm75 = true
	updated[2].PayoffCents = 100000
	b := l.Query()
	if b.Revision != 1 {
		t.Fatalf("query revision = %d, want 1", b.Revision)
	}
	want := independentBoard(updated, versions[0])
	if !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("entries mismatch:\n got %+v\nwant %+v", b.Entries, want)
	}
}

func TestModifyAtomicity(t *testing.T) {
	l, err := New(testCustomers(), testVersions(), "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	payoff := int64(1)
	_, err = l.Modify([]CustomerUpdate{
		{ID: 1, PayoffCents: &payoff},
		{ID: 999, PayoffCents: &payoff},
	})
	if err != ErrUnknownCustomer {
		t.Fatalf("got %v, want ErrUnknownCustomer", err)
	}
	b := l.Query()
	if b.Revision != 0 {
		t.Fatalf("failed batch changed revision to %d", b.Revision)
	}
	want := independentBoard(testCustomers(), testVersions()[0])
	if !reflect.DeepEqual(b.Entries, want) {
		t.Fatalf("failed batch changed board:\n got %+v\nwant %+v", b.Entries, want)
	}
}

func TestModifyUnknownAndClosed(t *testing.T) {
	l, err := New(testCustomers(), testVersions(), "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payoff := int64(1)
	if _, err := l.Modify([]CustomerUpdate{{ID: 42, PayoffCents: &payoff}}); err != ErrUnknownCustomer {
		t.Fatalf("got %v, want ErrUnknownCustomer", err)
	}
	l.Close()
	if _, err := l.Modify([]CustomerUpdate{{ID: 1, PayoffCents: &payoff}}); err != ErrClosed {
		t.Fatalf("got %v, want ErrClosed", err)
	}
	if _, err := l.Switch("v2"); err != ErrClosed {
		t.Fatalf("got %v, want ErrClosed", err)
	}
	l.Close() // idempotent
}
