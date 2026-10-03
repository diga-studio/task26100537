// Package dealer implements an in-memory equity opportunity leaderboard
// for a car dealership. Sales staff locate trade-in prospects by comparing
// vehicle valuations against loan payoff amounts.
package dealer

// Tier classifies a customer's equity.
type Tier int

const (
	// TierNegative: equity below -150000 cents.
	TierNegative Tier = iota
	// TierBorderline: equity in [-150000, 150000) cents.
	TierBorderline
	// TierPositive: equity at or above 150000 cents.
	TierPositive
)

func (t Tier) String() string {
	switch t {
	case TierNegative:
		return "negative"
	case TierBorderline:
		return "borderline"
	case TierPositive:
		return "positive"
	}
	return "unknown"
}

// Customer is a dealership customer record. PayoffCents and PastTerm75 are
// supplied by upstream systems; this library never computes loan schedules.
type Customer struct {
	ID          int64
	ModelKey    string
	Mileage     int64
	PayoffCents int64
	PastTerm75  bool
}

// CustomerUpdate modifies an existing customer. Nil fields are left
// unchanged. Only existing customers can be updated; this library does not
// handle additions or removals.
type CustomerUpdate struct {
	ID          int64
	Mileage     *int64
	PayoffCents *int64
	PastTerm75  *bool
}

// ValuationRecord values one model within the half-open mileage interval
// [MileageMin, MileageMax). Input guarantees a unique hit per lookup.
type ValuationRecord struct {
	ModelKey   string
	MileageMin int64
	MileageMax int64
	ValueCents int64
}

// ValuationVersion is an immutable valuation table.
type ValuationVersion struct {
	ID      string
	Records []ValuationRecord
}

// Entry is one row of the leaderboard.
type Entry struct {
	CustomerID  int64
	EquityCents int64
	Tier        Tier
	Score       int
}

// Board is a leaderboard snapshot. It is always equivalent to a from-scratch
// computation over the full customer list using Version at the customer data
// revision Revision.
type Board struct {
	Version  string
	Revision int64
	Entries  []Entry
}

// SwitchStatus is the terminal state of a version switch request.
type SwitchStatus int

const (
	// SwitchPublished: the request's version became the published version.
	SwitchPublished SwitchStatus = iota
	// SwitchSuperseded: a later accepted request replaced this one before
	// it could be published.
	SwitchSuperseded
)

func (s SwitchStatus) String() string {
	switch s {
	case SwitchPublished:
		return "published"
	case SwitchSuperseded:
		return "superseded"
	}
	return "unknown"
}

// SwitchOutcome reports how a switch request ended.
type SwitchOutcome struct {
	Status SwitchStatus
	// Version is the requested version.
	Version string
	// Revision is the customer data revision at publish time. It is only
	// meaningful when Status is SwitchPublished.
	Revision int64
}
