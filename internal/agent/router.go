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
)

const routerPrompt = `Ты разбираешь сообщение путешественника и возвращаешь только JSON без пояснений.
Поля (null, если в сообщении этого нет):
{"destination": город назначения в именительном падеже, "check_in": дата заезда YYYY-MM-DD,
 "nights": число ночей, "adults": число взрослых, "children": число детей,
 "pets": true если едут с животным, "budget_rub": бюджет на проживание в рублях}
Сегодня %s. Текущий план: %s`

// Router turns a user message into a Patch: with the light LLM when it is
// configured, with rules otherwise or when the LLM fails.
type Router struct {
	llm *LLM
}

func (r Router) Route(ctx context.Context, plan TripPlan, text string, today time.Time) (patch Patch) {
	ctx, span := startSpan(ctx, "router", "chain", text)
	defer func() { endSpan(span, patch, nil) }()

	if r.llm != nil {
		patch, err := r.routeLLM(ctx, plan, text, today)
		if err == nil {
			return patch
		}
		slog.Warn("llm router failed, falling back to rules", "err", err)
	}
	_, rules := startSpan(ctx, "router-rules", "span", text)
	patch = routeRules(plan, text, today)
	endSpan(rules, patch, nil)
	return patch
}

func (r Router) routeLLM(ctx context.Context, plan TripPlan, text string, today time.Time) (Patch, error) {
	constraints, _ := json.Marshal(plan.Constraints)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := r.llm.Complete(ctx, fmt.Sprintf(routerPrompt, today.Format(time.DateOnly), constraints), text)
	if err != nil {
		return Patch{}, err
	}
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return Patch{}, errors.New("router returned no JSON object")
	}
	var patch Patch
	if err := json.Unmarshal([]byte(out[start:end+1]), &patch); err != nil {
		return Patch{}, fmt.Errorf("parse router JSON: %w", err)
	}
	return patch, nil
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
func routeRules(plan TripPlan, text string, today time.Time) Patch {
	var p Patch
	lower := strings.ToLower(text)
	trimmed := strings.TrimSpace(text)

	if m := reCity.FindStringSubmatch(text); m != nil {
		p.Destination = ptr(m[1])
	} else if plan.Constraints.Destination == "" && reBareCity.MatchString(trimmed) {
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
		p.CheckIn = ptr(m[1])
	} else if m := reDayMonth.FindStringSubmatch(text); m != nil {
		d, err := time.Parse("2.1.2006", fmt.Sprintf("%s.%s.%d", m[1], m[2], today.Year()))
		if err == nil {
			if d.Before(today) {
				d = d.AddDate(1, 0, 0)
			}
			p.CheckIn = ptr(d.Format(time.DateOnly))
		}
	}
	return p
}

func ptr[T any](v T) *T { return &v }

func atoi32(s string) int32 {
	n, _ := strconv.Atoi(s)
	return int32(n)
}
