// Package plan holds TripPlan v2: the single source of truth about a trip. The
// agent decides by it, the frontend renders it as is, eval compares against it.
// The JSON shape is the TripPlan schema of api/openapi.yaml; a test in this
// package checks that they match.
package plan

import (
	"encoding/json"
	"fmt"
	"time"
)

// Stages from the case, in order.
const (
	StageClarify      = "clarify"
	StageInspire      = "inspire"
	StageSearch       = "search"
	StageCompare      = "compare"
	StageAssemble     = "assemble"
	StageCheckout     = "checkout"
	StagePostPurchase = "post_purchase"
)

// Component names.
const (
	Outbound          = "outbound"
	Stay              = "stay"
	Return            = "return"
	TransferArrival   = "transfer_arrival"
	TransferDeparture = "transfer_departure"
)

// ComponentNames lists the components in trip order.
var ComponentNames = []string{Outbound, TransferArrival, Stay, TransferDeparture, Return}

// Option kinds.
const (
	KindStay     = "stay"
	KindJourney  = "journey"
	KindTransfer = "transfer"
)

// Option flags.
const (
	FlagOverBudget          = "over_budget"
	FlagSelectedUnavailable = "selected_unavailable"
)

type TripPlan struct {
	SessionID          string              `json:"session_id"`
	Version            int                 `json:"version"`
	Stage              string              `json:"stage"`
	Constraints        Constraints         `json:"constraints"`
	Assumptions        []Assumption        `json:"assumptions"`
	DestinationOptions []DestinationOption `json:"destination_options"`
	Components         Components          `json:"components"`
	Weather            *Weather            `json:"weather,omitempty"`
	Checks             []Check             `json:"checks"`
	Conflicts          []Conflict          `json:"conflicts"`
	TotalRub           int64               `json:"total_rub"`
	OverBudget         bool                `json:"over_budget"`

	// stale marks components whose options must be re-checked with the
	// supplier in this turn. It lives only within a turn and is not stored.
	stale map[string]bool
}

type Constraints struct {
	Origin      string      `json:"origin,omitempty"`
	Destination string      `json:"destination,omitempty"`
	Dates       Dates       `json:"dates"`
	Adults      int32       `json:"adults,omitempty"`
	ChildAges   []int32     `json:"child_ages"`
	Pets        bool        `json:"pets"`
	Budget      *Budget     `json:"budget,omitempty"`
	Preferences Preferences `json:"preferences"`
}

// Dates are given as depart and return or as depart and nights; the plan keeps
// all three consistent once depart is known.
type Dates struct {
	Depart   string `json:"depart,omitempty"`
	Return   string `json:"return,omitempty"`
	Nights   int32  `json:"nights,omitempty"`
	FlexDays int32  `json:"flex_days,omitempty"`
}

type Budget struct {
	AmountRub int64 `json:"amount_rub"`
	Strict    bool  `json:"strict"`
}

type Preferences struct {
	Meal             string   `json:"meal,omitempty"`
	FreeCancellation *bool    `json:"free_cancellation,omitempty"`
	TransportModes   []string `json:"transport_modes"`
	MaxTravelHours   float64  `json:"max_travel_hours,omitempty"`
	Moods            []string `json:"moods"`
}

// Assumption is something the agent decided on its own; the user sees and can correct it.
type Assumption struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

type Explanation struct {
	Why       string   `json:"why"`
	Tradeoffs []string `json:"tradeoffs"`
	Risks     []string `json:"risks"`
}

type DestinationOption struct {
	Destination Destination  `json:"destination"`
	Explanation *Explanation `json:"explanation,omitempty"`
}

type Components struct {
	Outbound          Component `json:"outbound"`
	Stay              Component `json:"stay"`
	Return            Component `json:"return"`
	TransferArrival   Component `json:"transfer_arrival"`
	TransferDeparture Component `json:"transfer_departure"`
}

// Get returns the component by name, nil for an unknown name.
func (c *Components) Get(name string) *Component {
	switch name {
	case Outbound:
		return &c.Outbound
	case Stay:
		return &c.Stay
	case Return:
		return &c.Return
	case TransferArrival:
		return &c.TransferArrival
	case TransferDeparture:
		return &c.TransferDeparture
	}
	return nil
}

type Component struct {
	Options    []Option `json:"options"`
	SelectedID string   `json:"selected_id,omitempty"`
}

// Selected returns the selected option, nil when nothing is selected or the
// selection is no longer among the options.
func (c *Component) Selected() *Option {
	if c.SelectedID == "" {
		return nil
	}
	for i := range c.Options {
		if c.Options[i].ID == c.SelectedID {
			return &c.Options[i]
		}
	}
	return nil
}

// Option is one variant of a component. Exactly one of Stay, Journey and
// Transfer is set, matching Kind; in JSON it is the "offer" field.
type Option struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind"`
	PriceRub    int64        `json:"price_rub"`
	Explanation *Explanation `json:"explanation,omitempty"`
	Flags       []string     `json:"flags"`
	Sources     []Source     `json:"sources"`

	Stay     *Hotel    `json:"-"`
	Journey  *Journey  `json:"-"`
	Transfer *Transfer `json:"-"`
}

type optionJSON struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Offer       json.RawMessage `json:"offer"`
	PriceRub    int64           `json:"price_rub"`
	Explanation *Explanation    `json:"explanation,omitempty"`
	Flags       []string        `json:"flags"`
	Sources     []Source        `json:"sources"`
}

func (o Option) MarshalJSON() ([]byte, error) {
	var offer any
	switch o.Kind {
	case KindStay:
		offer = o.Stay
	case KindJourney:
		offer = o.Journey
	case KindTransfer:
		offer = o.Transfer
	default:
		return nil, fmt.Errorf("option %q: unknown kind %q", o.ID, o.Kind)
	}
	raw, err := json.Marshal(offer)
	if err != nil {
		return nil, err
	}
	return json.Marshal(optionJSON{
		ID: o.ID, Kind: o.Kind, Offer: raw, PriceRub: o.PriceRub, Explanation: o.Explanation,
		Flags: nonNil(o.Flags), Sources: nonNil(o.Sources),
	})
}

func (o *Option) UnmarshalJSON(data []byte) error {
	var j optionJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	*o = Option{ID: j.ID, Kind: j.Kind, PriceRub: j.PriceRub, Explanation: j.Explanation, Flags: j.Flags, Sources: j.Sources}
	switch j.Kind {
	case KindStay:
		return json.Unmarshal(j.Offer, &o.Stay)
	case KindJourney:
		return json.Unmarshal(j.Offer, &o.Journey)
	case KindTransfer:
		return json.Unmarshal(j.Offer, &o.Transfer)
	}
	return fmt.Errorf("option %q: unknown kind %q", j.ID, j.Kind)
}

func (o *Option) hasFlag(flag string) bool {
	for _, f := range o.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

func (o *Option) setFlag(flag string, on bool) {
	kept := o.Flags[:0:0]
	for _, f := range o.Flags {
		if f != flag {
			kept = append(kept, f)
		}
	}
	if on {
		kept = append(kept, flag)
	}
	o.Flags = kept
}

// Check is a connection check between components.
type Check struct {
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Message    string   `json:"message"`
	Components []string `json:"components"`
}

// Conflict is a broken constraint or connection the agent must tell the user about.
type Conflict struct {
	Kind       string   `json:"kind"`
	Message    string   `json:"message"`
	Components []string `json:"components"`
}

// New returns an empty plan for a new session.
func New(sessionID string) TripPlan {
	p := TripPlan{SessionID: sessionID, Stage: StageClarify}
	p.normalize()
	return p
}

// MarshalJSON writes empty lists as [] rather than null, as the schema requires.
func (p TripPlan) MarshalJSON() ([]byte, error) {
	p.normalize()
	type plain TripPlan
	return json.Marshal(plain(p))
}

func (p *TripPlan) normalize() {
	c := &p.Constraints
	c.ChildAges = nonNil(c.ChildAges)
	c.Preferences.TransportModes = nonNil(c.Preferences.TransportModes)
	c.Preferences.Moods = nonNil(c.Preferences.Moods)
	p.Assumptions = nonNil(p.Assumptions)
	p.DestinationOptions = nonNil(p.DestinationOptions)
	for _, name := range ComponentNames {
		comp := p.Components.Get(name)
		comp.Options = nonNil(comp.Options)
	}
	p.Checks = nonNil(p.Checks)
	p.Conflicts = nonNil(p.Conflicts)
	for i := range p.Conflicts {
		p.Conflicts[i].Components = nonNil(p.Conflicts[i].Components)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// departDate parses the depart date; ok is false when it is unset.
func (d Dates) departDate() (time.Time, bool) {
	t, err := time.Parse(time.DateOnly, d.Depart)
	return t, err == nil
}
