package plan

import (
	"fmt"
	"slices"
	"strings"

	catalogv1 "horkilion/gen/catalog/v1"
)

// MaxDestinationOptions is how many destinations inspire offers at once.
const MaxDestinationOptions = 5

func DestinationFromProto(d *catalogv1.Destination) Destination {
	out := Destination{
		ID: d.GetId(), Name: d.GetName(), Country: d.GetCountry(), CountryCode: d.GetCountryCode(),
		Place: PlaceFromProto(d.GetPlace()), Tags: nonNil(d.GetTags()), EstimatedTotalRub: d.GetEstimatedTotalRub(),
		TravelHours: d.GetTravelHours(), Sources: SourcesFromProto(d.GetSources()),
	}
	if w := d.GetWeather(); w != nil {
		out.Weather = WeatherFromProto(w)
	}
	if v := VisaFromProto(d.GetVisa()); v != nil {
		out.Visa = *v
	}
	return out
}

var tagNames = map[string]string{
	"sea": "море", "warm": "тепло", "kids": "подходит детям", "nature": "природа", "mountains": "горы", "city": "город",
}

// SetDestinationOptions stores the destinations inspire found and explains
// each one by rules from the plan alone: why it fits, what it costs the
// traveller, what can go wrong.
func (p *TripPlan) SetDestinationOptions(ds []Destination) {
	c := p.Constraints
	opts := make([]DestinationOption, 0, min(len(ds), MaxDestinationOptions))
	for _, d := range ds[:min(len(ds), MaxDestinationOptions)] {
		e := &Explanation{Tradeoffs: []string{}, Risks: []string{}}

		var why []string
		for _, m := range c.Preferences.Moods {
			if slices.Contains(d.Tags, m) && tagNames[m] != "" {
				why = append(why, tagNames[m])
			}
		}
		if len(why) == 0 {
			for _, t := range d.Tags {
				if tagNames[t] != "" {
					why = append(why, tagNames[t])
				}
			}
		}
		line := strings.Join(why, ", ")
		if w := d.Weather; w != nil {
			line += fmt.Sprintf("; днём до %.0f °C", w.TempMaxC)
		}
		e.Why = fmt.Sprintf("%s. Поездка целиком около %d ₽, в пути %.1f ч.", capitalize(line), d.EstimatedTotalRub, d.TravelHours)

		if b := c.Budget; b != nil && d.EstimatedTotalRub > b.AmountRub {
			e.Tradeoffs = append(e.Tradeoffs, fmt.Sprintf("дороже бюджета на %d ₽", d.EstimatedTotalRub-b.AmountRub))
		}
		if h := c.Preferences.MaxTravelHours; h > 0 && d.TravelHours > h {
			e.Tradeoffs = append(e.Tradeoffs, fmt.Sprintf("в пути дольше %.0f ч", h))
		}
		for _, m := range c.Preferences.Moods {
			if !slices.Contains(d.Tags, m) && tagNames[m] != "" {
				e.Tradeoffs = append(e.Tradeoffs, "нет: "+tagNames[m])
			}
		}
		if msg := visaRisk(d.Visa); msg != "" {
			e.Risks = append(e.Risks, msg)
		}
		if w := d.Weather; w != nil && w.Kind == "climate_normal" {
			e.Risks = append(e.Risks, "погода по климатической норме месяца, не прогноз")
		}
		opts = append(opts, DestinationOption{Destination: d, Explanation: e})
	}
	p.DestinationOptions = opts
}

func visaRisk(v VisaRequirement) string {
	switch v.Status {
	case VisaETA, VisaElectronic, VisaRequired:
		return visaMessages[v.Status]
	case VisaOnArrival:
		return "виза оформляется на границе по прилёту"
	}
	return ""
}

func capitalize(s string) string {
	for i, r := range s {
		return strings.ToUpper(string(r)) + s[i+len(string(r)):]
	}
	return s
}
