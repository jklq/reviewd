// Package harness registers trusted, compiled-in harness drivers, like database/sql.
package harness

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

type Options struct {
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

type Route struct {
	Method         string // Empty means POST; GET is reserved for fixed model catalogs.
	Query          string // Optional exact SDK query string.
	Path           string
	URL            string
	Headers        http.Header
	ForwardHeaders []string // Non-authentication SDK protocol metadata only.
}

// Never put credentials in Script or Environment.
type Launch struct {
	Script      string
	Environment map[string]string
	Routes      []Route
}

type Driver interface {
	Validate(Options) error
	Prepare(Options, map[string]string) (Launch, error)
}

var registry = struct {
	sync.RWMutex
	drivers map[string]Driver
}{drivers: make(map[string]Driver)}

func Register(name string, driver Driver) {
	registry.Lock()
	defer registry.Unlock()
	if name == "" || driver == nil {
		panic("harness: empty name or nil driver")
	}
	if _, ok := registry.drivers[name]; ok {
		panic("harness: duplicate driver " + name)
	}
	registry.drivers[name] = driver
}

func Lookup(name string) (Driver, error) {
	registry.RLock()
	defer registry.RUnlock()
	d, ok := registry.drivers[name]
	if !ok {
		return nil, fmt.Errorf("harness driver %q is not linked into this binary", name)
	}
	return d, nil
}

func Drivers() []string {
	registry.RLock()
	defer registry.RUnlock()
	names := make([]string, 0, len(registry.drivers))
	for name := range registry.drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func RequireCredential(values map[string]string, name string) (string, error) {
	value := values[name]
	if value == "" {
		return "", fmt.Errorf("driver requires %s", name)
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return "", fmt.Errorf("invalid %s credential", name)
		}
	}
	return value, nil
}

func ValidateOptions(o Options, efforts ...string) error {
	if o.Model == "" {
		return fmt.Errorf("driver requires model")
	}
	if o.ReasoningEffort == "" {
		return nil
	}
	for _, effort := range efforts {
		if effort == o.ReasoningEffort {
			return nil
		}
	}
	return fmt.Errorf("unsupported reasoning_effort %q", o.ReasoningEffort)
}

// ValidateOption rejects control characters in an optional operator-provided
// value. Allowed names are model-catalog specific, so drivers cannot enumerate them.
func ValidateOption(name, value string) error {
	for _, c := range value {
		if c < 32 || c == 127 {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}

func SDKEnvironment(o Options) map[string]string {
	return map[string]string{"REVIEWD_MODEL": o.Model, "REVIEWD_REASONING_EFFORT": o.ReasoningEffort, "REVIEWD_SERVICE_TIER": o.ServiceTier, "REVIEWD_MODEL_URL": "http://127.0.0.1:39123"}
}
