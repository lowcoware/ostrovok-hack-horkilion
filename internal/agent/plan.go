// Package agent runs agent turns: it routes a user message into a patch of the
// TripPlan, invalidates what the patch affects, moves the plan through stage
// gates, calls inventory and streams the reply.
package agent

import (
	"fmt"
	"time"

	inventoryv1 "horkilion/gen/inventory/v1"
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

// Defaults used when the user did not say. Each one becomes a visible assumption.
const (
	defaultLeadDays = 14
	defaultNights   = 3
	defaultAdults   = 2
)

// TripPlan is the single source of truth about the trip. Chat history only
// sets the tone; decisions are made from the plan. The frontend renders it as is.
type TripPlan struct {
	SessionID   string        `json:"session_id"`
	Version     int           `json:"version"`
	Stage       string        `json:"stage"`
	Constraints Constraints   `json:"constraints"`
	Assumptions []Assumption  `json:"assumptions"`
	Options     []HotelOption `json:"options"`
	Selected    *HotelOption  `json:"selected,omitempty"`
	Weather     *Weather      `json:"weather,omitempty"`
	Conflicts   []Conflict    `json:"conflicts"`
}

type Constraints struct {
	Destination string `json:"destination,omitempty"`
	CheckIn     string `json:"check_in,omitempty"`
	Nights      int32  `json:"nights,omitempty"`
	Adults      int32  `json:"adults,omitempty"`
	Children    int32  `json:"children,omitempty"`
	Pets        bool   `json:"pets,omitempty"`
	BudgetRub   int64  `json:"budget_rub,omitempty"`
}

// Assumption is something the agent decided on its own; the user sees and can correct it.
type Assumption struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// Conflict is a broken constraint or connection the agent must tell the user about.
type Conflict struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type HotelOption struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	City               string  `json:"city"`
	Stars              int32   `json:"stars"`
	Rating             float64 `json:"rating"`
	Meal               string  `json:"meal"`
	FreeCancellation   bool    `json:"free_cancellation"`
	PetsAllowed        bool    `json:"pets_allowed"`
	PricePerNightRub   int64   `json:"price_per_night_rub"`
	TotalPriceRub      int64   `json:"total_price_rub"`
	DistanceToCenterKm float64 `json:"distance_to_center_km"`
	OverBudget         bool    `json:"over_budget,omitempty"`
	Synthetic          bool    `json:"synthetic"`
}

type Weather struct {
	City            string  `json:"city"`
	Date            string  `json:"date"`
	TempMaxC        float64 `json:"temp_max_c"`
	TempMinC        float64 `json:"temp_min_c"`
	PrecipitationMM float64 `json:"precipitation_mm"`
	Source          string  `json:"source"`
}

// Patch is the change a user message makes to the constraints. Nil means "not mentioned".
type Patch struct {
	Destination *string `json:"destination"`
	CheckIn     *string `json:"check_in"`
	Nights      *int32  `json:"nights"`
	Adults      *int32  `json:"adults"`
	Children    *int32  `json:"children"`
	Pets        *bool   `json:"pets"`
	BudgetRub   *int64  `json:"budget_rub"`
}

func NewPlan(sessionID string) TripPlan {
	return TripPlan{SessionID: sessionID, Stage: StageClarify, Assumptions: []Assumption{}, Options: []HotelOption{}, Conflicts: []Conflict{}}
}

// Apply merges a patch into the constraints and invalidates exactly what the
// change makes stale. Untouched parts of the plan stay as they were: this is
// how the plan is rebuilt without searching from scratch. It returns what was
// invalidated, for the reply and the logs.
func (p *TripPlan) Apply(patch Patch, today time.Time) []string {
	c := &p.Constraints
	var invalidated []string
	stayChanged := false

	if v := patch.Destination; v != nil && *v != "" && *v != c.Destination {
		if c.Destination != "" {
			invalidated = append(invalidated, "направление изменилось: варианты, выбор и погода сброшены")
		}
		c.Destination = *v
		p.Options, p.Selected, p.Weather = []HotelOption{}, nil, nil
	}
	if v := patch.CheckIn; v != nil && *v != c.CheckIn {
		if d, err := time.Parse(time.DateOnly, *v); err == nil && !d.Before(today) {
			c.CheckIn = *v
			p.Weather = nil
			p.dropAssumption("check_in")
			stayChanged = true
		}
	}
	if v := patch.Nights; v != nil && *v >= 1 && *v <= 30 && *v != c.Nights {
		c.Nights = *v
		p.dropAssumption("nights")
		stayChanged = true
	}
	if v := patch.Adults; v != nil && *v >= 1 && *v <= 10 && *v != c.Adults {
		c.Adults = *v
		p.dropAssumption("adults")
		stayChanged = true
	}
	if v := patch.Children; v != nil && *v >= 0 && *v <= 10 && *v != c.Children {
		c.Children = *v
		stayChanged = true
	}
	if v := patch.Pets; v != nil && *v != c.Pets {
		c.Pets = *v
		stayChanged = true
	}
	if stayChanged && len(p.Options) > 0 {
		// Dates or guests changed: re-check the same hotels instead of a new
		// destination search. The selection is kept and re-validated after search.
		p.Options = []HotelOption{}
		invalidated = append(invalidated, "даты или состав гостей изменились: цены и доступность перепроверяются")
	}
	if v := patch.BudgetRub; v != nil && *v > 0 && *v != c.BudgetRub {
		c.BudgetRub = *v
		invalidated = append(invalidated, "бюджет изменился: варианты пересчитаны без нового поиска")
	}
	p.markBudget()
	return invalidated
}

// SearchGate reports whether the plan has enough to search hotels. Only the
// destination is required: dates and guests fall back to visible assumptions,
// so the agent asks one question at most.
func (p *TripPlan) SearchGate() bool {
	return p.Constraints.Destination != ""
}

// FillDefaults applies the defaults for everything the user left out and
// records each one as an assumption.
func (p *TripPlan) FillDefaults(today time.Time) {
	c := &p.Constraints
	if c.CheckIn == "" {
		c.CheckIn = today.AddDate(0, 0, defaultLeadDays).Format(time.DateOnly)
		p.addAssumption("check_in", c.CheckIn, fmt.Sprintf("даты не названы, взяли заезд через %d дней", defaultLeadDays))
	}
	if c.Nights == 0 {
		c.Nights = defaultNights
		p.addAssumption("nights", fmt.Sprint(defaultNights), "длительность не названа")
	}
	if c.Adults == 0 {
		c.Adults = defaultAdults
		p.addAssumption("adults", fmt.Sprint(defaultAdults), "состав гостей не назван")
	}
}

// SetOptions stores search results and re-validates the current selection.
func (p *TripPlan) SetOptions(hotels []*inventoryv1.Hotel) {
	p.Options = make([]HotelOption, 0, len(hotels))
	for _, h := range hotels {
		p.Options = append(p.Options, HotelOption{
			ID: h.GetId(), Name: h.GetName(), City: h.GetCity(), Stars: h.GetStars(), Rating: h.GetRating(),
			Meal: h.GetMeal(), FreeCancellation: h.GetFreeCancellation(), PetsAllowed: h.GetPetsAllowed(),
			PricePerNightRub: h.GetPricePerNightRub(), TotalPriceRub: h.GetTotalPriceRub(),
			DistanceToCenterKm: h.GetDistanceToCenterKm(), Synthetic: h.GetSynthetic(),
		})
	}
	p.markBudget()
	p.Conflicts = p.Conflicts[:0]
	if p.Selected != nil {
		for _, o := range p.Options {
			if o.ID == p.Selected.ID {
				sel := o
				p.Selected = &sel
				return
			}
		}
		p.Conflicts = append(p.Conflicts, Conflict{
			Kind:    "selected_unavailable",
			Message: fmt.Sprintf("«%s» не подходит под новые условия", p.Selected.Name),
		})
	}
}

// NextStage moves the plan to the earliest stage whose gate is not passed.
func (p *TripPlan) NextStage() {
	switch {
	case !p.SearchGate():
		p.Stage = StageClarify
	case len(p.Options) == 0:
		p.Stage = StageSearch
	default:
		p.Stage = StageCompare
	}
}

func (p *TripPlan) markBudget() {
	budget := p.Constraints.BudgetRub
	for i := range p.Options {
		p.Options[i].OverBudget = budget > 0 && p.Options[i].TotalPriceRub > budget
	}
}

func (p *TripPlan) addAssumption(field, value, reason string) {
	p.dropAssumption(field)
	p.Assumptions = append(p.Assumptions, Assumption{Field: field, Value: value, Reason: reason})
}

func (p *TripPlan) dropAssumption(field string) {
	kept := p.Assumptions[:0]
	for _, a := range p.Assumptions {
		if a.Field != field {
			kept = append(kept, a)
		}
	}
	p.Assumptions = kept
}
