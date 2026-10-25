package agent

import (
	"testing"

	"horkilion/internal/agent/plan"
)

func names(es []funnelEvent) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.name)
	}
	return out
}

func TestFunnelEvents(t *testing.T) {
	p := plan.New("s")
	p.Stage = plan.StageInspire
	if got := names(funnelEvents(p, plan.StageClarify, true, nil, "", nil)); len(got) != 2 || got[0] != "session_started" || got[1] != "stage_entered" {
		t.Errorf("the first turn starts the session and enters inspire, got %v", got)
	}
	if got := funnelEvents(p, plan.StageInspire, false, nil, "", nil); len(got) != 0 {
		t.Errorf("a quiet turn has no funnel events, got %v", names(got))
	}

	p.Components.Stay.Options = []plan.Option{{ID: "h1", Kind: plan.KindStay, PriceRub: 30000, Stay: &plan.Hotel{ID: "h1"}}}
	p.Components.Stay.SelectedID = "h1"
	sel := &plan.Selection{Component: plan.Stay, OptionID: "h1"}
	got := funnelEvents(p, p.Stage, false, sel, "", nil)
	if len(got) != 1 || got[0].name != "option_selected" || got[0].props["price_rub"] != int64(30000) {
		t.Errorf("a selection is recorded with its price, got %+v", got)
	}
	if got := funnelEvents(p, p.Stage, false, sel, "h1", nil); len(got) != 0 {
		t.Errorf("selecting the same option again is not a new selection, got %v", names(got))
	}
	if got := funnelEvents(p, p.Stage, false, &plan.Selection{Component: plan.Stay, OptionID: "gone"}, "h1", nil); len(got) != 0 {
		t.Errorf("a failed selection is not recorded, got %v", names(got))
	}
}
