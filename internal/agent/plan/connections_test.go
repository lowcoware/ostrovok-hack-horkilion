package plan

import (
	"slices"
	"testing"
	"time"
)

var rules = ConnectionRules{MinArrivalToCheckin: 60 * time.Minute, DepartureBuffer: 120 * time.Minute}

func journey(id, depart, arrive string) Option {
	return Option{ID: id, Kind: KindJourney, PriceRub: 10000, Journey: &Journey{ID: id, Segments: []Segment{{
		Mode: "air", From: Place{ID: "A"}, To: Place{ID: "B"}, DepartAt: depart, ArriveAt: arrive,
	}}}}
}

func transfer(id, depart string, minutes int32) Option {
	return Option{ID: id, Kind: KindTransfer, PriceRub: 1500, Transfer: &Transfer{ID: id, DepartAt: depart, DurationMinutes: minutes}}
}

// trip is Сочи 2026-11-20 .. 2026-11-23 with a 14:00 check-in and a 12:00 check-out.
func trip(outbound, arrival, departure, back Option) TripPlan {
	p := New("s")
	p.Constraints.Destination = "Сочи"
	p.Constraints.Dates = Dates{Depart: "2026-11-20", Return: "2026-11-23", Nights: 3}
	p.Components.Stay.Options = []Option{{ID: "h1", Kind: KindStay, Stay: &Hotel{ID: "h1", CheckInTime: "14:00", CheckOutTime: "12:00"}}}
	p.Components.Outbound.Options = []Option{outbound}
	p.Components.TransferArrival.Options = []Option{arrival}
	p.Components.TransferDeparture.Options = []Option{departure}
	p.Components.Return.Options = []Option{back}
	p.UpdateConnectionChecks(rules)
	return p
}

func check(t *testing.T, p TripPlan, kind string) Check {
	t.Helper()
	i := slices.IndexFunc(p.Checks, func(c Check) bool { return c.Kind == kind })
	if i < 0 {
		t.Fatalf("no %s check in %+v", kind, p.Checks)
	}
	return p.Checks[i]
}

var (
	morningFlight = journey("out", "2026-11-20T09:30:00+03:00", "2026-11-20T12:05:00+03:00")
	eveningFlight = journey("back", "2026-11-23T17:15:00+03:00", "2026-11-23T19:30:00+03:00")
	taxiIn        = transfer("in", "2026-11-20T12:05:00+03:00", 45)
	taxiOut       = transfer("out", "2026-11-23T14:30:00+03:00", 45)
)

func TestWellConnectedTrip(t *testing.T) {
	p := trip(journey("out", "2026-11-20T10:00:00+03:00", "2026-11-20T13:00:00+03:00"), taxiIn, taxiOut, eveningFlight)
	for _, kind := range []string{CheckArrivalBeforeCheckin, CheckTransferFits, CheckCheckoutBeforeDeparture, CheckDatesConsistent} {
		if c := check(t, p, kind); c.Status != "ok" {
			t.Errorf("%s: want ok, got %s: %s", kind, c.Status, c.Message)
		}
	}
}

func TestEarlyArrivalWaitsForCheckin(t *testing.T) {
	c := check(t, trip(morningFlight, taxiIn, taxiOut, eveningFlight), CheckArrivalBeforeCheckin)
	if c.Status != "warn" || !slices.Equal(c.Components, []string{Outbound, TransferArrival, Stay}) {
		t.Errorf("12:05 plus an hour is before 14:00: want warn on outbound, transfer and stay, got %+v", c)
	}
}

func TestTransferToTheReturn(t *testing.T) {
	for _, tt := range []struct {
		leave, want string
	}{
		{"2026-11-23T14:30:00+03:00", "ok"},   // at the airport 15:15, two hours before 17:15
		{"2026-11-23T15:15:00+03:00", "warn"}, // 16:00, inside the buffer
		{"2026-11-23T16:45:00+03:00", "fail"}, // 17:30, after the departure
	} {
		c := check(t, trip(morningFlight, taxiIn, transfer("out", tt.leave, 45), eveningFlight), CheckTransferFits)
		if c.Status != tt.want {
			t.Errorf("leaving %s: want %s, got %s: %s", tt.leave, tt.want, c.Status, c.Message)
		}
	}
}

func TestReturnBeforeCheckoutDayFails(t *testing.T) {
	night := journey("back", "2026-11-23T01:00:00+03:00", "2026-11-23T03:30:00+03:00")
	p := trip(morningFlight, taxiIn, transfer("out", "2026-11-22T22:15:00+03:00", 45), night)
	if c := check(t, p, CheckCheckoutBeforeDeparture); c.Status != "fail" {
		t.Errorf("a 01:00 return needs leaving the night before check-out: want fail, got %+v", c)
	}
}

func TestOvernightTrainLosesTheFirstNight(t *testing.T) {
	train := journey("out", "2026-11-20T13:05:00+03:00", "2026-11-21T14:45:00+03:00")
	if c := check(t, trip(train, taxiIn, taxiOut, eveningFlight), CheckDatesConsistent); c.Status != "warn" {
		t.Errorf("arriving the day after check-in must warn, got %+v", c)
	}
}

func TestChecksNeedTheirComponents(t *testing.T) {
	p := New("s")
	p.UpdateConnectionChecks(rules)
	if len(p.Checks) != 0 {
		t.Errorf("no hotel, no connection checks, got %+v", p.Checks)
	}
}

func TestSelectingAHotelRechecksTransfers(t *testing.T) {
	p := trip(morningFlight, taxiIn, taxiOut, eveningFlight)
	p.Components.Stay.Options = append(p.Components.Stay.Options, Option{ID: "h2", Kind: KindStay, Stay: &Hotel{ID: "h2"}})
	p.Apply(Patch{Select: &Selection{Component: Stay, OptionID: "h2"}}, today)
	if !p.NeedsSearch(TransferArrival) || !p.NeedsSearch(TransferDeparture) || p.NeedsSearch(Outbound) {
		t.Errorf("a new hotel must re-check both transfers and nothing else")
	}
	q := trip(morningFlight, taxiIn, taxiOut, eveningFlight)
	q.Components.Outbound.Options = append(q.Components.Outbound.Options, journey("out2", "2026-11-20T07:00:00+03:00", "2026-11-20T09:00:00+03:00"))
	q.Apply(Patch{Select: &Selection{Component: Outbound, OptionID: "out2"}}, today)
	if !q.NeedsSearch(TransferArrival) || q.NeedsSearch(TransferDeparture) {
		t.Errorf("a new outbound must re-check only the arrival transfer")
	}
}
