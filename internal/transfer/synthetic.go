package transfer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "horkilion/gen/common/v1"
	transferv1 "horkilion/gen/transfer/v1"
	"horkilion/internal/dataset"
	"horkilion/internal/synth"
)

// Synthetic is the provider "synthetic": taxi, shuttle and public transport
// between an airport or a station and a hotel of the data snapshot, timed by
// distance and priced by the calibration tariffs. A transfer id encodes the
// mode, both places and the departure, so GetOffer rebuilds the same offer.
type Synthetic struct {
	transferv1.UnimplementedTransferServiceServer
	data *dataset.Data
}

func NewSynthetic(data *dataset.Data) *Synthetic { return &Synthetic{data: data} }

const (
	// Roads are longer than the great circle.
	roadFactor = 1.3
	// Shuttles run only from airports to hotels this close.
	maxShuttleKm = 60
)

// point is a resolved end of a transfer.
type point struct {
	place   *commonv1.Place
	country string
	airport bool
}

func (s *Synthetic) SearchTransfers(_ context.Context, req *transferv1.SearchTransfersRequest) (*transferv1.SearchTransfersResponse, error) {
	from, to, err := s.ends(req.GetFrom().GetId(), req.GetTo().GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	depart, err := time.Parse(time.RFC3339, req.GetDepartAt())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "depart_at must be RFC 3339")
	}
	party, err := synth.ParseParty(req.GetPassengers())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "passengers."+err.Error())
	}
	// Same zone as GetOffer rebuilds it in, so both print the same depart_at.
	depart = depart.In(zone(from.place.GetTimezone()))
	return &transferv1.SearchTransfersResponse{Transfers: s.offers(from, to, depart, party)}, nil
}

func (s *Synthetic) GetOffer(_ context.Context, req *transferv1.GetOfferRequest) (*transferv1.Transfer, error) {
	party, err := synth.ParseParty(req.GetPassengers())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "passengers."+err.Error())
	}
	// mode:from:to:unix seconds; the offset of depart_at is the zone of the from place.
	parts := strings.Split(req.GetTransferId(), ":")
	if len(parts) != 4 {
		return nil, status.Error(codes.NotFound, "unknown transfer id")
	}
	from, to, err := s.ends(parts[1], parts[2])
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown transfer id")
	}
	sec, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown transfer id")
	}
	depart := time.Unix(sec, 0).In(zone(from.place.GetTimezone()))
	for _, t := range s.offers(from, to, depart, party) {
		if t.Id == req.GetTransferId() {
			return t, nil
		}
	}
	return nil, status.Error(codes.NotFound, "unknown transfer id")
}

// ends resolves both places; one must be an airport or a station, the other a hotel.
func (s *Synthetic) ends(fromID, toID string) (point, point, error) {
	from, ok := s.resolve(fromID)
	if !ok {
		return point{}, point{}, fmt.Errorf("unknown from.id %q", fromID)
	}
	to, ok := s.resolve(toID)
	if !ok {
		return point{}, point{}, fmt.Errorf("unknown to.id %q", toID)
	}
	fromHotel, toHotel := from.place.GetKind() == commonv1.Place_KIND_HOTEL, to.place.GetKind() == commonv1.Place_KIND_HOTEL
	if fromHotel == toHotel {
		return point{}, point{}, fmt.Errorf("a transfer goes between a hotel and an airport or a station")
	}
	return from, to, nil
}

// resolve finds a place of the snapshot by its id: an IATA code, a station
// code or a hotel id.
// impulse: transfers resolve only snapshot places by id, accept bare points when the agent needs transfers to addresses.
func (s *Synthetic) resolve(id string) (point, bool) {
	if a := s.data.AirportByIATA[id]; a != nil {
		return point{place: &commonv1.Place{
			Id: a.IATA, Name: a.Name, Kind: commonv1.Place_KIND_AIRPORT, CountryCode: a.CountryCode,
			Point: &commonv1.GeoPoint{Lat: a.Lat, Lon: a.Lon}, Timezone: a.Timezone, Code: a.IATA,
		}, country: a.CountryCode, airport: true}, true
	}
	if st := s.data.StationByCode[id]; st != nil {
		c := s.data.CityByID[st.CityID]
		return point{place: &commonv1.Place{
			Id: st.Code, Name: st.Name, Kind: commonv1.Place_KIND_STATION, CountryCode: c.CountryCode,
			Point: &commonv1.GeoPoint{Lat: st.Lat, Lon: st.Lon}, Timezone: c.Timezone, Code: st.Code,
		}, country: c.CountryCode}, true
	}
	if h := s.data.HotelByID[id]; h != nil {
		d := s.data.DestinationByID[h.DestinationID]
		c := s.data.CityByID[d.CityID]
		return point{place: &commonv1.Place{
			Id: h.ID, Name: h.Name, Kind: commonv1.Place_KIND_HOTEL, CountryCode: d.CountryCode,
			Point: &commonv1.GeoPoint{Lat: h.Lat, Lon: h.Lon}, Timezone: c.Timezone,
		}, country: d.CountryCode}, true
	}
	return point{}, false
}

// offers are taxi and public transport for every pair, and a shuttle between
// a nearby airport and a hotel when no pet travels.
func (s *Synthetic) offers(from, to point, depart time.Time, p synth.Party) []*transferv1.Transfer {
	cal := s.data.Calibration
	a, b := from.place.GetPoint(), to.place.GetPoint()
	km := synth.Haversine(a.GetLat(), a.GetLon(), b.GetLat(), b.GetLon()) * roadFactor
	country := to.country
	if to.place.GetKind() != commonv1.Place_KIND_HOTEL {
		country = from.country
	}
	type option struct {
		mode    transferv1.Mode
		code    string
		minutes int
		price   int64
	}
	opts := []option{{transferv1.Mode_MODE_TAXI, "taxi", synth.TaxiMinutes(km), synth.TaxiRub(cal, country, km, p)}}
	if (from.airport || to.airport) && km <= maxShuttleKm && !p.Pets {
		opts = append(opts, option{transferv1.Mode_MODE_SHUTTLE, "shuttle", synth.ShuttleMinutes(km), synth.ShuttleRub(cal, country, p)})
	}
	opts = append(opts, option{transferv1.Mode_MODE_PUBLIC_TRANSPORT, "public", synth.PublicMinutes(km), synth.PublicRub(cal, country, p)})

	out := make([]*transferv1.Transfer, 0, len(opts))
	for _, o := range opts {
		out = append(out, &transferv1.Transfer{
			Id:   fmt.Sprintf("%s:%s:%s:%d", o.code, from.place.GetId(), to.place.GetId(), depart.Unix()),
			Mode: o.mode, From: from.place, To: to.place,
			DepartAt: depart.Format(time.RFC3339), DurationMinutes: int32(o.minutes), PriceRub: o.price,
			Sources: synth.Sources(s.data, "duration,price", "synthetic"),
		})
	}
	return out
}

func zone(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}
