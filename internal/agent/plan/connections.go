package plan

import (
	"fmt"
	"time"
)

// Connection check kinds, see Check.kind in api/openapi.yaml.
const (
	CheckArrivalBeforeCheckin    = "arrival_before_checkin"
	CheckTransferFits            = "transfer_fits"
	CheckCheckoutBeforeDeparture = "checkout_before_departure"
	CheckDatesConsistent         = "dates_consistent"
)

// ConnectionRules are the intervals of config/rules.yaml the validator uses.
type ConnectionRules struct {
	// MinArrivalToCheckin is the least time from arrival to the hotel door,
	// transfer included.
	MinArrivalToCheckin time.Duration
	// DepartureBuffer is the time to be at the airport or station before departure.
	DepartureBuffer time.Duration
}

// Effective is the option the trip is assembled from: the selection, or the
// first offered option while the user has not chosen.
func (c *Component) Effective() *Option {
	if sel := c.Selected(); sel != nil && !sel.hasFlag(FlagSelectedUnavailable) {
		return sel
	}
	for i := range c.Options {
		if !c.Options[i].hasFlag(FlagSelectedUnavailable) {
			return &c.Options[i]
		}
	}
	return nil
}

// Arrival is where and when the effective outbound journey ends.
func (p *TripPlan) Arrival() (Place, time.Time, bool) {
	o := p.Components.Outbound.Effective()
	if o == nil || o.Journey == nil || len(o.Journey.Segments) == 0 {
		return Place{}, time.Time{}, false
	}
	last := o.Journey.Segments[len(o.Journey.Segments)-1]
	at, err := time.Parse(time.RFC3339, last.ArriveAt)
	return last.To, at, err == nil
}

// Departure is where and when the effective return journey starts.
func (p *TripPlan) Departure() (Place, time.Time, bool) {
	o := p.Components.Return.Effective()
	if o == nil || o.Journey == nil || len(o.Journey.Segments) == 0 {
		return Place{}, time.Time{}, false
	}
	first := o.Journey.Segments[0]
	at, err := time.Parse(time.RFC3339, first.DepartAt)
	return first.From, at, err == nil
}

// UpdateConnectionChecks validates the assembled trip (ADR 0001, step 5):
// arrival against check-in, the transfer to the return journey, check-out
// against the return and the journey dates against the stay. Checks whose
// components are missing are dropped, not guessed.
func (p *TripPlan) UpdateConnectionChecks(r ConnectionRules) {
	p.dropChecks(CheckArrivalBeforeCheckin, CheckTransferFits, CheckCheckoutBeforeDeparture, CheckDatesConsistent)
	stay := p.Components.Stay.Effective()
	if stay == nil || stay.Stay == nil {
		return
	}
	hotel := stay.Stay
	_, arrive, hasArrival := p.Arrival()
	_, depart, hasDeparture := p.Departure()

	if hasArrival || hasDeparture {
		p.Checks = append(p.Checks, p.datesCheck(arrive, hasArrival, depart, hasDeparture))
	}
	if hasArrival {
		p.Checks = append(p.Checks, p.arrivalCheck(hotel, arrive, r))
	}
	if hasDeparture {
		if t := p.Components.TransferDeparture.Effective(); t != nil && t.Transfer != nil {
			p.Checks = append(p.Checks, transferCheck(t.Transfer, depart, r))
		}
		p.Checks = append(p.Checks, p.checkoutCheck(hotel, depart, r))
	}
}

func (p *TripPlan) datesCheck(arrive time.Time, hasArrival bool, depart time.Time, hasDeparture bool) Check {
	d := p.Constraints.Dates
	c := Check{Kind: CheckDatesConsistent, Status: "ok", Message: "Даты дороги совпадают с датами проживания.", Components: []string{Stay}}
	if hasArrival {
		c.Components = append([]string{Outbound}, c.Components...)
		if day := arrive.Format(time.DateOnly); day != d.Depart {
			c.Status = "warn"
			c.Message = fmt.Sprintf("Прибытие %s, а заселение %s: ", day, d.Depart)
			if day > d.Depart {
				c.Message += "первая ночь в отеле пропадёт."
			} else {
				c.Message += "ночь до заселения не покрыта отелем."
			}
		}
	}
	if hasDeparture {
		c.Components = append(c.Components, Return)
		if day := depart.Format(time.DateOnly); day != d.Return && c.Status == "ok" {
			c.Status = "warn"
			c.Message = fmt.Sprintf("Отправление обратно %s, а выезд из отеля %s.", day, d.Return)
		}
	}
	return c
}

func (p *TripPlan) arrivalCheck(h *Hotel, arrive time.Time, r ConnectionRules) Check {
	c := Check{Kind: CheckArrivalBeforeCheckin, Status: "ok", Components: []string{Outbound, Stay}}
	road := r.MinArrivalToCheckin
	if t := p.Components.TransferArrival.Effective(); t != nil && t.Transfer != nil {
		c.Components = []string{Outbound, TransferArrival, Stay}
		road = max(road, time.Duration(t.Transfer.DurationMinutes)*time.Minute)
	}
	door := arrive.Add(road)
	checkIn, ok := clockOn(p.Constraints.Dates.Depart, h.CheckInTime, arrive.Location())
	switch {
	case !ok:
		c.Message = fmt.Sprintf("Прибытие в %s, до отеля %d мин.", arrive.Format("15:04"), int(road.Minutes()))
	case door.Before(checkIn):
		c.Status = "warn"
		c.Message = fmt.Sprintf("Прибытие в %s, до отеля %d мин, заселение с %s: ожидание %s. Можно попросить ранний заезд.",
			arrive.Format("15:04"), int(road.Minutes()), h.CheckInTime, duration(checkIn.Sub(door)))
	default:
		c.Message = fmt.Sprintf("Прибытие в %s, в отеле к %s, заселение с %s.", arrive.Format("15:04"), door.Format("15:04"), h.CheckInTime)
	}
	return c
}

func transferCheck(t *Transfer, depart time.Time, r ConnectionRules) Check {
	c := Check{Kind: CheckTransferFits, Components: []string{Stay, TransferDeparture, Return}}
	leave, err := time.Parse(time.RFC3339, t.DepartAt)
	if err != nil {
		c.Status, c.Message = "warn", "Не удалось проверить время трансфера."
		return c
	}
	at := leave.Add(time.Duration(t.DurationMinutes) * time.Minute).In(depart.Location())
	spare := depart.Sub(at)
	switch {
	case spare >= r.DepartureBuffer:
		c.Status = "ok"
		c.Message = fmt.Sprintf("Трансфер к %s, за %s до отправления.", at.Format("15:04"), duration(spare))
	case spare >= 0:
		c.Status = "warn"
		c.Message = fmt.Sprintf("Трансфер к %s, всего за %s до отправления, нужно не меньше %s.", at.Format("15:04"), duration(spare), duration(r.DepartureBuffer))
	default:
		c.Status = "fail"
		c.Message = fmt.Sprintf("Трансфер прибывает в %s, позже отправления в %s.", at.Format("15:04"), depart.Format("15:04"))
	}
	return c
}

// checkoutCheck fails when the return leaves before the last night is over:
// the traveller would have to be on the way before the day of check-out.
func (p *TripPlan) checkoutCheck(h *Hotel, depart time.Time, r ConnectionRules) Check {
	c := Check{Kind: CheckCheckoutBeforeDeparture, Status: "ok", Components: []string{Stay, Return}}
	leave := depart.Add(-r.DepartureBuffer)
	if t := p.Components.TransferDeparture.Effective(); t != nil && t.Transfer != nil {
		if at, err := time.Parse(time.RFC3339, t.Transfer.DepartAt); err == nil {
			leave = at.In(depart.Location())
		}
	}
	dayStart, ok := clockOn(p.Constraints.Dates.Return, "00:00", depart.Location())
	switch {
	case ok && leave.Before(dayStart):
		c.Status = "fail"
		c.Message = fmt.Sprintf("Отправление обратно %s в %s: с дорогой выезжать придётся до дня выезда из отеля %s.",
			depart.Format(time.DateOnly), depart.Format("15:04"), p.Constraints.Dates.Return)
	default:
		c.Message = fmt.Sprintf("Выезд из отеля в %s (расчётный час %s), отправление в %s.", leave.Format("15:04"), h.CheckOutTime, depart.Format("15:04"))
	}
	return c
}

// clockOn is the wall clock hh:mm on a date in loc.
func clockOn(date, clock string, loc *time.Location) (time.Time, bool) {
	t, err := time.ParseInLocation(time.DateOnly+" 15:04", date+" "+clock, loc)
	return t, err == nil
}

func duration(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%d мин", m)
	case m%60 == 0:
		return fmt.Sprintf("%d ч", m/60)
	}
	return fmt.Sprintf("%d ч %d мин", m/60, m%60)
}
