package agent

import (
	"testing"

	"horkilion/internal/agent/plan"
	"horkilion/internal/apitest"
)

func TestNoDestinationInspires(t *testing.T) {
	a := inventoryAgent(t)
	p := plan.New("s")
	say(t, a, &p, "хочу отдохнуть в ноябре")
	if p.Stage != plan.StageInspire || len(p.DestinationOptions) < 3 {
		t.Fatalf("want stage inspire with at least three destinations, got %s with %d", p.Stage, len(p.DestinationOptions))
	}
	for _, o := range p.DestinationOptions {
		if o.Explanation == nil || o.Explanation.Why == "" || !o.Destination.Visa.VisaFree() {
			t.Errorf("%s: want an explanation and a visa-free country by default, got %+v", o.Destination.Name, o)
		}
	}
	apitest.ValidateSchema(t, "TripPlan", p)
}

func TestTightLimitsAreRelaxedAndExplained(t *testing.T) {
	a := inventoryAgent(t)
	p := plan.New("s")
	p.Apply(plan.Patch{Budget: &plan.Budget{AmountRub: 1000}}, today)
	say(t, a, &p, "куда-нибудь")
	if len(p.DestinationOptions) < 3 {
		t.Fatalf("an impossible budget must still give suggestions, got %d", len(p.DestinationOptions))
	}
	if tr := p.DestinationOptions[0].Explanation.Tradeoffs; len(tr) == 0 {
		t.Errorf("going over the budget must be named as a trade-off")
	}
}

func TestChoosingASuggestionStartsTheSearch(t *testing.T) {
	for _, text := range []string{"давай второй вариант", "поедем в Анталию"} {
		a := inventoryAgent(t)
		p := plan.New("s")
		say(t, a, &p, "хочу на море")
		say(t, a, &p, text)
		if p.Constraints.Destination == "" || p.Stage == plan.StageInspire || len(p.Components.Stay.Options) == 0 {
			t.Errorf("%q must pick a destination and search hotels, got %q at %s", text, p.Constraints.Destination, p.Stage)
		}
	}
}

func TestPickDestinationMatchesAnyCase(t *testing.T) {
	p := plan.New("s")
	for _, name := range []string{"Дубай", "Сочи", "Анталия"} {
		p.DestinationOptions = append(p.DestinationOptions, plan.DestinationOption{Destination: plan.Destination{Name: name}})
	}
	for text, want := range map[string]string{"а что в дубае": "Дубай", "сочи": "Сочи", "третий": "Анталия", "погода": ""} {
		if got := pickDestination(p, text); got != want {
			t.Errorf("pickDestination(%q) = %q, want %q", text, got, want)
		}
	}
}
