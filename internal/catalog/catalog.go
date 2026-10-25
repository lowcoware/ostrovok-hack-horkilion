// Package catalog implements catalogv1.CatalogService providers.
package catalog

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
	"horkilion/internal/dataset"
)

// OpenData is the provider "open-data": destinations, visas, countries and
// places from the data snapshot, forecasts from Open-Meteo.
type OpenData struct {
	catalogv1.UnimplementedCatalogServiceServer
	weather   *Weather
	data      *dataset.Data
	countries []*catalogv1.Country
	routes    map[string]dataset.Route
	rail      map[string]dataset.RailRoute
}

func NewOpenData(data *dataset.Data) *OpenData {
	s := &OpenData{weather: NewWeather(), data: data, routes: map[string]dataset.Route{}, rail: map[string]dataset.RailRoute{}}
	for _, c := range data.Countries {
		s.countries = append(s.countries, &catalogv1.Country{Code: c.Code, NameRu: c.NameRu, NameEn: c.NameEn})
	}
	slices.SortFunc(s.countries, func(a, b *catalogv1.Country) int {
		if (a.Code == "RU") != (b.Code == "RU") {
			if a.Code == "RU" {
				return -1
			}
			return 1
		}
		return strings.Compare(fold(a.NameRu), fold(b.NameRu))
	})
	// Routes and rail routes work both ways.
	for _, r := range data.Routes {
		s.routes[r.FromIATA+"/"+r.ToIATA] = r
		if _, ok := s.routes[r.ToIATA+"/"+r.FromIATA]; !ok {
			s.routes[r.ToIATA+"/"+r.FromIATA] = dataset.Route{FromIATA: r.ToIATA, ToIATA: r.FromIATA, DistanceKm: r.DistanceKm, Direct: r.Direct}
		}
	}
	for _, r := range data.RailRoutes {
		s.rail[r.FromStation+"/"+r.ToStation] = r
		if _, ok := s.rail[r.ToStation+"/"+r.FromStation]; !ok {
			s.rail[r.ToStation+"/"+r.FromStation] = dataset.RailRoute{FromStation: r.ToStation, ToStation: r.FromStation,
				DurationMin: r.DurationMin, Departures: r.Departures, TrainNumbers: r.TrainNumbers}
		}
	}
	return s
}

func (s *OpenData) GetWeather(ctx context.Context, req *catalogv1.GetWeatherRequest) (*catalogv1.Weather, error) {
	date, err := time.Parse(time.DateOnly, req.GetDate())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "date must be YYYY-MM-DD")
	}
	var place Place
	if pt := req.GetPoint(); pt != nil {
		place = Place{Name: strings.TrimSpace(req.GetCity()), Latitude: pt.GetLat(), Longitude: pt.GetLon()}
	} else {
		city := strings.TrimSpace(req.GetCity())
		if city == "" || utf8.RuneCountInString(city) > 100 {
			return nil, status.Error(codes.InvalidArgument, "city must be 1 to 100 characters, or point must be set")
		}
		place, err = s.weather.Geocode(ctx, city)
		if err != nil {
			slog.Warn("geocode failed", "city", city, "err", err)
			return nil, status.Error(codes.NotFound, err.Error())
		}
	}
	day, err := s.weather.DayWeather(ctx, place, date)
	if err != nil {
		slog.Warn("weather failed", "place", place.Name, "err", err)
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	kind, url := catalogv1.Weather_KIND_FORECAST, "https://open-meteo.com/en/docs"
	if day.Source == "last_year" {
		// impulse: the climate estimate is the same day last year, average several years from the archive when a single odd year misleads the agent.
		kind, url = catalogv1.Weather_KIND_CLIMATE_NORMAL, "https://open-meteo.com/en/docs/historical-weather-api"
	}
	return &catalogv1.Weather{
		Place: &commonv1.Place{
			Name: place.Name, Kind: commonv1.Place_KIND_CITY,
			Point: &commonv1.GeoPoint{Lat: place.Latitude, Lon: place.Longitude},
		},
		Date:            req.GetDate(),
		TempMaxC:        day.TempMaxC,
		TempMinC:        day.TempMinC,
		PrecipitationMm: day.PrecipitationMM,
		Kind:            kind,
		Sources: []*commonv1.Source{{
			Provider: "open-meteo", Kind: commonv1.Source_KIND_REAL, FieldScope: "*", Url: url,
			License: "CC-BY-4.0", FetchedAt: timestamppb.Now(),
		}},
	}, nil
}
