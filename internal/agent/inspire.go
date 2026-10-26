package agent

import (
	"context"
	"log/slog"

	catalogv1 "horkilion/gen/catalog/v1"
	"horkilion/internal/agent/plan"
)

// minDestinationOptions is how many destinations inspire tries to offer; with
// fewer, the search is repeated without the budget and travel time limits and
// the explanations say what was given up.
const minDestinationOptions = 3

// inspire suggests destinations when the user has not named one (stage
// inspire). It searches every turn without a destination, so any change of
// dates, party, budget or moods shows up at once.
func (a *Agent) inspire(ctx context.Context, p *plan.TripPlan) error {
	c := p.Constraints
	req := &catalogv1.SearchDestinationsRequest{
		Origin: c.Origin, DepartDate: c.Dates.Depart, ReturnDate: c.Dates.Return, Travelers: c.TravelersProto(),
		Tags: c.Preferences.Moods, MaxTravelHours: c.Preferences.MaxTravelHours, Citizenship: c.Citizenship,
		VisaFreeOnly: c.Preferences.VisaFreeOnly, Limit: plan.MaxDestinationOptions,
	}
	if c.Budget != nil {
		req.BudgetRub = c.Budget.AmountRub
	}
	found, err := a.searchDestinations(ctx, req)
	if err != nil {
		return err
	}
	if len(found) < minDestinationOptions && (req.BudgetRub > 0 || req.MaxTravelHours > 0) {
		req.BudgetRub, req.MaxTravelHours = 0, 0
		if found, err = a.searchDestinations(ctx, req); err != nil {
			return err
		}
	}
	p.SetDestinationOptions(found)
	return nil
}

func (a *Agent) searchDestinations(ctx context.Context, req *catalogv1.SearchDestinationsRequest) (out []plan.Destination, err error) {
	ctx, span := startSpan(ctx, "catalog.SearchDestinations", "tool", req)
	defer func() { endSpan(span, out, err) }()
	ctx, cancel := context.WithTimeout(ctx, supplierTimeout)
	defer cancel()
	resp, err := a.catalog.SearchDestinations(ctx, req)
	if err != nil {
		slog.Warn("destination search failed", "origin", req.GetOrigin(), "err", err)
		return nil, err
	}
	for _, d := range resp.GetDestinations() {
		out = append(out, plan.DestinationFromProto(d))
	}
	return out, nil
}
