package platform

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Provider returns the implementation named by the environment variable key,
// or by def when the variable is unset. An unknown name is an error that lists
// the allowed values, so a typo in the config stops the process at start.
func Provider[T any](key, def string, impls map[string]func() T) (T, error) {
	name := Env(key, def)
	if build, ok := impls[name]; ok {
		return build(), nil
	}
	var zero T
	allowed := slices.Sorted(maps.Keys(impls))
	return zero, fmt.Errorf("%s=%q is not a known provider, allowed: %s", key, name, strings.Join(allowed, ", "))
}
