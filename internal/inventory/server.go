package inventory

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "horkilion/gen/inventory/v1"
)

// Server implements inventoryv1.InventoryServiceServer.
type Server struct {
	inventoryv1.UnimplementedInventoryServiceServer
	weather *Weather
}

func NewServer(weather *Weather) *Server {
	return &Server{weather: weather}
}

func (s *Server) SearchHotels(_ context.Context, req *inventoryv1.SearchHotelsRequest) (*inventoryv1.SearchHotelsResponse, error) {
	city, err := NormalizeCity(req.GetCity())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	stay, err := ParseStay(req.GetStay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &inventoryv1.SearchHotelsResponse{Hotels: SearchHotels(city, stay)}, nil
}

func (s *Server) GetOffer(_ context.Context, req *inventoryv1.GetOfferRequest) (*inventoryv1.Hotel, error) {
	stay, err := ParseStay(req.GetStay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	h, err := Offer(req.GetHotelId(), stay)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return h, nil
}

func (s *Server) GetWeather(ctx context.Context, req *inventoryv1.GetWeatherRequest) (*inventoryv1.GetWeatherResponse, error) {
	city, err := NormalizeCity(req.GetCity())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	date, err := time.Parse(time.DateOnly, req.GetDate())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "date must be YYYY-MM-DD")
	}
	place, err := s.weather.Geocode(ctx, city)
	if err != nil {
		slog.Warn("geocode failed", "city", city, "err", err)
		return nil, status.Error(codes.NotFound, err.Error())
	}
	day, err := s.weather.DayWeather(ctx, place, date)
	if err != nil {
		slog.Warn("weather failed", "city", city, "err", err)
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &inventoryv1.GetWeatherResponse{
		City:            place.Name,
		Latitude:        place.Latitude,
		Longitude:       place.Longitude,
		TempMaxC:        day.TempMaxC,
		TempMinC:        day.TempMinC,
		PrecipitationMm: day.PrecipitationMM,
		Source:          day.Source,
	}, nil
}
