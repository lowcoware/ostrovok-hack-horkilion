package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"horkilion/internal/agent/plan"
	"horkilion/internal/apitest"
	"horkilion/internal/platform"
)

var today = time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)

func TestRouteRules(t *testing.T) {
	tests := []struct {
		name string
		text string
		want routed
	}{
		{"city after preposition", "Хочу в Сочи на 5 ночей", routed{Destination: ptr("Сочи"), Nights: ptr(int32(5))}},
		{"collective numeral", "Хочу в Сочи, нас двое", routed{Destination: ptr("Сочи"), Adults: ptr(int32(2))}},
		{"bare city", "Казань", routed{Destination: ptr("Казань")}},
		{"guests and pet", "нас 2 взрослых и 1 ребёнок, с нами едет собака", routed{Adults: ptr(int32(2)), Children: ptr(int32(1)), Pets: ptr(true)}},
		{"budget in thousands", "бюджет 80 тысяч", routed{BudgetRub: ptr(int64(80000))}},
		{"day and month rolls to next year", "заезд 05.03", routed{Depart: ptr("2027-03-05")}},
		{"lowercase month is not a city", "в ноябре", routed{}},
		{"empty", "", routed{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeRules(plan.New("s"), tt.text, today)
			if s(got.Destination) != s(tt.want.Destination) || s(got.Depart) != s(tt.want.Depart) ||
				n(got.Nights) != n(tt.want.Nights) || n(got.Adults) != n(tt.want.Adults) ||
				n(got.Children) != n(tt.want.Children) || b(got.Pets) != b(tt.want.Pets) ||
				i64(got.BudgetRub) != i64(tt.want.BudgetRub) {
				t.Errorf("routeRules(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

func TestChildrenWithoutAgesBecomeAssumption(t *testing.T) {
	p := plan.New("s")
	p.Apply(routed{Destination: ptr("Сочи"), Children: ptr(int32(2))}.patch(), today)
	if got := p.Constraints.ChildAges; len(got) != 2 || got[0] != plan.DefaultChildAge {
		t.Fatalf("want two children of the default age, got %v", got)
	}
	if len(p.Assumptions) != 1 || p.Assumptions[0].Field != "child_ages" {
		t.Errorf("unknown ages must be a visible assumption, got %+v", p.Assumptions)
	}

	p.Apply(routed{ChildAges: []int32{4, 9}}.patch(), today)
	if len(p.Assumptions) != 0 {
		t.Errorf("named ages must drop the assumption, got %+v", p.Assumptions)
	}
}

func TestTemplateReplyWithoutDestinationAsksOneQuestion(t *testing.T) {
	if got := templateReply(plan.New("s"), nil); strings.Count(got, "?") != 1 {
		t.Errorf("want exactly one question, got %q", got)
	}
}

func TestEventsMatchSchema(t *testing.T) {
	p := plan.New("4b0c5a5e-8f0e-4a43-9c55-2f3b9e3c7a10")
	base := platform.Event{SessionID: p.SessionID, TurnID: "8f1b2c3d-0000-4000-8000-000000000001", Seq: nextSeq()}
	for _, ev := range []platform.Event{
		{Type: "status", Data: platform.StatusData{Stage: plan.StageSearch, Text: "ищу отели"}},
		{Type: "token", Data: platform.TokenData{Text: "Привет"}},
		{Type: "plan", Data: p},
		{Type: "clarify", Data: platform.ClarifyData{Question: "Куда?", QuickReplies: cityExamples}},
		{Type: "done", Data: platform.DoneData{Text: "Готово", Stage: plan.StageClarify}},
		{Type: "error", Data: platform.ErrorData{Code: "internal", Message: "сбой"}},
	} {
		ev.SessionID, ev.TurnID, ev.Seq = base.SessionID, base.TurnID, base.Seq
		apitest.ValidateSchema(t, "AgentEvent", ev)
	}
	if a, b := nextSeq(), nextSeq(); b <= a {
		t.Errorf("seq must grow: %d then %d", a, b)
	}
}

func TestLoadRules(t *testing.T) {
	if _, err := LoadRules(filepath.Join("..", "..", "config", "rules.yaml")); err != nil {
		t.Fatalf("config/rules.yaml must load: %v", err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"typo":       "connections:\n  min_arival_to_checkin_minutes: 60\nclarify:\n  max_questions_per_turn: 1\n",
		"no clarify": "connections:\n  min_arrival_to_checkin_minutes: 60\n",
		"not yaml":   "connections: [",
	} {
		path := filepath.Join(dir, name+".yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRules(path); err == nil {
			t.Errorf("rules %q must be rejected", name)
		}
	}
}

func TestLLMConfigFromEnv(t *testing.T) {
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "old")
	t.Setenv("LLM_EXTRA_BODY", "")
	cfg, err := LLMConfigFromEnv()
	if err != nil || cfg.APIKey != "old" || cfg.ExtraBody["provider"] == nil {
		t.Fatalf("fallback key and default extra body: %+v, %v", cfg, err)
	}

	t.Setenv("LLM_API_KEY", "new")
	t.Setenv("LLM_EXTRA_BODY", "{}")
	if cfg, err := LLMConfigFromEnv(); err != nil || cfg.APIKey != "new" || len(cfg.ExtraBody) != 0 {
		t.Errorf("LLM_API_KEY wins and an empty extra body is allowed: %+v, %v", cfg, err)
	}

	t.Setenv("LLM_EXTRA_BODY", "provider=deny")
	if _, err := LLMConfigFromEnv(); err == nil {
		t.Error("invalid LLM_EXTRA_BODY must be an error")
	}
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func n(p *int32) int32 {
	if p == nil {
		return -1
	}
	return *p
}
func b(p *bool) string {
	if p == nil {
		return "nil"
	}
	if *p {
		return "true"
	}
	return "false"
}
func i64(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}
