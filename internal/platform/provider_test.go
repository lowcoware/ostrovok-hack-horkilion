package platform

import (
	"strings"
	"testing"
)

func TestProvider(t *testing.T) {
	impls := map[string]func() string{"synthetic": func() string { return "s" }, "stub": func() string { return "x" }}

	t.Setenv("TEST_PROVIDER", "")
	if got, err := Provider("TEST_PROVIDER", "synthetic", impls); err != nil || got != "s" {
		t.Errorf("default provider: got %q, %v", got, err)
	}

	t.Setenv("TEST_PROVIDER", "ostrovok")
	_, err := Provider("TEST_PROVIDER", "synthetic", impls)
	if err == nil || !strings.Contains(err.Error(), "allowed: stub, synthetic") {
		t.Errorf("unknown provider must fail with the allowed values, got %v", err)
	}
}
