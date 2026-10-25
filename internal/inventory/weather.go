package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// forecastDays is how far ahead the Open-Meteo forecast reaches reliably.
const forecastDays = 14

// Weather fetches geocoding, forecasts and historical weather from Open-Meteo.
type Weather struct {
	client *http.Client
	// impulse: in-process geocoding cache, move to NATS KV when inventory runs more than one replica.
	geocache sync.Map
}

// NewWeather returns a client with a short timeout so a slow Open-Meteo never
// stalls an agent turn.
func NewWeather() *Weather {
	return &Weather{client: &http.Client{Timeout: 5 * time.Second}}
}

// Place is a geocoded city.
type Place struct {
	Name      string
	Latitude  float64
	Longitude float64
}

// Day is the weather for a single day.
type Day struct {
	TempMaxC        float64
	TempMinC        float64
	PrecipitationMM float64
	// Source is "forecast" or "last_year".
	Source string
}

// Geocode resolves a city name to coordinates.
func (w *Weather) Geocode(ctx context.Context, city string) (Place, error) {
	key := strings.ToLower(city)
	if p, ok := w.geocache.Load(key); ok {
		return p.(Place), nil
	}
	q := url.Values{"name": {city}, "count": {"1"}, "language": {"ru"}, "format": {"json"}}
	var body struct {
		Results []struct {
			Name      string  `json:"name"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := w.get(ctx, "https://geocoding-api.open-meteo.com/v1/search?"+q.Encode(), &body); err != nil {
		return Place{}, err
	}
	if len(body.Results) == 0 {
		return Place{}, fmt.Errorf("city %q not found", city)
	}
	r := body.Results[0]
	p := Place{Name: r.Name, Latitude: r.Latitude, Longitude: r.Longitude}
	w.geocache.Store(key, p)
	return p, nil
}

// DayWeather returns the forecast when date is within the forecast window and
// the same day last year otherwise, as a climate estimate.
func (w *Weather) DayWeather(ctx context.Context, p Place, date time.Time) (Day, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	endpoint, source := "https://api.open-meteo.com/v1/forecast", "forecast"
	switch {
	case date.Before(today.AddDate(0, 0, -7)):
		endpoint, source = "https://archive-api.open-meteo.com/v1/archive", "last_year"
	case date.After(today.AddDate(0, 0, forecastDays)):
		endpoint, source = "https://archive-api.open-meteo.com/v1/archive", "last_year"
		date = date.AddDate(-1, 0, 0)
	}
	day := date.Format(time.DateOnly)
	q := url.Values{
		"latitude":   {fmt.Sprint(p.Latitude)},
		"longitude":  {fmt.Sprint(p.Longitude)},
		"daily":      {"temperature_2m_max,temperature_2m_min,precipitation_sum"},
		"timezone":   {"auto"},
		"start_date": {day},
		"end_date":   {day},
	}
	var body struct {
		Daily struct {
			TempMax []float64 `json:"temperature_2m_max"`
			TempMin []float64 `json:"temperature_2m_min"`
			Precip  []float64 `json:"precipitation_sum"`
		} `json:"daily"`
	}
	if err := w.get(ctx, endpoint+"?"+q.Encode(), &body); err != nil {
		return Day{}, err
	}
	d := body.Daily
	if len(d.TempMax) == 0 || len(d.TempMin) == 0 || len(d.Precip) == 0 {
		return Day{}, errors.New("open-meteo returned no daily data")
	}
	return Day{TempMaxC: d.TempMax[0], TempMinC: d.TempMin[0], PrecipitationMM: d.Precip[0], Source: source}, nil
}

// impulse: single attempt with a timeout, add retries with backoff and a circuit breaker per the ADR when Open-Meteo errors show up in the error-rate metric.
func (w *Weather) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "horkilion-inventory/0.1")
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("open-meteo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("open-meteo returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
