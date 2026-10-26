package platform

import "testing"

func TestFullTrip(t *testing.T) {
	items := func(kinds ...string) BookingCreated {
		var b BookingCreated
		for _, k := range kinds {
			b.Items = append(b.Items, BookingItemInfo{Kind: k})
		}
		return b
	}
	if !items("journey", "transfer", "stay", "transfer", "journey").FullTrip() {
		t.Error("a hotel, two journeys and two transfers are a whole trip")
	}
	if items("stay").FullTrip() || items("journey", "stay", "journey").FullTrip() {
		t.Error("a hotel alone or without transfers is not a whole trip")
	}
}
