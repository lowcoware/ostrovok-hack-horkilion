package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
	"horkilion/internal/agent/plan"
)

// resolvePlaces turns the city names of a routed patch into catalog names:
// the rules router keeps the case of the message («из Минска», «в Анталию»),
// suppliers need «Минск» and «Анталия». A name the catalog does not know
// stays as it is, and the reply then says nothing is found.
func (a *Agent) resolvePlaces(ctx context.Context, patch *plan.Patch) {
	for _, name := range []**string{&patch.Origin, &patch.Destination} {
		if *name != nil {
			if city := a.resolveCity(ctx, **name); city != "" {
				*name = &city
			}
		}
	}
}

// resolveCity looks the name up as is, then with one and two letters of the
// case ending dropped.
func (a *Agent) resolveCity(ctx context.Context, name string) string {
	runes := []rune(strings.TrimSpace(name))
	for drop := 0; drop <= 2 && len(runes)-drop >= 3; drop++ {
		query := string(runes[:len(runes)-drop])
		cctx, cancel := context.WithTimeout(ctx, supplierTimeout)
		resp, err := a.catalog.SearchPlaces(cctx, &catalogv1.SearchPlacesRequest{
			Query: query, Kinds: []commonv1.Place_Kind{commonv1.Place_KIND_CITY}, Limit: 1,
		})
		cancel()
		if err != nil {
			return ""
		}
		if places := resp.GetPlaces(); len(places) > 0 {
			// The found name may differ only in the ending: «Москвы» → «Москва», not «Можайск».
			if found := places[0].GetName(); utf8.RuneCountInString(found) <= len(runes)+2 {
				return found
			}
		}
	}
	return ""
}
