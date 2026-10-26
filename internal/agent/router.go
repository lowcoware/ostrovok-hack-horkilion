package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"horkilion/internal/agent/plan"
)

const routerPrompt = `Ты разбираешь сообщение путешественника и возвращаешь только JSON без пояснений.
Поля (null, если в сообщении этого нет):
{"origin": город отправления в именительном падеже, "destination": город назначения в именительном падеже,
 "citizenship": гражданство (страна паспорта) кодом ISO 3166-1 alpha-2, например BY,
 "depart": дата отъезда YYYY-MM-DD, "month": месяц поездки YYYY-MM, если назван только месяц, "nights": число ночей,
 "adults": число взрослых, "children": число детей, "child_ages": массив возрастов детей, если они названы,
 "pets": true если едут с животным, "budget_rub": бюджет на всю поездку в рублях,
 "budget_strict": true если бюджет нельзя превышать («строго», «не больше»), false если он примерный,
 "moods": массив тегов из sea, warm, mountains, nature, city, kids,
 "transport_modes": массив из air и rail, если человек хочет только самолёт или только поезд,
 "max_travel_hours": максимум часов в пути в одну сторону; «без долгих перелётов» — 5,
 "visa_free_only": false если виза не проблема, true если нужно только без визы,
 "shift_days": на сколько дней сдвинуть текущие даты: «на два дня позже» — 2, «на день раньше» — -1}
Если пользователь выбирает одно из предложенных направлений (по названию или по номеру), верни его название в destination.
Сегодня %s. Текущие условия: %s. Предложенные направления: %s`

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
	Citizenship *string `json:"citizenship"`
	// Month is YYYY-MM when only the month of the trip is named.
	Month          *string  `json:"month"`
	BudgetStrict   *bool    `json:"budget_strict"`
	Moods          []string `json:"moods"`
	TransportModes []string `json:"transport_modes"`
	MaxTravelHours *float64 `json:"max_travel_hours"`
	VisaFreeOnly   *bool    `json:"visa_free_only"`
	// ShiftDays moves the current departure: +2 for «на два дня позже».
	ShiftDays *int32 `json:"shift_days"`
	// LongTripsUnwanted means «без долгих перелётов» without a number.
	LongTripsUnwanted bool `json:"-"`
}

// Interpretations the router makes, shown to the user as assumptions.
const (
	// monthStartDay is the day a trip named only by its month starts.
	monthStartDay = 1
	// shortTripHours is what «без долгих перелётов» means.
	shortTripHours = 5.0
)

func (r Router) Route(ctx context.Context, p plan.TripPlan, text string, today time.Time) (patch plan.Patch) {
	ctx, span := startSpan(ctx, "router", "chain", text)
	defer func() { endSpan(span, patch, nil) }()

	if r.llm != nil {
		out, err := r.routeLLM(ctx, p, text, today)
		if err == nil {
			return out.shift(p).patch()
		}
		slog.Warn("llm router failed, falling back to rules", "err", err)
	}
	_, rules := startSpan(ctx, "router-rules", "span", text)
	out := routeRules(p, text, today)
	endSpan(rules, out, nil)
	return out.shift(p).patch()
}

// shift turns «на два дня позже» into a new departure date relative to the
// current one; without a known departure there is nothing to shift.
func (r routed) shift(p plan.TripPlan) routed {
	if r.ShiftDays == nil || *r.ShiftDays == 0 || r.Depart != nil {
		return r
	}
	if dep, err := time.Parse(time.DateOnly, p.Constraints.Dates.Depart); err == nil {
		r.Depart = ptr(dep.AddDate(0, 0, int(*r.ShiftDays)).Format(time.DateOnly))
	}
	return r
}

func (r Router) routeLLM(ctx context.Context, p plan.TripPlan, text string, today time.Time) (routed, error) {
	constraints, _ := json.Marshal(p.Constraints)
	suggested := make([]string, 0, len(p.DestinationOptions))
	for i, o := range p.DestinationOptions {
		suggested = append(suggested, fmt.Sprintf("%d. %s", i+1, o.Destination.Name))
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := r.llm.Complete(ctx, fmt.Sprintf(routerPrompt, today.Format(time.DateOnly), constraints, strings.Join(suggested, "; ")), text)
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
	p := plan.Patch{Origin: r.Origin, Destination: r.Destination, Adults: r.Adults, Pets: r.Pets, Citizenship: r.Citizenship}
	assume := func(field, reason string) {
		if p.Assumed == nil {
			p.Assumed = map[string]string{}
		}
		p.Assumed[field] = reason
	}
	depart := r.Depart
	if depart == nil && r.Month != nil {
		if m, err := time.Parse("2006-01", *r.Month); err == nil {
			depart = ptr(m.AddDate(0, 0, monthStartDay-1).Format(time.DateOnly))
			assume(plan.FieldDepart, "назван только месяц, взяли его начало")
		}
	}
	if depart != nil || r.Nights != nil {
		p.Dates = &plan.DatesPatch{Depart: depart, Nights: r.Nights}
	}
	prefs := plan.PreferencesPatch{VisaFreeOnly: r.VisaFreeOnly}
	if len(r.Moods) > 0 {
		moods := r.Moods
		prefs.Moods = &moods
	}
	if len(r.TransportModes) > 0 {
		modes := r.TransportModes
		prefs.TransportModes = &modes
	}
	switch {
	case r.MaxTravelHours != nil && *r.MaxTravelHours > 0:
		prefs.MaxTravelHours = r.MaxTravelHours
	case r.LongTripsUnwanted:
		prefs.MaxTravelHours = ptr(shortTripHours)
		assume(plan.FieldMaxTravelHours, fmt.Sprintf("«без долгих перелётов» поняли как до %.0f ч в одну сторону", shortTripHours))
	}
	if prefs != (plan.PreferencesPatch{}) {
		p.Preferences = &prefs
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
		p.Budget = &plan.Budget{AmountRub: *r.BudgetRub, Strict: r.BudgetStrict != nil && *r.BudgetStrict}
	}
	return p
}

var (
	reNights   = regexp.MustCompile(`(\d{1,2})\s*(ноч|сут|дн)`)
	reAdults   = regexp.MustCompile(`(\d{1,2})\s*(взросл|человек|чел)`)
	reChildren = regexp.MustCompile(`(\d{1,2})\s*(дет|ребён|ребен)`)
	reOneChild = regexp.MustCompile(`(?:с|и)\s+(ребёнком|ребенком|ребёнок|ребенок|сыном|дочкой|дочерью|сын|дочка|дочь)(?:[^а-яё]|$)`)
	// Go's \b is ASCII-only, so Cyrillic word ends are matched explicitly.
	rePets       = regexp.MustCompile(`собак|кошк|питом|пёс|щен|(?:^|[^а-яё])пес(?:[^а-яё]|$)`)
	reBudget     = regexp.MustCompile(`(\d[\d\s]*\d|\d)\s*(тыс[а-яё]*|к|₽|руб[а-яё]*|р)(?:[^а-яё]|$)`)
	reISODate    = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\b`)
	reDayMonth   = regexp.MustCompile(`\b(\d{1,2})\.(\d{1,2})\b`)
	reCollective = regexp.MustCompile(`(?:^|[^а-яё])(вдвоём|вдвоем|двое|втроём|втроем|трое|вчетвером|четверо)(?:[^а-яё]|$)`)
	collective   = map[string]int32{"вдвоём": 2, "вдвоем": 2, "двое": 2, "втроём": 3, "втроем": 3, "трое": 3, "вчетвером": 4, "четверо": 4}
	reCity       = regexp.MustCompile(`(?:^|\s)(?:в|во|на)\s+([А-ЯЁ][а-яё]+(?:[\s-][А-ЯЁ][а-яё]+)?)`)
	reBareCity   = regexp.MustCompile(`^[А-ЯЁ][а-яё]+(?:[\s-][А-ЯЁ][а-яё]+)?$`)
	reOrigin     = regexp.MustCompile(`(?:^|\s)(?i:из|от)\s+([А-ЯЁ][а-яё]+(?:[\s-][А-ЯЁ][а-яё]+)?)`)
	rePassport   = regexp.MustCompile(`паспорт|гражданств|гражданин|гражданка`)
	reMonth      = regexp.MustCompile(`(?:^|[^а-яё])(?:в|на)\s+(январ|феврал|март|апрел|ма[ейю]|июн|июл|август|сентябр|октябр|ноябр|декабр)`)
	reHours      = regexp.MustCompile(`(?:не больше|не дольше|до|максимум)\s+(\d{1,2})\s*(?:час|ч(?:[^а-яё]|$))`)
	reShortTrip  = regexp.MustCompile(`без долг(?:их|ого|ой) (?:перел[её]т|дорог|пут)|недалеко`)
	reRail       = regexp.MustCompile(`поездом|на поезде|только поезд|без самол[её]т`)
	reAir        = regexp.MustCompile(`самол[её]том|на самол[её]те|только самол[её]т|лететь`)
	reStrict     = regexp.MustCompile(`строго|не больше|не дороже|максимум`)
	reVisaOK     = regexp.MustCompile(`можно с виз|виза не проблема|визу сделаем|есть виза|есть шенген|с визой можно`)
	reVisaFree   = regexp.MustCompile(`без виз|безвиз`)
	reShift      = regexp.MustCompile(`на\s+(\d{1,2}|день|сутки|два|две|три|четыре|пять|шесть|неделю)\s*(?:дн[а-яё]*|сут[а-яё]*)?\s*(позже|раньше|попозже|пораньше)`)
	shiftWords   = map[string]int32{"день": 1, "сутки": 1, "два": 2, "две": 2, "три": 3, "четыре": 4, "пять": 5, "шесть": 6, "неделю": 7}
	moodRules    = []struct {
		tag string
		re  *regexp.Regexp
	}{
		{"sea", regexp.MustCompile(`мор[еяюь]|пляж`)},
		{"warm", regexp.MustCompile(`тепл|тёпл|жарк|солн`)},
		{"mountains", regexp.MustCompile(`(?:^|[^а-яё])гор(?:ы|ах|ам|у)?(?:[^а-яё]|$)`)},
		{"nature", regexp.MustCompile(`природ`)},
		{"city", regexp.MustCompile(`экскурс|музе|архитектур|достопримечат`)},
		{"kids", regexp.MustCompile(`ребён|ребен|дет[иья]|сын|дочк|дочер`)},
	}
	months = map[string]time.Month{
		"январ": time.January, "феврал": time.February, "март": time.March, "апрел": time.April, "мае": time.May,
		"мая": time.May, "маю": time.May, "июн": time.June, "июл": time.July, "август": time.August,
		"сентябр": time.September, "октябр": time.October, "ноябр": time.November, "декабр": time.December,
	}
	// Passport countries by the stem of their name in any case or as an adjective.
	passportCountries = []struct{ stem, code string }{
		{"росси", "RU"}, {"беларус", "BY"}, {"белорус", "BY"}, {"казах", "KZ"}, {"узбек", "UZ"}, {"армян", "AM"},
		{"армени", "AM"}, {"киргиз", "KG"}, {"кыргыз", "KG"}, {"таджик", "TJ"}, {"грузи", "GE"}, {"грузин", "GE"},
		{"азербайдж", "AZ"}, {"молдав", "MD"}, {"молдов", "MD"}, {"украин", "UA"}, {"серби", "RS"}, {"сербск", "RS"},
		{"турци", "TR"}, {"турецк", "TR"}, {"франци", "FR"}, {"француз", "FR"}, {"германи", "DE"}, {"немец", "DE"},
		{"кита", "CN"}, {"израил", "IL"},
	}
)

// words splits lower-case text into Cyrillic and Latin words.
func words(lower string) []string {
	return strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
}

// routeRules is the fallback router: enough for a demo without an API key.
// impulse: regex router without morphology ("в Москву" stays accusative), use the LLM router in any real demo.
func routeRules(cur plan.TripPlan, text string, today time.Time) routed {
	var p routed
	lower := strings.ToLower(text)
	trimmed := strings.TrimSpace(text)

	if d := pickDestination(cur, lower); d != "" {
		p.Destination = ptr(d)
	} else if m := reCity.FindStringSubmatch(text); m != nil {
		p.Destination = ptr(m[1])
	} else if cur.Constraints.Destination == "" && reBareCity.MatchString(trimmed) {
		p.Destination = ptr(trimmed)
	}
	if m := reOrigin.FindStringSubmatch(text); m != nil {
		// Genitive («из Минска»); the agent resolves it to a catalog city.
		p.Origin = ptr(m[1])
	}
	if rePassport.MatchString(lower) {
		for _, w := range words(lower) {
			if i := slices.IndexFunc(passportCountries, func(c struct{ stem, code string }) bool { return strings.HasPrefix(w, c.stem) }); i >= 0 {
				p.Citizenship = ptr(passportCountries[i].code)
				break
			}
		}
	}
	if m := reMonth.FindStringSubmatch(lower); m != nil {
		month := months[m[1]]
		year := today.Year()
		if month < today.Month() {
			year++
		}
		if month == today.Month() {
			p.Depart = ptr(today.Format(time.DateOnly))
		} else {
			p.Month = ptr(fmt.Sprintf("%d-%02d", year, month))
		}
	}
	for _, m := range moodRules {
		if m.re.MatchString(lower) {
			p.Moods = append(p.Moods, m.tag)
		}
	}
	switch {
	case reRail.MatchString(lower):
		p.TransportModes = []string{"rail"}
	case reAir.MatchString(lower):
		p.TransportModes = []string{"air"}
	}
	if m := reHours.FindStringSubmatch(lower); m != nil {
		p.MaxTravelHours = ptr(float64(atoi32(m[1])))
	} else if reShortTrip.MatchString(lower) {
		p.LongTripsUnwanted = true
	}
	switch {
	case reVisaOK.MatchString(lower):
		p.VisaFreeOnly = ptr(false)
	case reVisaFree.MatchString(lower):
		p.VisaFreeOnly = ptr(true)
	}
	if m := reShift.FindStringSubmatch(lower); m != nil {
		n, ok := shiftWords[m[1]]
		if !ok {
			n = atoi32(m[1])
		}
		if strings.HasSuffix(m[2], "раньше") {
			n = -n
		}
		p.ShiftDays = &n
	}
	if m := reNights.FindStringSubmatch(lower); m != nil && p.ShiftDays == nil {
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
			p.BudgetStrict = ptr(reStrict.MatchString(lower))
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

var ordinals = []string{"перв", "втор", "трет", "четв", "пят"}

// pickDestination finds which suggested destination the message chooses: by
// an ordinal («второй вариант») or by the stem of its name, so that any case
// of the name matches («в Анталию», «Анталья»).
func pickDestination(cur plan.TripPlan, lower string) string {
	opts := cur.DestinationOptions
	if cur.Constraints.Destination != "" || len(opts) == 0 {
		return ""
	}
	for _, o := range opts {
		// Drop the ending: «Дубай» matches «в Дубае», «Сочи» stays whole.
		name := []rune(strings.ToLower(o.Destination.Name))
		stem := string(name[:min(len(name), max(4, min(5, len(name)-1)))])
		for _, w := range words(lower) {
			// A case ending adds at most two letters: «казахстана» is not «Казань».
			if strings.HasPrefix(w, stem) && utf8.RuneCountInString(w) <= len(name)+2 {
				return o.Destination.Name
			}
		}
	}
	for i, ord := range ordinals[:min(len(ordinals), len(opts))] {
		if strings.Contains(lower, ord) {
			return opts[i].Destination.Name
		}
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func atoi32(s string) int32 {
	n, _ := strconv.Atoi(s)
	return int32(n)
}
