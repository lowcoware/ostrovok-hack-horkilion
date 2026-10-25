// Package catalog implements catalogv1.CatalogService providers.
package catalog

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
)

// OpenData is the provider "open-data": weather from Open-Meteo. Destinations
// from open datasets come in phase 1.
type OpenData struct {
	catalogv1.UnimplementedCatalogServiceServer
	weather *Weather
}

func NewOpenData() *OpenData {
	return &OpenData{weather: NewWeather()}
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
