package plan

import (
	"encoding/json"
	"testing"
	"time"

	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	"horkilion/internal/apitest"
)

var today = time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func hotel(id string, price int64) *lodgingv1.Hotel {
	return &lodgingv1.Hotel{
		Id: id, Name: "Отель " + id, City: "Сочи", Stars: 4, Rating: 8.5, Meal: "breakfast",
		Place:         &commonv1.Place{Name: "Отель " + id, Kind: commonv1.Place_KIND_HOTEL, Point: &commonv1.GeoPoint{Lat: 43.6, Lon: 39.7}},
		TotalPriceRub: price, PricePerNightRub: price / 3, CheckInTime: "14:00", CheckOutTime: "12:00",
		CancellationPolicy: "бесплатная отмена до 3 дней до заезда",
		Sources:            []*commonv1.Source{{Provider: "synthetic", Kind: commonv1.Source_KIND_SYNTHETIC, FieldScope: "*"}},
	}
}

func stayOptions(hotels ...*lodgingv1.Hotel) []Option {
	out := make([]Option, 0, len(hotels))
	for _, h := range hotels {
		out = append(out, StayOption(h))
	}
	return out
}

func searchedPlan(t *testing.T) TripPlan {
	t.Helper()
	p := New("4b0c5a5e-8f0e-4a43-9c55-2f3b9e3c7a10")
	p.Apply(Patch{Destination: ptr("Сочи")}, today)
	p.FillDefaults(today)
	p.SetOptions(Stay, stayOptions(hotel("a", 30000), hotel("b", 90000)))
	return p
}

func TestFillDefaultsRecordsAssumptions(t *testing.T) {
	p := New("s")
	p.Apply(Patch{Destination: ptr("Сочи")}, today)
	p.FillDefaults(today)
	d := p.Constraints.Dates
	if d.Depart != "2026-11-07" || d.Nights != 3 || d.Return != "2026-11-10" || p.Constraints.Adults != 2 {
		t.Fatalf("defaults are wrong: %+v adults=%d", d, p.Constraints.Adults)
	}
	if len(p.Assumptions) != 5 {
		t.Fatalf("want 5 assumptions (origin, citizenship, depart, nights, adults), got %+v", p.Assumptions)
	}
	p.Apply(Patch{Dates: &DatesPatch{Nights: ptr(int32(5))}}, today)
	if p.Constraints.Dates.Return != "2026-11-12" || len(p.Assumptions) != 4 {
		t.Errorf("explicit nights must update return and drop the assumption: %+v %+v", p.Constraints.Dates, p.Assumptions)
	}
}

func TestBudgetRecomputesWithoutSearch(t *testing.T) {
	p := searchedPlan(t)
	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "b"}}, today)
	if p.TotalRub != 90000 || p.Components.Stay.SelectedID != "b" {
		t.Fatalf("selection must set total: total=%d selected=%q", p.TotalRub, p.Components.Stay.SelectedID)
	}

	changes := p.Apply(Patch{Budget: &Budget{AmountRub: 50000}}, today)
	if p.NeedsSearch(Stay) {
		t.Fatal("budget change must not re-search hotels")
	}
	if len(changes) != 1 || !p.OverBudget {
		t.Errorf("want one change and over_budget, got %v over=%v", changes, p.OverBudget)
	}
	a, b := p.Components.Stay.Options[0], p.Components.Stay.Options[1]
	if a.hasFlag(FlagOverBudget) || !b.hasFlag(FlagOverBudget) {
		t.Errorf("over_budget flags are wrong: a=%v b=%v", a.Flags, b.Flags)
	}
}

func TestPetsRecheckKeepsSelection(t *testing.T) {
	p := searchedPlan(t)
	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "b"}}, today)

	p.Apply(Patch{Pets: ptr(true)}, today)
	if !p.NeedsSearch(Stay) || p.Components.Stay.SelectedID != "b" {
		t.Fatalf("pets must schedule a re-check and keep the selection")
	}
	if !p.NeedsSearch(Outbound) {
		t.Fatal("an empty component always needs a search")
	}

	p.SetOptions(Stay, stayOptions(hotel("a", 31000)))
	if len(p.Conflicts) != 1 || p.Conflicts[0].Kind != "selected_unavailable" || p.Conflicts[0].Components[0] != Stay {
		t.Fatalf("lost selection must be a conflict, got %+v", p.Conflicts)
	}
	last := p.Components.Stay.Options[len(p.Components.Stay.Options)-1]
	if last.ID != "b" || !last.hasFlag(FlagSelectedUnavailable) || p.TotalRub != 0 {
		t.Errorf("unavailable selection must stay visible and out of the total: %+v total=%d", last, p.TotalRub)
	}

	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "a"}}, today)
	if len(p.Conflicts) != 0 || p.TotalRub != 31000 {
		t.Errorf("a new selection must resolve the conflict: %+v total=%d", p.Conflicts, p.TotalRub)
	}
}

func TestDestinationChangeDropsEverything(t *testing.T) {
	p := searchedPlan(t)
	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "a"}}, today)
	p.Apply(Patch{Destination: ptr("Казань")}, today)
	if len(p.Components.Stay.Options) != 0 || p.Components.Stay.SelectedID != "" || p.TotalRub != 0 {
		t.Errorf("new destination must drop options and selection: %+v", p.Components.Stay)
	}
}

func TestSelectUnknownOptionKeepsSelection(t *testing.T) {
	p := searchedPlan(t)
	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "a"}}, today)
	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "nope"}}, today)
	if p.Components.Stay.SelectedID != "a" {
		t.Errorf("unknown option must not change the selection, got %q", p.Components.Stay.SelectedID)
	}
}

func TestApplyIgnoresInvalidValues(t *testing.T) {
	p := New("s")
	p.Apply(Patch{
		Dates:     &DatesPatch{Nights: ptr(int32(0)), Depart: ptr("2020-01-01")},
		Adults:    ptr(int32(-1)),
		ChildAges: &[]int32{40},
	}, today)
	if c := p.Constraints; c.Dates.Nights != 0 || c.Adults != 0 || c.Dates.Depart != "" || len(c.ChildAges) != 0 {
		t.Errorf("invalid values must be ignored, got %+v", c)
	}
}

func TestPatchValidate(t *testing.T) {
	bad := []Patch{
		{},
		{Adults: ptr(int32(0))},
		{ChildAges: &[]int32{18}},
		{Dates: &DatesPatch{Depart: ptr("05.03.2027")}},
		{Preferences: &PreferencesPatch{Meal: ptr("buffet")}},
		{Select: &Selection{Component: "hotel", OptionID: "a"}},
		{Budget: &Budget{AmountRub: 0}},
	}
	for _, p := range bad {
		if p.Validate() == nil {
			t.Errorf("patch %+v must be invalid", p)
		}
	}
	if err := (Patch{Pets: ptr(false)}).Validate(); err != nil {
		t.Errorf("pets=false is a valid change: %v", err)
	}
}

func TestPlanMatchesSchema(t *testing.T) {
	apitest.ValidateSchema(t, "TripPlan", New("4b0c5a5e-8f0e-4a43-9c55-2f3b9e3c7a10"))

	p := searchedPlan(t)
	p.Apply(Patch{
		Origin:      ptr("Москва"),
		ChildAges:   &[]int32{6},
		Budget:      &Budget{AmountRub: 120000, Strict: true},
		Preferences: &PreferencesPatch{TransportModes: &[]string{"rail"}, Moods: &[]string{"sea"}},
		Select:      &Selection{Component: Stay, OptionID: "a"},
	}, today)
	p.Components.Outbound.Options = []Option{{
		ID: "j1", Kind: KindJourney, PriceRub: 12000, Flags: []string{},
		Explanation: &Explanation{Why: "прямой поезд", Tradeoffs: []string{"дольше самолёта"}, Risks: []string{}},
		Journey: &Journey{ID: "j1", DurationMinutes: 1440, Segments: []Segment{{
			Mode: "rail", Carrier: "РЖД", Number: "104В",
			From:     Place{Name: "Москва Казанская", Kind: "station"},
			To:       Place{Name: "Сочи", Kind: "station"},
			DepartAt: "2026-11-06T21:40:00+03:00", ArriveAt: "2026-11-07T21:40:00+03:00",
		}}, PriceRub: 12000, Sources: []Source{{Provider: "synthetic", Kind: "synthetic", FieldScope: "*"}}},
		Sources: []Source{{Provider: "synthetic", Kind: "synthetic", FieldScope: "*"}},
	}}
	p.Components.TransferArrival.Options = []Option{{
		ID: "t1", Kind: KindTransfer, PriceRub: 900, Flags: []string{},
		Transfer: &Transfer{ID: "t1", Mode: "taxi", From: Place{Name: "Сочи", Kind: "station"}, To: Place{Name: "Отель a", Kind: "hotel"},
			DepartAt: "2026-11-07T21:50:00+03:00", DurationMinutes: 25, PriceRub: 900, Sources: []Source{}},
		Sources: []Source{},
	}}
	p.Weather = &Weather{Place: Place{Name: "Сочи", Kind: "city"}, Date: "2026-11-07", Kind: "forecast", Sources: []Source{
		{Provider: "open-meteo", Kind: "real", FieldScope: "*", URL: "https://open-meteo.com", License: "CC-BY-4.0", FetchedAt: "2026-10-24T10:00:00Z"},
	}}
	p.Checks = []Check{{Kind: "arrival_before_checkin", Status: "warn", Message: "прибытие после 21:00", Components: []string{Outbound, Stay}}}
	p.DestinationOptions = []DestinationOption{{Destination: Destination{
		ID: "sochi", Name: "Сочи", Country: "Россия", CountryCode: "RU", Place: Place{Name: "Сочи", Kind: "city"},
		Tags: []string{"sea"}, EstimatedTotalRub: 80000, TravelHours: 2.5, Sources: []Source{},
		Visa: VisaRequirement{Citizenship: "RU", CountryCode: "RU", Status: VisaHome, Sources: []Source{{Provider: "passport-index-data", Kind: "real", FieldScope: "visa"}}},
	}}}
	p.recompute()
	p.NextStage()
	apitest.ValidateSchema(t, "TripPlan", p)

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back TripPlan
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Components.Outbound.Options[0].Journey.Segments[0].Number != "104В" || back.Components.Stay.Selected() == nil {
		t.Errorf("plan does not survive a JSON round trip: %s", raw)
	}
}

func TestPatchMatchesSchema(t *testing.T) {
	apitest.ValidateSchema(t, "PlanPatch", Patch{
		Origin: ptr("Москва"), Destination: ptr("Сочи"),
		Dates:     &DatesPatch{Depart: ptr("2026-11-20"), Nights: ptr(int32(5)), FlexDays: ptr(int32(2))},
		Adults:    ptr(int32(2)),
		ChildAges: &[]int32{4, 9}, Pets: ptr(true),
		Budget:      &Budget{AmountRub: 100000},
		Preferences: &PreferencesPatch{Meal: ptr("breakfast"), FreeCancellation: ptr(true), TransportModes: &[]string{"air"}},
		Select:      &Selection{Component: Stay, OptionID: "a"},
	})
}
