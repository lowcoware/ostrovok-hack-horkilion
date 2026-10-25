package agent

import (
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Rules are the business rules from config/rules.yaml. They are config, not
// prompt text, so a business tunes them without touching the agent.
type Rules struct {
	Connections struct {
		MinArrivalToCheckinMinutes int `yaml:"min_arrival_to_checkin_minutes"`
		DepartureBufferMinutes     int `yaml:"departure_buffer_minutes"`
	} `yaml:"connections"`
	Clarify struct {
		MaxQuestionsPerTurn int `yaml:"max_questions_per_turn"`
	} `yaml:"clarify"`
	Pricing struct {
		MarkupPercent float64 `yaml:"markup_percent"`
	} `yaml:"pricing"`
}

// LoadRules reads and validates the rules file. Unknown keys are an error, so
// a typo does not silently fall back to zero.
func LoadRules(path string) (Rules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Rules{}, fmt.Errorf("read rules: %w", err)
	}
	var r Rules
	if err := yaml.UnmarshalWithOptions(data, &r, yaml.DisallowUnknownField()); err != nil {
		return Rules{}, fmt.Errorf("parse rules %s: %w", path, err)
	}
	if err := r.validate(); err != nil {
		return Rules{}, fmt.Errorf("rules %s: %w", path, err)
	}
	return r, nil
}

func (r Rules) validate() error {
	switch {
	case r.Connections.MinArrivalToCheckinMinutes < 0 || r.Connections.MinArrivalToCheckinMinutes > 24*60:
		return errors.New("connections.min_arrival_to_checkin_minutes must be 0 to 1440")
	case r.Connections.DepartureBufferMinutes < 0 || r.Connections.DepartureBufferMinutes > 24*60:
		return errors.New("connections.departure_buffer_minutes must be 0 to 1440")
	case r.Clarify.MaxQuestionsPerTurn < 1 || r.Clarify.MaxQuestionsPerTurn > 5:
		return errors.New("clarify.max_questions_per_turn must be 1 to 5")
	case r.Pricing.MarkupPercent < 0 || r.Pricing.MarkupPercent > 100:
		return errors.New("pricing.markup_percent must be 0 to 100")
	}
	return nil
}
