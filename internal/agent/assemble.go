package agent

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/agent/plan"
	"horkilion/internal/platform"
)

// supplierTimeout bounds every supplier call of a turn.
const supplierTimeout = 5 * time.Second

// assemble searches or re-checks every component the plan needs in this turn,
// in trip order: hotels, both journeys, then the transfers between the chosen
// journeys and the chosen hotel. Transport and transfers are best effort: a
// failed search leaves the component empty and the reply says nothing is found.
func (a *Agent) assemble(ctx context.Context, p *plan.TripPlan, ev *events) error {
	// A fresh search of a journey or of hotels changes what the transfers
	// connect, so they follow it in the same turn.
	stay, outbound, back := p.NeedsSearch(plan.Stay), p.NeedsSearch(plan.Outbound), p.NeedsSearch(plan.Return)
	if stay {
		ev.emit("status", platform.StatusData{Stage: plan.StageSearch, Text: "ищу отели"})
		if err := a.searchStay(ctx, p); err != nil {
			return err
		}
	}
	if p.Visa == nil {
		a.visa(ctx, p)
	}
	if outbound || back {
		ev.emit("status", platform.StatusData{Stage: plan.StageSearch, Text: "ищу дорогу"})
	}
	c := p.Constraints
	if outbound {
		a.searchJourneys(ctx, p, plan.Outbound, c.Origin, c.Destination, c.Dates.Depart)
	}
	if back {
		a.searchJourneys(ctx, p, plan.Return, c.Destination, c.Origin, c.Dates.Return)
	}
	if stay || outbound || p.NeedsSearch(plan.TransferArrival) {
		a.searchArrivalTransfer(ctx, p)
	}
	if stay || back || p.NeedsSearch(plan.TransferDeparture) {
		a.searchDepartureTransfer(ctx, p)
	}
	p.UpdateConnectionChecks(a.rules.connectionRules())
	return nil
}

func (a *Agent) searchJourneys(ctx context.Context, p *plan.TripPlan, component, from, to, date string) {
	req := &transportv1.SearchJourneysRequest{Origin: from, Destination: to, Date: date, Passengers: p.Constraints.TravelersProto()}
	for _, m := range p.Constraints.Preferences.TransportModes {
		req.Modes = append(req.Modes, transportModes[m])
	}
	ctx, span := startSpan(ctx, "transport.SearchJourneys", "tool", req)
	ctx, cancel := context.WithTimeout(ctx, supplierTimeout)
	defer cancel()
	resp, err := a.transport.SearchJourneys(ctx, req)
	endSpan(span, resp, err)
	if err != nil {
		slog.Warn("journey search failed", "component", component, "from", from, "to", to, "err", err)
		p.SetOptions(component, nil)
		return
	}
	maxMinutes := int32(p.Constraints.Preferences.MaxTravelHours * 60)
	options := make([]plan.Option, 0, len(resp.GetJourneys()))
	for _, j := range resp.GetJourneys() {
		if maxMinutes > 0 && j.GetDurationMinutes() > maxMinutes {
			continue
		}
		options = append(options, plan.JourneyOption(j))
	}
	// The first option is what the trip is assembled from until the user
	// chooses: an outbound that arrives on the check-in day before one that
	// loses the first night, then the cheapest, then the fastest.
	lateness := func(o plan.Option) int {
		segs := o.Journey.Segments
		if component == plan.Outbound && len(segs) > 0 && !strings.HasPrefix(segs[len(segs)-1].ArriveAt, date) {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(options, func(x, y plan.Option) int {
		return cmp.Or(cmp.Compare(lateness(x), lateness(y)), cmp.Compare(x.PriceRub, y.PriceRub),
			cmp.Compare(x.Journey.DurationMinutes, y.Journey.DurationMinutes))
	})
	p.SetOptions(component, options)
}

var transportModes = map[string]transportv1.Mode{"air": transportv1.Mode_MODE_AIR, "rail": transportv1.Mode_MODE_RAIL}

// searchArrivalTransfer looks for transfers from where the outbound journey
// ends to the hotel, leaving at the arrival time.
func (a *Agent) searchArrivalTransfer(ctx context.Context, p *plan.TripPlan) {
	stay := p.Components.Stay.Effective()
	place, arrive, ok := p.Arrival()
	if stay == nil || stay.Stay == nil || !ok {
		p.SetOptions(plan.TransferArrival, nil)
		return
	}
	options, _ := a.transfers(ctx, p, place.ID, stay.Stay.ID, arrive)
	p.SetOptions(plan.TransferArrival, options)
}

// searchDepartureTransfer looks for transfers from the hotel to where the
// return journey starts, each mode leaving so that it arrives the departure
// buffer before the journey: a first search learns the durations, a second
// per mode asks for the right departure time.
func (a *Agent) searchDepartureTransfer(ctx context.Context, p *plan.TripPlan) {
	stay := p.Components.Stay.Effective()
	place, depart, ok := p.Departure()
	if stay == nil || stay.Stay == nil || !ok {
		p.SetOptions(plan.TransferDeparture, nil)
		return
	}
	deadline := depart.Add(-a.rules.connectionRules().DepartureBuffer)
	probe, err := a.transfers(ctx, p, stay.Stay.ID, place.ID, deadline)
	if err != nil {
		p.SetOptions(plan.TransferDeparture, nil)
		return
	}
	options := make([]plan.Option, 0, len(probe))
	for _, o := range probe {
		// Round down to five minutes: a pick-up at 10:45, not 10:49.
		leave := deadline.Add(-time.Duration(o.Transfer.DurationMinutes) * time.Minute).Truncate(5 * time.Minute)
		timed, err := a.transfers(ctx, p, stay.Stay.ID, place.ID, leave)
		if err != nil {
			continue
		}
		for _, t := range timed {
			if t.Transfer.Mode == o.Transfer.Mode {
				options = append(options, t)
			}
		}
	}
	p.SetOptions(plan.TransferDeparture, options)
}

func (a *Agent) transfers(ctx context.Context, p *plan.TripPlan, fromID, toID string, at time.Time) ([]plan.Option, error) {
	req := &transferv1.SearchTransfersRequest{
		From: &commonv1.Place{Id: fromID}, To: &commonv1.Place{Id: toID}, DepartAt: at.Format(time.RFC3339),
		Passengers: p.Constraints.TravelersProto(),
	}
	ctx, span := startSpan(ctx, "transfer.SearchTransfers", "tool", req)
	ctx, cancel := context.WithTimeout(ctx, supplierTimeout)
	defer cancel()
	resp, err := a.transfer.SearchTransfers(ctx, req)
	endSpan(span, resp, err)
	if err != nil {
		slog.Warn("transfer search failed", "from", fromID, "to", toID, "err", err)
		return nil, err
	}
	options := make([]plan.Option, 0, len(resp.GetTransfers()))
	for _, t := range resp.GetTransfers() {
		options = append(options, plan.TransferOption(t))
	}
	return options, nil
}

// visa finds the country of the destination and its visa requirement for the
// citizenship of the plan (spec 0002). Anything missing leaves a warning.
func (a *Agent) visa(ctx context.Context, p *plan.TripPlan) {
	c := p.Constraints
	country := ""
	if stay := p.Components.Stay.Effective(); stay != nil && stay.Stay != nil {
		country = stay.Stay.Place.CountryCode
	}
	if country == "" {
		req := &catalogv1.SearchPlacesRequest{Query: c.Destination, Kinds: []commonv1.Place_Kind{commonv1.Place_KIND_CITY}, Limit: 1}
		cctx, cancel := context.WithTimeout(ctx, supplierTimeout)
		resp, err := a.catalog.SearchPlaces(cctx, req)
		cancel()
		if err == nil && len(resp.GetPlaces()) > 0 {
			country = resp.GetPlaces()[0].GetCountryCode()
		}
	}
	if country == "" {
		p.SetVisa(nil)
		return
	}
	req := &catalogv1.GetVisaRequirementRequest{Citizenship: c.Citizenship, CountryCode: country}
	ctx, span := startSpan(ctx, "catalog.GetVisaRequirement", "tool", req)
	ctx, cancel := context.WithTimeout(ctx, supplierTimeout)
	defer cancel()
	v, err := a.catalog.GetVisaRequirement(ctx, req)
	endSpan(span, v, err)
	if err != nil {
		slog.Warn("visa check failed", "citizenship", c.Citizenship, "country", country, "err", err)
		p.SetVisa(nil)
		return
	}
	p.SetVisa(plan.VisaFromProto(v))
}
