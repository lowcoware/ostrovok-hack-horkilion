package plan

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults used when the user did not say. Each one becomes a visible assumption.
const (
	DefaultLeadDays = 14
	DefaultNights   = 3
	DefaultAdults   = 2
	DefaultChildAge = 7
	// DefaultCitizenship is assumed until the traveller names another passport.
	DefaultCitizenship = "RU"
	// DefaultOrigin is assumed until the traveller names the departure city.
	DefaultOrigin = "Москва"
)

// Patch changes the plan. Text messages (through the router) and manual edits
// in the interface (POST /sessions/{id}/plan) both arrive as a Patch, so the
// same invalidation applies to both. A nil field means "not mentioned".
type Patch struct {
	Origin      *string           `json:"origin,omitempty"`
	Destination *string           `json:"destination,omitempty"`
	Citizenship *string           `json:"citizenship,omitempty"`
	Dates       *DatesPatch       `json:"dates,omitempty"`
	Adults      *int32            `json:"adults,omitempty"`
	ChildAges   *[]int32          `json:"child_ages,omitempty"`
	Pets        *bool             `json:"pets,omitempty"`
	Budget      *Budget           `json:"budget,omitempty"`
	Preferences *PreferencesPatch `json:"preferences,omitempty"`
	Select      *Selection        `json:"select,omitempty"`

	// ChildAgesAssumed means the router knew the number of children but not
	// their ages; Apply records the ages as an assumption.
	ChildAgesAssumed bool `json:"-"`
}

type DatesPatch struct {
	Depart   *string `json:"depart,omitempty"`
	Return   *string `json:"return,omitempty"`
	Nights   *int32  `json:"nights,omitempty"`
	FlexDays *int32  `json:"flex_days,omitempty"`
}

type PreferencesPatch struct {
	Meal             *string   `json:"meal,omitempty"`
	FreeCancellation *bool     `json:"free_cancellation,omitempty"`
	TransportModes   *[]string `json:"transport_modes,omitempty"`
	MaxTravelHours   *float64  `json:"max_travel_hours,omitempty"`
	Moods            *[]string `json:"moods,omitempty"`
	VisaFreeOnly     *bool     `json:"visa_free_only,omitempty"`
}

type Selection struct {
	Component string `json:"component"`
	OptionID  string `json:"option_id"`
}

var (
	countryCode    = regexp.MustCompile(`^[A-Z]{2}$`)
	meals          = []string{"room_only", "breakfast", "half_board", "all_inclusive"}
	transportModes = []string{"air", "rail"}
)

// Empty reports whether the patch changes nothing.
func (p Patch) Empty() bool {
	return p.Origin == nil && p.Destination == nil && p.Citizenship == nil && p.Dates == nil && p.Adults == nil && p.ChildAges == nil &&
		p.Pets == nil && p.Budget == nil && p.Preferences == nil && p.Select == nil
}

// Validate checks a patch from an API client against the PlanPatch schema
// limits. Patches from the router are not validated: Apply ignores values out
// of range instead.
func (p Patch) Validate() error {
	if p.Empty() {
		return errors.New("patch must change at least one field")
	}
	for name, s := range map[string]*string{"origin": p.Origin, "destination": p.Destination} {
		if s != nil && (strings.TrimSpace(*s) == "" || utf8.RuneCountInString(*s) > 100) {
			return fmt.Errorf("%s must be 1 to 100 characters", name)
		}
	}
	if p.Citizenship != nil && !countryCode.MatchString(*p.Citizenship) {
		return errors.New("citizenship must be an ISO 3166-1 alpha-2 code, e.g. RU")
	}
	if d := p.Dates; d != nil {
		for name, s := range map[string]*string{"dates.depart": d.Depart, "dates.return": d.Return} {
			if s != nil {
				if _, err := time.Parse(time.DateOnly, *s); err != nil {
					return fmt.Errorf("%s must be YYYY-MM-DD", name)
				}
			}
		}
		if d.Nights != nil && (*d.Nights < 1 || *d.Nights > 30) {
			return errors.New("dates.nights must be 1 to 30")
		}
		if d.FlexDays != nil && (*d.FlexDays < 0 || *d.FlexDays > 14) {
			return errors.New("dates.flex_days must be 0 to 14")
		}
	}
	if p.Adults != nil && (*p.Adults < 1 || *p.Adults > 10) {
		return errors.New("adults must be 1 to 10")
	}
	if p.ChildAges != nil {
		if len(*p.ChildAges) > 10 {
			return errors.New("child_ages must have at most 10 items")
		}
		for _, age := range *p.ChildAges {
			if age < 0 || age > 17 {
				return errors.New("child_ages items must be 0 to 17")
			}
		}
	}
	if p.Budget != nil && p.Budget.AmountRub < 1 {
		return errors.New("budget.amount_rub must be positive")
	}
	if pr := p.Preferences; pr != nil {
		if pr.Meal != nil && !slices.Contains(meals, *pr.Meal) {
			return fmt.Errorf("preferences.meal must be one of %v", meals)
		}
		if pr.TransportModes != nil {
			for _, m := range *pr.TransportModes {
				if !slices.Contains(transportModes, m) {
					return fmt.Errorf("preferences.transport_modes items must be one of %v", transportModes)
				}
			}
		}
	}
	if s := p.Select; s != nil {
		if !slices.Contains(ComponentNames, s.Component) {
			return fmt.Errorf("select.component must be one of %v", ComponentNames)
		}
		if s.OptionID == "" {
			return errors.New("select.option_id is required")
		}
	}
	return nil
}

// What a change of each constraint makes stale (ADR 0001, "Обработка одного
// сообщения", step 2). Stale components are re-checked with their supplier in
// the same turn: the same offers on the new terms, not a search from scratch.
// Everything not listed stays as it was.
//
//	destination        -> all components and the weather are dropped (a new search)
//	origin             -> outbound, return, transfer_arrival, transfer_departure
//	dates              -> every component, weather
//	adults, child_ages -> every component
//	pets               -> stay, transfer_arrival, transfer_departure
//	meal, cancellation -> stay
//	transport modes    -> outbound, return
//	budget             -> nothing: totals and over_budget flags are recomputed
//	citizenship        -> destination options, visa and visa checks; hotels and transport stay
//	visa_free_only     -> destination options
//	nights             -> the visa-free stay check is recomputed (spec 0002)
//	select outbound    -> transfer_arrival
//	select stay        -> transfer_arrival, transfer_departure
//	select return      -> transfer_departure
var (
	allComponents   = ComponentNames
	transportStale  = []string{Outbound, Return, TransferArrival, TransferDeparture}
	petsStale       = []string{Stay, TransferArrival, TransferDeparture}
	journeyStale    = []string{Outbound, Return}
	preferenceStale = []string{Stay}
	// Transfers run between the chosen journeys and the chosen hotel.
	selectStale = map[string][]string{
		Outbound: {TransferArrival}, Stay: {TransferArrival, TransferDeparture}, Return: {TransferDeparture},
	}
)

// Apply merges a patch into the plan and invalidates exactly what the change
// makes stale. It returns what changed, in words for the reply and the logs.
func (p *TripPlan) Apply(patch Patch, today time.Time) []string {
	c := &p.Constraints
	var changes []string

	if v := patch.Destination; v != nil && strings.TrimSpace(*v) != "" && *v != c.Destination {
		if c.Destination != "" {
			changes = append(changes, "направление изменилось: варианты, выбор и погода сброшены")
		}
		c.Destination = strings.TrimSpace(*v)
		p.Components = Components{}
		p.Weather = nil
		p.clearVisa()
		p.normalize()
	}
	if v := patch.Citizenship; v != nil && countryCode.MatchString(*v) && *v != c.Citizenship {
		c.Citizenship = *v
		p.dropAssumption("citizenship")
		p.DestinationOptions = []DestinationOption{}
		p.clearVisa()
		changes = append(changes, "гражданство изменилось: визы и варианты направлений пересчитываются")
	}
	if v := patch.Origin; v != nil && strings.TrimSpace(*v) != "" && *v != c.Origin {
		c.Origin = strings.TrimSpace(*v)
		p.dropAssumption("origin")
		changes = append(changes, p.markStale("город отправления изменился", transportStale)...)
	}
	if d := patch.Dates; d != nil && p.applyDates(*d, today) {
		p.Weather = nil
		changes = append(changes, p.markStale("даты изменились", allComponents)...)
	}
	guestsChanged := false
	if v := patch.Adults; v != nil && *v >= 1 && *v <= 10 && *v != c.Adults {
		c.Adults = *v
		p.dropAssumption("adults")
		guestsChanged = true
	}
	if v := patch.ChildAges; v != nil && validAges(*v) && !slices.Equal(*v, c.ChildAges) {
		c.ChildAges = slices.Clone(*v)
		p.dropAssumption("child_ages")
		if patch.ChildAgesAssumed && len(c.ChildAges) > 0 {
			p.addAssumption("child_ages", fmt.Sprint(c.ChildAges), fmt.Sprintf("возраст детей не назван, взяли %d лет", DefaultChildAge))
		}
		guestsChanged = true
	}
	if guestsChanged {
		changes = append(changes, p.markStale("состав гостей изменился", allComponents)...)
	}
	if v := patch.Pets; v != nil && *v != c.Pets {
		c.Pets = *v
		changes = append(changes, p.markStale("питомец в поездке изменился", petsStale)...)
	}
	if v := patch.Budget; v != nil && v.AmountRub > 0 && (c.Budget == nil || *v != *c.Budget) {
		b := *v
		c.Budget = &b
		changes = append(changes, "бюджет изменился: итог пересчитан без нового поиска")
	}
	if pr := patch.Preferences; pr != nil {
		changes = append(changes, p.applyPreferences(*pr)...)
	}
	if s := patch.Select; s != nil {
		changes = append(changes, p.selectOption(*s)...)
	}
	p.recompute()
	return changes
}

func validAges(ages []int32) bool {
	if len(ages) > 10 {
		return false
	}
	for _, a := range ages {
		if a < 0 || a > 17 {
			return false
		}
	}
	return true
}

// applyDates merges new dates and keeps depart, return and nights consistent.
// It reports whether the trip dates changed.
func (p *TripPlan) applyDates(d DatesPatch, today time.Time) bool {
	dates := &p.Constraints.Dates
	before := *dates
	if d.Depart != nil {
		if t, err := time.Parse(time.DateOnly, *d.Depart); err == nil && !t.Before(today) {
			dates.Depart = *d.Depart
			p.dropAssumption("dates.depart")
		}
	}
	if d.Nights != nil && *d.Nights >= 1 && *d.Nights <= 30 {
		dates.Nights = *d.Nights
		dates.Return = ""
		p.dropAssumption("dates.nights")
	}
	if d.Return != nil {
		if ret, err := time.Parse(time.DateOnly, *d.Return); err == nil {
			if dep, ok := dates.departDate(); ok {
				if n := int32(ret.Sub(dep).Hours() / 24); n >= 1 && n <= 30 {
					dates.Return, dates.Nights = *d.Return, n
					p.dropAssumption("dates.nights")
				}
			} else {
				dates.Return = *d.Return
			}
		}
	}
	if d.FlexDays != nil && *d.FlexDays >= 0 && *d.FlexDays <= 14 {
		dates.FlexDays = *d.FlexDays
	}
	p.syncReturn()
	return dates.Depart != before.Depart || dates.Nights != before.Nights || dates.Return != before.Return
}

// syncReturn derives the return date from depart and nights.
func (p *TripPlan) syncReturn() {
	dates := &p.Constraints.Dates
	if dep, ok := dates.departDate(); ok && dates.Nights > 0 {
		dates.Return = dep.AddDate(0, 0, int(dates.Nights)).Format(time.DateOnly)
	}
}

func (p *TripPlan) applyPreferences(pr PreferencesPatch) []string {
	prefs := &p.Constraints.Preferences
	var changes []string
	stayChanged := false
	if v := pr.Meal; v != nil && slices.Contains(meals, *v) && *v != prefs.Meal {
		prefs.Meal = *v
		stayChanged = true
	}
	if v := pr.FreeCancellation; v != nil && (prefs.FreeCancellation == nil || *v != *prefs.FreeCancellation) {
		b := *v
		prefs.FreeCancellation = &b
		stayChanged = true
	}
	if stayChanged {
		changes = append(changes, p.markStale("пожелания к отелю изменились", preferenceStale)...)
	}
	if v := pr.TransportModes; v != nil && !slices.Equal(*v, prefs.TransportModes) {
		modes := slices.DeleteFunc(slices.Clone(*v), func(m string) bool { return !slices.Contains(transportModes, m) })
		prefs.TransportModes = modes
		changes = append(changes, p.markStale("виды транспорта изменились", journeyStale)...)
	}
	if v := pr.MaxTravelHours; v != nil && *v >= 0 && *v != prefs.MaxTravelHours {
		prefs.MaxTravelHours = *v
		changes = append(changes, p.markStale("допустимое время в пути изменилось", journeyStale)...)
	}
	if v := pr.Moods; v != nil {
		prefs.Moods = slices.Clone(*v)
	}
	if v := pr.VisaFreeOnly; v != nil && *v != prefs.VisaFreeOnly {
		prefs.VisaFreeOnly = *v
		p.DestinationOptions = []DestinationOption{}
		if *v {
			changes = append(changes, "подбор только по странам без визы")
		} else {
			changes = append(changes, "в подбор добавлены страны с визой")
		}
	}
	return changes
}

func (p *TripPlan) selectOption(s Selection) []string {
	comp := p.Components.Get(s.Component)
	if comp == nil {
		return nil
	}
	for _, o := range comp.Options {
		if o.ID == s.OptionID && !o.hasFlag(FlagSelectedUnavailable) {
			if comp.SelectedID == o.ID {
				return nil
			}
			comp.SelectedID = o.ID
			p.dropConflicts(s.Component)
			changes := []string{fmt.Sprintf("выбран вариант %s", OptionTitle(o))}
			return append(changes, p.markStale("подбираю трансфер под выбор", selectStale[s.Component])...)
		}
	}
	return []string{"выбранного варианта нет среди доступных, выбор не изменился"}
}

// markStale schedules a re-check of the components that already have options.
func (p *TripPlan) markStale(reason string, names []string) []string {
	var touched []string
	for _, name := range names {
		if len(p.Components.Get(name).Options) > 0 {
			if p.stale == nil {
				p.stale = map[string]bool{}
			}
			p.stale[name] = true
			touched = append(touched, componentTitles[name])
		}
	}
	if len(touched) == 0 {
		return nil
	}
	return []string{reason + ": перепроверяю " + strings.Join(touched, ", ")}
}

// componentTitles name components in replies to the user.
var componentTitles = map[string]string{
	Outbound: "дорогу туда", Stay: "отели", Return: "дорогу обратно",
	TransferArrival: "трансфер до отеля", TransferDeparture: "трансфер на обратную дорогу",
}

// NeedsSearch reports whether a component has to be searched or re-checked in this turn.
func (p *TripPlan) NeedsSearch(component string) bool {
	return len(p.Components.Get(component).Options) == 0 || p.stale[component]
}

// SearchGate reports whether the plan has enough to search hotels. Only the
// destination is required: dates and guests fall back to visible assumptions,
// so the agent asks one question at most.
func (p *TripPlan) SearchGate() bool {
	return p.Constraints.Destination != ""
}

// FillDefaults applies the defaults for everything the user left out and
// records each one as an assumption.
func (p *TripPlan) FillDefaults(today time.Time) {
	c := &p.Constraints
	if c.Origin == "" {
		c.Origin = DefaultOrigin
		p.addAssumption("origin", DefaultOrigin, "город отправления не назван")
	}
	if c.Citizenship == "" {
		c.Citizenship = DefaultCitizenship
		p.addAssumption("citizenship", DefaultCitizenship, "гражданство не названо")
	}
	if c.Dates.Depart == "" {
		c.Dates.Depart = today.AddDate(0, 0, DefaultLeadDays).Format(time.DateOnly)
		p.addAssumption("dates.depart", c.Dates.Depart, fmt.Sprintf("даты не названы, взяли отъезд через %d дней", DefaultLeadDays))
	}
	if c.Dates.Nights == 0 {
		c.Dates.Nights = DefaultNights
		p.addAssumption("dates.nights", fmt.Sprint(DefaultNights), "длительность не названа")
	}
	p.syncReturn()
	if c.Adults == 0 {
		c.Adults = DefaultAdults
		p.addAssumption("adults", fmt.Sprint(DefaultAdults), "состав гостей не назван")
	}
}

// SetOptions stores fresh supplier results for a component. The current
// selection is re-validated: when it is no longer offered, it stays in the
// list flagged selected_unavailable and a conflict tells the user.
func (p *TripPlan) SetOptions(component string, options []Option) {
	comp := p.Components.Get(component)
	prev := comp.Selected()
	comp.Options = options
	delete(p.stale, component)
	p.dropConflicts(component)
	if prev != nil && comp.Selected() == nil {
		gone := *prev
		gone.setFlag(FlagSelectedUnavailable, true)
		comp.Options = append(comp.Options, gone)
		p.Conflicts = append(p.Conflicts, Conflict{
			Kind:       "selected_unavailable",
			Message:    fmt.Sprintf("«%s» не подходит под новые условия", OptionTitle(gone)),
			Components: []string{component},
		})
	}
	p.recompute()
}

// NextStage moves the plan to the earliest stage whose gate is not passed.
func (p *TripPlan) NextStage() {
	switch {
	case !p.SearchGate() && len(p.DestinationOptions) > 0:
		p.Stage = StageInspire
	case !p.SearchGate():
		p.Stage = StageClarify
	case len(p.Components.Stay.Options) == 0:
		p.Stage = StageSearch
	case p.Components.Stay.Selected() == nil:
		p.Stage = StageCompare
	default:
		p.Stage = StageAssemble
	}
}

// recompute sums the selected options and flags options that would break the
// budget if chosen instead of the current selection of their component.
func (p *TripPlan) recompute() {
	var total int64
	for _, name := range ComponentNames {
		if sel := p.Components.Get(name).Selected(); sel != nil && !sel.hasFlag(FlagSelectedUnavailable) {
			total += sel.PriceRub
		}
	}
	p.TotalRub = total
	p.updateVisaChecks()
	budget := p.Constraints.Budget
	p.OverBudget = budget != nil && total > budget.AmountRub
	for _, name := range ComponentNames {
		comp := p.Components.Get(name)
		others := total
		if sel := comp.Selected(); sel != nil && !sel.hasFlag(FlagSelectedUnavailable) {
			others -= sel.PriceRub
		}
		for i := range comp.Options {
			o := &comp.Options[i]
			o.setFlag(FlagOverBudget, budget != nil && others+o.PriceRub > budget.AmountRub)
		}
	}
}

func (p *TripPlan) addAssumption(field, value, reason string) {
	p.dropAssumption(field)
	p.Assumptions = append(p.Assumptions, Assumption{Field: field, Value: value, Reason: reason})
}

func (p *TripPlan) dropAssumption(field string) {
	p.Assumptions = slices.DeleteFunc(p.Assumptions, func(a Assumption) bool { return a.Field == field })
}

func (p *TripPlan) dropConflicts(component string) {
	p.Conflicts = slices.DeleteFunc(p.Conflicts, func(c Conflict) bool { return slices.Contains(c.Components, component) })
}

// OptionTitle is a short human name of an option, e.g. for order items.
func OptionTitle(o Option) string {
	switch {
	case o.Stay != nil:
		return o.Stay.Name
	case o.Journey != nil && len(o.Journey.Segments) > 0:
		s := o.Journey.Segments[0]
		return strings.TrimSpace(s.Carrier + " " + s.Number)
	case o.Transfer != nil:
		return "Трансфер " + o.Transfer.From.Name + " → " + o.Transfer.To.Name
	}
	return o.ID
}

// Visa check kinds, see Check.kind in api/openapi.yaml.
const (
	CheckVisa         = "visa"
	CheckVisaFreeStay = "stay_within_visa_free_days"
)

// SetVisa stores the visa requirement of the chosen destination. nil means it
// could not be checked (catalog unavailable or the pair is not in the data):
// the plan then carries a warning instead of silently passing.
func (p *TripPlan) SetVisa(v *VisaRequirement) {
	p.Visa = v
	p.dropChecks(CheckVisa, CheckVisaFreeStay)
	if v == nil {
		p.Checks = append(p.Checks, Check{
			Kind: CheckVisa, Status: "warn", Message: "Не удалось проверить визовый режим.", Components: []string{},
		})
		return
	}
	p.updateVisaChecks()
}

func (p *TripPlan) clearVisa() {
	p.Visa = nil
	p.dropChecks(CheckVisa, CheckVisaFreeStay)
}

var visaMessages = map[string]string{
	VisaHome:        "Своя страна, виза не нужна.",
	VisaFree:        "Виза не нужна.",
	VisaOnArrival:   "Виза оформляется по прилёту.",
	VisaETA:         "Нужно электронное разрешение на въезд, оформляется онлайн до поездки.",
	VisaElectronic:  "Нужна электронная виза, оформляется онлайн до поездки.",
	VisaRequired:    "Нужна виза, оформляется в консульстве до поездки.",
	VisaNoAdmission: "Въезд для граждан этой страны закрыт.",
}

// updateVisaChecks derives the visa checks from p.Visa and the trip length
// (spec 0002, runtime behaviour step 6). Without p.Visa existing checks stay.
func (p *TripPlan) updateVisaChecks() {
	v := p.Visa
	if v == nil {
		return
	}
	p.dropChecks(CheckVisa, CheckVisaFreeStay)
	status := "ok"
	switch v.Status {
	case VisaETA, VisaElectronic:
		status = "warn"
	case VisaRequired, VisaNoAdmission:
		status = "fail"
	}
	msg := visaMessages[v.Status]
	if v.VisaFree() && v.MaxStayDays > 0 && v.Status != VisaHome {
		msg = fmt.Sprintf("%s Без визы можно находиться до %d дней.", msg, v.MaxStayDays)
	}
	p.Checks = append(p.Checks, Check{Kind: CheckVisa, Status: status, Message: msg, Components: []string{}})

	if !v.VisaFree() || v.Status == VisaHome {
		return
	}
	stay := Check{Kind: CheckVisaFreeStay, Status: "ok", Message: "Поездка укладывается в безвизовый срок.", Components: []string{Stay}}
	if nights := p.Constraints.Dates.Nights; v.MaxStayDays > 0 && nights > v.MaxStayDays {
		stay.Status = "fail"
		stay.Message = fmt.Sprintf("Поездка %d ночей длиннее безвизового срока %d дней.", nights, v.MaxStayDays)
	}
	p.Checks = append(p.Checks, stay)
}

func (p *TripPlan) dropChecks(kinds ...string) {
	p.Checks = slices.DeleteFunc(p.Checks, func(c Check) bool { return slices.Contains(kinds, c.Kind) })
}
