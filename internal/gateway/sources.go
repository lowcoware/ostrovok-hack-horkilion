package gateway

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

// providers lists the sources the current supplier providers use. The names
// match Source.provider on the entities.
// impulse: a static list, serve it from the supplier services when phase 1 loads datasets with real fetch dates.
var providers = []Provider{
	{
		Provider:    "open-meteo",
		Title:       "Open-Meteo",
		Description: "Прогноз погоды на ближайшие две недели и архив наблюдений для дат дальше.",
		Kind:        "real",
		URL:         "https://open-meteo.com",
		License:     "CC-BY-4.0",
		UsedFor:     []string{"погода", "координаты городов"},
	},
	{
		Provider:    "synthetic",
		Title:       "Синтетические данные",
		Description: "Отели и цены, сгенерированные детерминированно: один и тот же запрос всегда даёт ту же цену.",
		Kind:        "synthetic",
		UsedFor:     []string{"отели", "цены на проживание"},
	},
}
