package gateway

import (
	"strings"
	"time"

	"horkilion/internal/dataset"
)

// Provider describes a data source for the "Откуда данные" page.
type Provider struct {
	Provider    string   `json:"provider"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"`
	URL         string   `json:"url,omitempty"`
	License     string   `json:"license,omitempty"`
	FetchedAt   string   `json:"fetched_at,omitempty"`
	UsedFor     []string `json:"used_for"`
}

var kindNotes = map[string]string{
	"real":      "Открытые данные",
	"synthetic": "Сгенерировано по параметрам калибровки, один и тот же запрос даёт ту же цену",
	"derived":   "Рассчитано из открытых данных",
}

// Providers turns the source registry of the data snapshot into the list of
// the "Откуда данные" page: only providers the snapshot uses. Records of the
// snapshot reference nothing else, dataset.Load checks that.
func Providers(registry []dataset.SourceInfo) []Provider {
	out := make([]Provider, 0, len(registry))
	for _, s := range registry {
		if s.Status != "used" {
			continue
		}
		p := Provider{
			Provider: s.Provider, Title: s.Title, Kind: s.Kind, URL: s.URL, License: s.License,
			UsedFor:     append([]string{}, s.UsedFor...),
			Description: kindNotes[s.Kind] + ". Используется для: " + strings.Join(s.UsedFor, ", ") + ".",
		}
		if !s.FetchedAt.IsZero() {
			p.FetchedAt = s.FetchedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, p)
	}
	return out
}
