package catalog

import (
	"cmp"
	"context"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
	"horkilion/internal/dataset"
	"horkilion/internal/synth"
)

var (
	reCountry = regexp.MustCompile(`^[A-Z]{2}$`)

	visaStatuses = map[string]catalogv1.VisaRequirement_Status{
		"home":            catalogv1.VisaRequirement_STATUS_HOME,
		"visa_free":       catalogv1.VisaRequirement_STATUS_VISA_FREE,
		"visa_on_arrival": catalogv1.VisaRequirement_STATUS_VISA_ON_ARRIVAL,
		"eta":             catalogv1.VisaRequirement_STATUS_ETA,
		"e_visa":          catalogv1.VisaRequirement_STATUS_E_VISA,
		"visa_required":   catalogv1.VisaRequirement_STATUS_VISA_REQUIRED,
		"no_admission":    catalogv1.VisaRequirement_STATUS_NO_ADMISSION,
	}
)

const (
	defaultCitizenship  = "RU"
	defaultNights       = 3
	defaultDestinations = 5
	maxDestinations     = 20
	defaultPlaces       = 10
	maxPlaces           = 20
	// Typical distance from an airport or a station to a hotel, for estimates.
	estimateTransferKm = 20
)

// fold makes names comparable: lower case, ё as е.
func fold(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
}

func (s *OpenData) ListCountries(context.Context, *catalogv1.ListCountriesRequest) (*catalogv1.ListCountriesResponse, error) {
	return &catalogv1.ListCountriesResponse{Countries: s.countries}, nil
}

func (s *OpenData) GetVisaRequirement(_ context.Context, req *catalogv1.GetVisaRequirementRequest) (*catalogv1.VisaRequirement, error) {
	cit, cc := req.GetCitizenship(), req.GetCountryCode()
	if !reCountry.MatchString(cit) || s.data.CountryByCode[cit] == nil {
		return nil, status.Errorf(codes.InvalidArgument, "unknown citizenship %q", cit)
	}
	if !reCountry.MatchString(cc) || s.data.CountryByCode[cc] == nil {
		return nil, status.Errorf(codes.InvalidArgument, "unknown country %q", cc)
	}
	v, ok := s.visa(cit, cc)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no visa data for %s passport to %s", cit, cc)
	}
	return v, nil
}

// visa returns the requirement for a passport and a country. A passport of
// the country itself is "home" even when the data has no row for it.
func (s *OpenData) visa(citizenship, country string) (*catalogv1.VisaRequirement, bool) {
	row, ok := s.data.Visa(citizenship, country)
	if !ok {
		if citizenship != country {
			return nil, false
		}
		row = &dataset.Visa{Passport: citizenship, Destination: country, Status: "home", Sources: []string{"passport-index-data"}}
	}
	return &catalogv1.VisaRequirement{
		Citizenship: citizenship, CountryCode: country, Status: visaStatuses[row.Status], MaxStayDays: row.Days,
		Sources: synth.Sources(s.data, "visa", row.Sources...),
	}, true
}

func (s *OpenData) SearchPlaces(_ context.Context, req *catalogv1.SearchPlacesRequest) (*catalogv1.SearchPlacesResponse, error) {
	q := fold(req.GetQuery())
	if n := utf8.RuneCountInString(q); n < 2 || n > 100 {
		return nil, status.Error(codes.InvalidArgument, "query must be 2 to 100 characters")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultPlaces
	}
	limit = min(limit, maxPlaces)
	kinds := req.GetKinds()
	wantCities := len(kinds) == 0 || slices.Contains(kinds, commonv1.Place_KIND_CITY)
	wantAirports := slices.Contains(kinds, commonv1.Place_KIND_AIRPORT)

	var cities []*dataset.City
	if wantCities {
		for i := range s.data.Cities {
			c := &s.data.Cities[i]
			if strings.HasPrefix(fold(c.NameRu), q) || strings.HasPrefix(fold(c.NameEn), q) {
				cities = append(cities, c)
			}
		}
		slices.SortFunc(cities, func(a, b *dataset.City) int { return cmp.Compare(b.Population, a.Population) })
	}
	out := &catalogv1.SearchPlacesResponse{}
	for _, c := range cities {
		out.Places = append(out.Places, s.cityPlace(c))
	}
	if wantAirports {
		for _, a := range s.data.Airports {
			if strings.HasPrefix(fold(a.IATA), q) || strings.HasPrefix(fold(a.Name), q) {
				out.Places = append(out.Places, airportPlace(a))
			}
		}
	}
	if len(out.Places) > limit {
		out.Places = out.Places[:limit]
	}
	return out, nil
}

func (s *OpenData) cityPlace(c *dataset.City) *commonv1.Place {
	return &commonv1.Place{
		Id: c.ID, Name: c.NameRu, Kind: commonv1.Place_KIND_CITY, CountryCode: c.CountryCode,
		Point: &commonv1.GeoPoint{Lat: c.Lat, Lon: c.Lon}, Timezone: c.Timezone,
	}
}

func airportPlace(a dataset.Airport) *commonv1.Place {
	return &commonv1.Place{
		Id: a.IATA, Name: a.Name, Kind: commonv1.Place_KIND_AIRPORT, CountryCode: a.CountryCode,
		Point: &commonv1.GeoPoint{Lat: a.Lat, Lon: a.Lon}, Timezone: a.Timezone, Code: a.IATA,
	}
}

// findCity resolves a departure city by id or by its Russian or English name.
func (s *OpenData) findCity(name string) *dataset.City {
	q := fold(name)
	if c := s.data.CityByID[q]; c != nil {
		return c
	}
	for i := range s.data.Cities {
		if c := &s.data.Cities[i]; fold(c.NameRu) == q || fold(c.NameEn) == q {
			return c
		}
	}
	return nil
}

// leg is the fastest way between two cities one way.
type leg struct {
	minutes int
	fare    func(date time.Time) int64
}

func (s *OpenData) fastest(from *dataset.City, to *dataset.Destination, p synth.Party) (leg, bool) {
	best, found := leg{}, false
	cal := s.data.Calibration
	for _, a := range from.Airports {
		for _, b := range to.Airports {
			if r, ok := s.routes[a+"/"+b]; ok {
				m, _ := synth.Air(cal, r)
				if !found || m < best.minutes {
					best, found = leg{m, func(d time.Time) int64 { return synth.AirFareRub(cal, r, p, d) }}, true
				}
			}
		}
	}
	for _, a := range from.Stations {
		for _, b := range to.Stations {
			if r, ok := s.rail[a+"/"+b]; ok {
				if !found || r.DurationMin < best.minutes {
					best, found = leg{r.DurationMin, func(d time.Time) int64 {
						return synth.RailFareRub(cal, r, r.TrainNumbers[0], p, d)
					}}, true
				}
			}
		}
	}
	return best, found
}

func (s *OpenData) SearchDestinations(_ context.Context, req *catalogv1.SearchDestinationsRequest) (*catalogv1.SearchDestinationsResponse, error) {
	cit := cmp.Or(req.GetCitizenship(), defaultCitizenship)
	if !reCountry.MatchString(cit) || s.data.CountryByCode[cit] == nil {
		return nil, status.Errorf(codes.InvalidArgument, "unknown citizenship %q", cit)
	}
	origin := s.findCity(req.GetOrigin())
	if origin == nil {
		return nil, status.Errorf(codes.InvalidArgument, "unknown origin city %q", req.GetOrigin())
	}
	depart, nights, err := tripDates(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	party := synth.PartyFromProto(req.GetTravelers())
	if party.Adults < 1 {
		party.Adults = 2
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultDestinations
	}
	limit = min(limit, maxDestinations)

	var out []*catalogv1.Destination
	for i := range s.data.Destinations {
		d := &s.data.Destinations[i]
		if d.CityID == origin.ID || !hasAll(d.Tags, req.GetTags()) {
			continue
		}
		visa, ok := s.visa(cit, d.CountryCode)
		if !ok || visa.Status == catalogv1.VisaRequirement_STATUS_NO_ADMISSION {
			continue
		}
		if req.GetVisaFreeOnly() && !visaFree(visa.Status) {
			continue
		}
		way, ok := s.fastest(origin, d, party)
		if !ok {
			continue
		}
		hours := math.Round(float64(way.minutes)/6) / 10
		if limitHours := req.GetMaxTravelHours(); limitHours > 0 && hours > limitHours {
			continue
		}
		total := s.estimate(d, way, party, depart, nights)
		if b := req.GetBudgetRub(); b > 0 && total > b {
			continue
		}
		city := s.data.CityByID[d.CityID]
		out = append(out, &catalogv1.Destination{
			Id: d.ID, Name: d.Name, Country: s.data.CountryByCode[d.CountryCode].NameRu, CountryCode: d.CountryCode,
			Place: s.cityPlace(city), Tags: d.Tags, EstimatedTotalRub: total, TravelHours: hours,
			Weather: s.climate(d, city, depart), Sources: synth.Sources(s.data, "*", d.Sources...), Visa: visa,
		})
	}
	slices.SortFunc(out, func(a, b *catalogv1.Destination) int {
		return cmp.Or(cmp.Compare(a.EstimatedTotalRub, b.EstimatedTotalRub), cmp.Compare(a.TravelHours, b.TravelHours))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return &catalogv1.SearchDestinationsResponse{Destinations: out}, nil
}

// estimate is the whole-trip price: the fastest way there and back, a
// three-star hotel with tourist tax and a taxi each way.
func (s *OpenData) estimate(d *dataset.Destination, way leg, p synth.Party, depart time.Time, nights int32) int64 {
	cal := s.data.Calibration
	back := depart.AddDate(0, 0, int(nights))
	stay := synth.BedRub(cal, 3, d.CountryCode, depart) * p.GuestWeight() * float64(nights)
	taxi := synth.TaxiRub(cal, d.CountryCode, estimateTransferKm, p)
	return way.fare(depart) + way.fare(back) + synth.RoundRub(stay) + synth.TouristTaxRub(cal, d.CityID, p, nights) + 2*taxi
}

// climate is the weather of the departure month from the climate norms.
// impulse: search results carry climate norms only, add the Open-Meteo forecast for departures within 14 days when users compare near trips by weather.
func (s *OpenData) climate(d *dataset.Destination, city *dataset.City, depart time.Time) *catalogv1.Weather {
	c := s.data.ClimateByDest[d.ID][depart.Month()-1]
	if c == nil {
		return nil
	}
	return &catalogv1.Weather{
		Place: s.cityPlace(city), Date: depart.Format(time.DateOnly), TempMaxC: c.TempMaxC, TempMinC: c.TempMinC,
		PrecipitationMm: c.PrecipitationMM, Kind: catalogv1.Weather_KIND_CLIMATE_NORMAL,
		Sources: synth.Sources(s.data, "weather", "open-meteo"),
	}
}

func tripDates(req *catalogv1.SearchDestinationsRequest) (time.Time, int32, error) {
	var depart time.Time
	var err error
	switch {
	case req.GetDepartDate() != "":
		if depart, err = time.Parse(time.DateOnly, req.GetDepartDate()); err != nil {
			return depart, 0, err
		}
	case req.GetMonth() != "":
		if depart, err = time.Parse("2006-01", req.GetMonth()); err != nil {
			return depart, 0, err
		}
	default:
		return depart, 0, errors.New("depart_date or month is required")
	}
	nights := int32(defaultNights)
	if req.GetReturnDate() != "" {
		back, err := time.Parse(time.DateOnly, req.GetReturnDate())
		if err != nil {
			return depart, 0, err
		}
		if n := int32(back.Sub(depart).Hours() / 24); n >= 1 && n <= 30 {
			nights = n
		}
	}
	return depart, nights, nil
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

func visaFree(s catalogv1.VisaRequirement_Status) bool {
	return s == catalogv1.VisaRequirement_STATUS_HOME || s == catalogv1.VisaRequirement_STATUS_VISA_FREE ||
		s == catalogv1.VisaRequirement_STATUS_VISA_ON_ARRIVAL
}
