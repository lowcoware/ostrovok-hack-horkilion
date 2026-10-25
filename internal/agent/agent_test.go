package agent

import (
	"testing"
	"time"

	inventoryv1 "horkilion/gen/inventory/v1"
)

var today = time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)

func TestRouteRules(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Patch
	}{
		{"city after preposition", "Хочу в Сочи на 5 ночей", Patch{Destination: ptr("Сочи"), Nights: ptr(int32(5))}},
		{"collective numeral", "Хочу в Сочи, нас двое", Patch{Destination: ptr("Сочи"), Adults: ptr(int32(2))}},
		{"bare city", "Казань", Patch{Destination: ptr("Казань")}},
		{"guests and pet", "нас 2 взрослых и 1 ребёнок, с нами едет собака", Patch{Adults: ptr(int32(2)), Children: ptr(int32(1)), Pets: ptr(true)}},
		{"budget in thousands", "бюджет 80 тысяч", Patch{BudgetRub: ptr(int64(80000))}},
		{"day and month rolls to next year", "заезд 05.03", Patch{CheckIn: ptr("2027-03-05")}},
		{"lowercase month is not a city", "в ноябре", Patch{}},
		{"empty", "", Patch{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeRules(NewPlan("s"), tt.text, today)
			if s(got.Destination) != s(tt.want.Destination) || s(got.CheckIn) != s(tt.want.CheckIn) ||
				n(got.Nights) != n(tt.want.Nights) || n(got.Adults) != n(tt.want.Adults) ||
				n(got.Children) != n(tt.want.Children) || b(got.Pets) != b(tt.want.Pets) ||
				i64(got.BudgetRub) != i64(tt.want.BudgetRub) {
				t.Errorf("routeRules(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

func TestApplyInvalidatesOnlyWhatChanged(t *testing.T) {
	plan := NewPlan("s")
	plan.Apply(Patch{Destination: ptr("Сочи")}, today)
	plan.FillDefaults(today)
	plan.SetOptions([]*inventoryv1.Hotel{{Id: "a", Name: "A", TotalPriceRub: 30000}, {Id: "b", Name: "B", TotalPriceRub: 90000}})
	plan.Selected = &plan.Options[1]

	plan.Apply(Patch{BudgetRub: ptr(int64(50000))}, today)
	if len(plan.Options) != 2 {
		t.Fatalf("budget change must keep options, got %d", len(plan.Options))
	}
	if plan.Options[0].OverBudget || !plan.Options[1].OverBudget {
		t.Errorf("over-budget flags are wrong: %+v", plan.Options)
	}

	plan.Apply(Patch{Pets: ptr(true)}, today)
	if len(plan.Options) != 0 || plan.Selected == nil {
		t.Fatalf("pets must clear options for re-check and keep the selection, got options=%d selected=%v", len(plan.Options), plan.Selected)
	}
	plan.SetOptions([]*inventoryv1.Hotel{{Id: "a", Name: "A", TotalPriceRub: 30000}})
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != "selected_unavailable" {
		t.Errorf("dropped selection must become a conflict, got %+v", plan.Conflicts)
	}

	plan.Apply(Patch{Destination: ptr("Казань")}, today)
	if plan.Selected != nil || len(plan.Options) != 0 {
		t.Errorf("destination change must reset options and selection")
	}
}

func TestApplyRejectsInvalidValues(t *testing.T) {
	plan := NewPlan("s")
	plan.Apply(Patch{Nights: ptr(int32(0)), Adults: ptr(int32(-1)), CheckIn: ptr("2020-01-01")}, today)
	if c := plan.Constraints; c.Nights != 0 || c.Adults != 0 || c.CheckIn != "" {
		t.Errorf("invalid values must be ignored, got %+v", c)
	}
}

func TestFillDefaultsRecordsAssumptions(t *testing.T) {
	plan := NewPlan("s")
	plan.Apply(Patch{Destination: ptr("Сочи"), Nights: ptr(int32(5))}, today)
	plan.FillDefaults(today)
	if len(plan.Assumptions) != 2 {
		t.Fatalf("want assumptions for check_in and adults only, got %+v", plan.Assumptions)
	}
	plan.Apply(Patch{Adults: ptr(int32(3))}, today)
	if len(plan.Assumptions) != 1 {
		t.Errorf("an explicit value must drop its assumption, got %+v", plan.Assumptions)
	}
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func n(p *int32) int32 {
	if p == nil {
		return -1
	}
	return *p
}
func b(p *bool) string {
	if p == nil {
		return "nil"
	}
	if *p {
		return "true"
	}
	return "false"
}
func i64(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}
