package plan

import (
	"strings"
	"time"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
)

// Supplier entities as the external API shows them. Conversions from proto
// live next to them, so every proto field that reaches the user is mapped here.

type Source struct {
	Provider   string `json:"provider"`
	Kind       string `json:"kind"`
	FieldScope string `json:"field_scope"`
	URL        string `json:"url,omitempty"`
	License    string `json:"license,omitempty"`
	FetchedAt  string `json:"fetched_at,omitempty"`
}

type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Place struct {
	ID          string    `json:"id,omitempty"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	CountryCode string    `json:"country_code,omitempty"`
	Point       *GeoPoint `json:"point,omitempty"`
	Timezone    string    `json:"timezone,omitempty"`
	Code        string    `json:"code,omitempty"`
}

type Hotel struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	City               string   `json:"city"`
	Place              Place    `json:"place"`
	Stars              int32    `json:"stars"`
	Rating             float64  `json:"rating"`
	Meal               string   `json:"meal"`
	FreeCancellation   bool     `json:"free_cancellation"`
	CancellationPolicy string   `json:"cancellation_policy"`
	PetsAllowed        bool     `json:"pets_allowed"`
	CheckInTime        string   `json:"check_in_time"`
	CheckOutTime       string   `json:"check_out_time"`
	PricePerNightRub   int64    `json:"price_per_night_rub"`
	TotalPriceRub      int64    `json:"total_price_rub"`
	DistanceToCenterKm float64  `json:"distance_to_center_km"`
	Sources            []Source `json:"sources"`
}

type Journey struct {
	ID              string    `json:"id"`
	Segments        []Segment `json:"segments"`
	DurationMinutes int32     `json:"duration_minutes"`
	Transfers       int32     `json:"transfers"`
	BaggageIncluded bool      `json:"baggage_included"`
	Refundable      bool      `json:"refundable"`
	PriceRub        int64     `json:"price_rub"`
	Sources         []Source  `json:"sources"`
}

type Segment struct {
	Mode     string `json:"mode"`
	Carrier  string `json:"carrier"`
	Number   string `json:"number"`
	From     Place  `json:"from"`
	To       Place  `json:"to"`
	DepartAt string `json:"depart_at"`
	ArriveAt string `json:"arrive_at"`
}

type Transfer struct {
	ID              string   `json:"id"`
	Mode            string   `json:"mode"`
	From            Place    `json:"from"`
	To              Place    `json:"to"`
	DepartAt        string   `json:"depart_at"`
	DurationMinutes int32    `json:"duration_minutes"`
	PriceRub        int64    `json:"price_rub"`
	Sources         []Source `json:"sources"`
}

type Destination struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Country           string          `json:"country"`
	CountryCode       string          `json:"country_code"`
	Place             Place           `json:"place"`
	Tags              []string        `json:"tags"`
	EstimatedTotalRub int64           `json:"estimated_total_rub"`
	TravelHours       float64         `json:"travel_hours"`
	Weather           *Weather        `json:"weather,omitempty"`
	Visa              VisaRequirement `json:"visa"`
	Sources           []Source        `json:"sources"`
}

// Visa statuses, see VisaStatus in api/openapi.yaml.
const (
	VisaHome        = "home"
	VisaFree        = "visa_free"
	VisaOnArrival   = "visa_on_arrival"
	VisaETA         = "eta"
	VisaElectronic  = "e_visa"
	VisaRequired    = "visa_required"
	VisaNoAdmission = "no_admission"
)

type VisaRequirement struct {
	Citizenship string   `json:"citizenship"`
	CountryCode string   `json:"country_code"`
	Status      string   `json:"status"`
	MaxStayDays int32    `json:"max_stay_days,omitempty"`
	Sources     []Source `json:"sources"`
}

// VisaFree reports whether the status lets the traveller in without arranging
// anything before the trip.
func (v VisaRequirement) VisaFree() bool {
	return v.Status == VisaHome || v.Status == VisaFree || v.Status == VisaOnArrival
}

type Weather struct {
	Place           Place    `json:"place"`
	Date            string   `json:"date"`
	TempMaxC        float64  `json:"temp_max_c"`
	TempMinC        float64  `json:"temp_min_c"`
	PrecipitationMM float64  `json:"precipitation_mm"`
	Kind            string   `json:"kind"`
	Sources         []Source `json:"sources"`
}

// enumName turns a proto enum name into the API value: KIND_REAL -> real.
func enumName(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

func SourcesFromProto(in []*commonv1.Source) []Source {
	out := make([]Source, 0, len(in))
	for _, s := range in {
		src := Source{
			Provider: s.GetProvider(), Kind: enumName(s.GetKind().String(), "KIND_"), FieldScope: s.GetFieldScope(),
			URL: s.GetUrl(), License: s.GetLicense(),
		}
		if s.GetFetchedAt() != nil {
			src.FetchedAt = s.GetFetchedAt().AsTime().UTC().Format(time.RFC3339)
		}
		out = append(out, src)
	}
	return out
}

func PlaceFromProto(p *commonv1.Place) Place {
	out := Place{
		ID: p.GetId(), Name: p.GetName(), Kind: enumName(p.GetKind().String(), "KIND_"),
		CountryCode: p.GetCountryCode(), Timezone: p.GetTimezone(), Code: p.GetCode(),
	}
	if pt := p.GetPoint(); pt != nil {
		out.Point = &GeoPoint{Lat: pt.GetLat(), Lon: pt.GetLon()}
	}
	return out
}

func HotelFromProto(h *lodgingv1.Hotel) *Hotel {
	return &Hotel{
		ID: h.GetId(), Name: h.GetName(), City: h.GetCity(), Place: PlaceFromProto(h.GetPlace()),
		Stars: h.GetStars(), Rating: h.GetRating(), Meal: h.GetMeal(), FreeCancellation: h.GetFreeCancellation(),
		CancellationPolicy: h.GetCancellationPolicy(), PetsAllowed: h.GetPetsAllowed(),
		CheckInTime: h.GetCheckInTime(), CheckOutTime: h.GetCheckOutTime(),
		PricePerNightRub: h.GetPricePerNightRub(), TotalPriceRub: h.GetTotalPriceRub(),
		DistanceToCenterKm: h.GetDistanceToCenterKm(), Sources: SourcesFromProto(h.GetSources()),
	}
}

func JourneyFromProto(j *transportv1.Journey) *Journey {
	out := &Journey{
		ID: j.GetId(), DurationMinutes: j.GetDurationMinutes(), Transfers: j.GetTransfers(),
		BaggageIncluded: j.GetBaggageIncluded(), Refundable: j.GetRefundable(), PriceRub: j.GetPriceRub(),
		Sources: SourcesFromProto(j.GetSources()), Segments: []Segment{},
	}
	for _, s := range j.GetSegments() {
		out.Segments = append(out.Segments, Segment{
			Mode: enumName(s.GetMode().String(), "MODE_"), Carrier: s.GetCarrier(), Number: s.GetNumber(),
			From: PlaceFromProto(s.GetFrom()), To: PlaceFromProto(s.GetTo()), DepartAt: s.GetDepartAt(), ArriveAt: s.GetArriveAt(),
		})
	}
	return out
}

func TransferFromProto(t *transferv1.Transfer) *Transfer {
	return &Transfer{
		ID: t.GetId(), Mode: enumName(t.GetMode().String(), "MODE_"), From: PlaceFromProto(t.GetFrom()),
		To: PlaceFromProto(t.GetTo()), DepartAt: t.GetDepartAt(), DurationMinutes: t.GetDurationMinutes(),
		PriceRub: t.GetPriceRub(), Sources: SourcesFromProto(t.GetSources()),
	}
}

func VisaFromProto(v *catalogv1.VisaRequirement) *VisaRequirement {
	if v == nil {
		return nil
	}
	return &VisaRequirement{
		Citizenship: v.GetCitizenship(), CountryCode: v.GetCountryCode(),
		Status: enumName(v.GetStatus().String(), "STATUS_"), MaxStayDays: v.GetMaxStayDays(),
		Sources: SourcesFromProto(v.GetSources()),
	}
}

func WeatherFromProto(w *catalogv1.Weather) *Weather {
	return &Weather{
		Place: PlaceFromProto(w.GetPlace()), Date: w.GetDate(), TempMaxC: w.GetTempMaxC(), TempMinC: w.GetTempMinC(),
		PrecipitationMM: w.GetPrecipitationMm(), Kind: enumName(w.GetKind().String(), "KIND_"),
		Sources: SourcesFromProto(w.GetSources()),
	}
}

// StayOption wraps a hotel offer into a stay option.
func StayOption(h *lodgingv1.Hotel) Option {
	hotel := HotelFromProto(h)
	return Option{ID: hotel.ID, Kind: KindStay, PriceRub: hotel.TotalPriceRub, Stay: hotel, Flags: []string{}, Sources: hotel.Sources}
}

// JourneyOption wraps a journey offer into an outbound or return option.
func JourneyOption(j *transportv1.Journey) Option {
	journey := JourneyFromProto(j)
	return Option{ID: journey.ID, Kind: KindJourney, PriceRub: journey.PriceRub, Journey: journey, Flags: []string{}, Sources: journey.Sources}
}

// TransferOption wraps a transfer offer into a transfer option.
func TransferOption(t *transferv1.Transfer) Option {
	transfer := TransferFromProto(t)
	return Option{ID: transfer.ID, Kind: KindTransfer, PriceRub: transfer.PriceRub, Transfer: transfer, Flags: []string{}, Sources: transfer.Sources}
}

// TravelersProto returns the party of the plan in supplier terms.
func (c Constraints) TravelersProto() *commonv1.Travelers {
	return &commonv1.Travelers{Adults: c.Adults, ChildAges: c.ChildAges, Pets: c.Pets}
}

// StayProto returns the stay of the plan in supplier terms.
func (c Constraints) StayProto() *lodgingv1.Stay {
	return &lodgingv1.Stay{CheckIn: c.Dates.Depart, Nights: c.Dates.Nights, Travelers: c.TravelersProto()}
}
