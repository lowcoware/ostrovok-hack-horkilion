package agent

import (
	"context"
	"slices"
	"testing"

	"horkilion/internal/agent/plan"
)

// Phrases of the phase 1 scenario (spec 0003) and of the visa spec 0002.
func TestRouteRulesPhase1Fields(t *testing.T) {
	tests := []struct {
		text  string
		check func(p plan.Patch) bool
	}{
		{"хочу к тёплому морю в ноябре, недорого, с ребёнком, без долгих перелётов", func(p plan.Patch) bool {
			return p.Dates != nil && s(p.Dates.Depart) == "2026-11-01" && p.Assumed[plan.FieldDepart] != "" &&
				p.Preferences != nil && slices.Equal(*p.Preferences.Moods, []string{"sea", "warm", "kids"}) &&
				*p.Preferences.MaxTravelHours == 5 && p.Assumed[plan.FieldMaxTravelHours] != "" &&
				p.ChildAges != nil && len(*p.ChildAges) == 1 && p.Budget == nil
		}},
		{"у меня паспорт Беларуси", func(p plan.Patch) bool { return s(p.Citizenship) == "BY" }},
		{"я гражданин Казахстана", func(p plan.Patch) bool { return s(p.Citizenship) == "KZ" }},
		{"летим из Минска в Сочи", func(p plan.Patch) bool { return s(p.Origin) == "Минска" && s(p.Destination) == "Сочи" }},
		{"Из Казани", func(p plan.Patch) bool { return s(p.Origin) == "Казани" }},
		{"бюджет строго 100 тысяч", func(p plan.Patch) bool { return p.Budget != nil && p.Budget.AmountRub == 100000 && p.Budget.Strict }},
		{"бюджет примерно 100 тысяч", func(p plan.Patch) bool { return p.Budget != nil && !p.Budget.Strict }},
		{"хочу поездом", func(p plan.Patch) bool {
			return p.Preferences != nil && slices.Equal(*p.Preferences.TransportModes, []string{"rail"})
		}},
		{"не больше 4 часов в пути", func(p plan.Patch) bool {
			return p.Preferences != nil && *p.Preferences.MaxTravelHours == 4 && p.Assumed[plan.FieldMaxTravelHours] == ""
		}},
		{"виза не проблема", func(p plan.Patch) bool {
			return p.Preferences != nil && p.Preferences.VisaFreeOnly != nil && !*p.Preferences.VisaFreeOnly
		}},
		{"только без визы", func(p plan.Patch) bool { return p.Preferences != nil && *p.Preferences.VisaFreeOnly }},
		{"в горы на природу", func(p plan.Patch) bool { return slices.Equal(*p.Preferences.Moods, []string{"mountains", "nature"}) }},
		{"хочу в город", func(p plan.Patch) bool { return p.Preferences == nil }},
		{"нас двое и ребёнок", func(p plan.Patch) bool { return n(p.Adults) == 2 && p.ChildAges != nil && len(*p.ChildAges) == 1 }},
		{"в октябре", func(p plan.Patch) bool { return p.Dates != nil && s(p.Dates.Depart) == "2026-10-24" }},
		{"в марте", func(p plan.Patch) bool { return p.Dates != nil && s(p.Dates.Depart) == "2027-03-01" }},
	}
	for _, tt := range tests {
		if got := routeRules(plan.New("s"), tt.text, today).patch(); !tt.check(got) {
			t.Errorf("routeRules(%q) = %+v, prefs %+v", tt.text, got, got.Preferences)
		}
	}
}

func TestInterpretedFieldsBecomeAssumptions(t *testing.T) {
	p := plan.New("s")
	p.Apply(routeRules(p, "в ноябре без долгих перелётов", today).patch(), today)
	fields := map[string]bool{}
	for _, a := range p.Assumptions {
		fields[a.Field] = true
	}
	if !fields[plan.FieldDepart] || !fields[plan.FieldMaxTravelHours] {
		t.Errorf("a month and «без долгих перелётов» must be visible assumptions, got %+v", p.Assumptions)
	}
	p.Apply(routeRules(p, "не больше 3 часов", today).patch(), today)
	for _, a := range p.Assumptions {
		if a.Field == plan.FieldMaxTravelHours {
			t.Errorf("an explicit limit must drop the assumption, got %+v", p.Assumptions)
		}
	}
}

func TestResolvePlacesFixesTheCase(t *testing.T) {
	a := inventoryAgent(t)
	for in, want := range map[string]string{"Минска": "Минск", "Москвы": "Москва", "Казани": "Казань", "Анталию": "Анталия", "Сочи": "Сочи", "Рим": "Рим"} {
		patch := plan.Patch{Origin: ptr(in)}
		a.resolvePlaces(context.Background(), &patch)
		if *patch.Origin != want {
			t.Errorf("%s: want %s, got %s", in, want, *patch.Origin)
		}
	}
}

func TestPassportPhraseDoesNotPickKazan(t *testing.T) {
	p := plan.New("s")
	p.DestinationOptions = []plan.DestinationOption{{Destination: plan.Destination{Name: "Казань"}}}
	if got := pickDestination(p, "я гражданин казахстана"); got != "" {
		t.Errorf("«казахстана» must not choose Казань, got %q", got)
	}
}
