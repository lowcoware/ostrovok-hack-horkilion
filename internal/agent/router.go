package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"horkilion/internal/agent/plan"
)

const routerPrompt = `Ты разбираешь сообщение путешественника и возвращаешь только JSON без пояснений.
Поля (null, если в сообщении этого нет):
{"origin": город отправления в именительном падеже, "destination": город назначения в именительном падеже,
 "depart": дата отъезда YYYY-MM-DD, "nights": число ночей, "adults": число взрослых,
 "children": число детей, "child_ages": массив возрастов детей, если они названы,
 "pets": true если едут с животным, "budget_rub": бюджет на всю поездку в рублях}
Сегодня %s. Текущие условия: %s`

// Router turns a user message into a plan patch: with the light LLM when it is
// configured, with rules otherwise or when the LLM fails.
type Router struct {
	llm *LLM
}

// routed is what the router extracts from a message, before it becomes a patch.
type routed struct {
	Origin      *string `json:"origin"`
	Destination *string `json:"destination"`
	Depart      *string `json:"depart"`
	Nights      *int32  `json:"nights"`
	Adults      *int32  `json:"adults"`
	Children    *int32  `json:"children"`
	ChildAges   []int32 `json:"child_ages"`
	Pets        *bool   `json:"pets"`
	BudgetRub   *int64  `json:"budget_rub"`
}

func (r Router) Route(ctx context.Context, p plan.TripPlan, text string, today time.Time) (patch plan.Patch) {
	ctx, span := startSpan(ctx, "router", "chain", text)
	defer func() { endSpan(span, patch, nil) }()

	if r.llm != nil {
		out, err := r.routeLLM(ctx, p, text, today)
		if err == nil {
			return out.patch()
		}
		slog.Warn("llm router failed, falling back to rules", "err", err)
	}
	_, rules := startSpan(ctx, "router-rules", "span", text)
	out := routeRules(p, text, today)
	endSpan(rules, out, nil)
	return out.patch()
}

func (r Router) routeLLM(ctx context.Context, p plan.TripPlan, text string, today time.Time) (routed, error) {
	constraints, _ := json.Marshal(p.Constraints)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := r.llm.Complete(ctx, fmt.Sprintf(routerPrompt, today.Format(time.DateOnly), constraints), text)
	if err != nil {
		return routed{}, err
	}
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return routed{}, errors.New("router returned no JSON object")
	}
	var res routed
	if err := json.Unmarshal([]byte(out[start:end+1]), &res); err != nil {
		return routed{}, fmt.Errorf("parse router JSON: %w", err)
	}
	return res, nil
}

// patch converts the router output into a plan patch. Children without named
// ages get the default age, which Apply shows as an assumption.
func (r routed) patch() plan.Patch {
	p := plan.Patch{Origin: r.Origin, Destination: r.Destination, Adults: r.Adults, Pets: r.Pets}
	if r.Depart != nil || r.Nights != nil {
		p.Dates = &plan.DatesPatch{Depart: r.Depart, Nights: r.Nights}
	}
	switch {
	case len(r.ChildAges) > 0:
		ages := r.ChildAges
		p.ChildAges = &ages
	case r.Children != nil && *r.Children >= 0 && *r.Children <= 10:
		ages := make([]int32, *r.Children)
		for i := range ages {
			ages[i] = plan.DefaultChildAge
		}
		p.ChildAges = &ages
		p.ChildAgesAssumed = true
	}
	if r.BudgetRub != nil && *r.BudgetRub > 0 {
		p.Budget = &plan.Budget{AmountRub: *r.BudgetRub}
	}
	return p
}

var (
	reNights   = regexp.MustCompile(`(\d{1,2})\s*(ноч|сут|дн)`)
	reAdults   = regexp.MustCompile(`(\d{1,2})\s*(взросл|человек|чел)`)
	reChildren = regexp.MustCompile(`(\d{1,2})\s*(дет|ребён|ребен)`)
	reOneChild = regexp.MustCompile(`с\s+(ребёнком|ребенком|сыном|дочкой|дочерью)`)
	// Go's \b is ASCII-only, so Cyrillic word ends are matched explicitly.
	rePets       = regexp.MustCompile(`собак|кошк|питом|пёс|щен|(?:^|[^а-яё])пес(?:[^а-яё]|$)`)
	reBudget     = regexp.MustCompile(`(\d[\d\s]*\d|\d)\s*(тыс[а-яё]*|к|₽|руб[а-яё]*|р)(?:[^а-яё]|$)`)
	reISODate    = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\b`)
	reDayMonth   = regexp.MustCompile(`\b(\d{1,2})\.(\d{1,2})\b`)
	reCollective = regexp.MustCompile(`(?:^|[^а-яё])(вдвоём|вдвоем|двое|втроём|втроем|трое|вчетвером|четверо)(?:[^а-яё]|$)`)
	collective   = map[string]int32{"вдвоём": 2, "вдвоем": 2, "двое": 2, "втроём": 3, "втроем": 3, "трое": 3, "вчетвером": 4, "четверо": 4}
	reCity       = regexp.MustCompile(`(?:^|\s)(?:в|во|на)\s+([А-ЯЁ][а-яё]+(?:[\s-][А-ЯЁ][а-яё]+)?)`)
	reBareCity   = regexp.MustCompile(`^[А-ЯЁ][а-яё]+(?:[\s-][А-ЯЁ][а-яё]+)?$`)
)

// routeRules is the fallback router: enough for a demo without an API key.
// impulse: regex router without morphology ("в Москву" stays accusative), use the LLM router in any real demo.
func routeRules(cur plan.TripPlan, text string, today time.Time) routed {
	var p routed
	lower := strings.ToLower(text)
	trimmed := strings.TrimSpace(text)

	if m := reCity.FindStringSubmatch(text); m != nil {
		p.Destination = ptr(m[1])
	} else if cur.Constraints.Destination == "" && reBareCity.MatchString(trimmed) {
		p.Destination = ptr(trimmed)
	}
	if m := reNights.FindStringSubmatch(lower); m != nil {
		p.Nights = ptr(atoi32(m[1]))
	}
	if m := reAdults.FindStringSubmatch(lower); m != nil {
		p.Adults = ptr(atoi32(m[1]))
	} else if m := reCollective.FindStringSubmatch(lower); m != nil {
		p.Adults = ptr(collective[m[1]])
	}
	if m := reChildren.FindStringSubmatch(lower); m != nil {
		p.Children = ptr(atoi32(m[1]))
	} else if reOneChild.MatchString(lower) {
		p.Children = ptr(int32(1))
	}
	if rePets.MatchString(lower) {
		p.Pets = ptr(!strings.Contains(lower, "без "))
	}
	if m := reBudget.FindStringSubmatch(lower); m != nil {
		n, err := strconv.ParseInt(strings.ReplaceAll(m[1], " ", ""), 10, 64)
		if err == nil {
			if strings.HasPrefix(m[2], "тыс") || m[2] == "к" {
				n *= 1000
			}
			p.BudgetRub = &n
		}
	}
	if m := reISODate.FindStringSubmatch(text); m != nil {
		p.Depart = ptr(m[1])
	} else if m := reDayMonth.FindStringSubmatch(text); m != nil {
		d, err := time.Parse("2.1.2006", fmt.Sprintf("%s.%s.%d", m[1], m[2], today.Year()))
		if err == nil {
			if d.Before(today) {
				d = d.AddDate(1, 0, 0)
			}
			p.Depart = ptr(d.Format(time.DateOnly))
		}
	}
	return p
}

func ptr[T any](v T) *T { return &v }

func atoi32(s string) int32 {
	n, _ := strconv.Atoi(s)
	return int32(n)
}
