package main

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// scenario is one reproducible demo. It asserts its own outcome.
type scenario struct {
	name        string
	description string
	service     string // service whose traces the run's trace URL links to
	run         func(ctx context.Context, env environment) error
}

type environment struct {
	orderBaseURL     string
	inventoryBaseURL string
	jaegerURL        string
}

var scenarios = map[string]scenario{}

func register(s scenario) {
	scenarios[s.name] = s
}

func scenarioNames() []string {
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// runScenario runs the named scenario and reports the window it ran in and
// the service its traces belong to, so the caller can build a trace query
// bounded to exactly that window without looking the scenario up again.
func runScenario(ctx context.Context, name string, env environment) (window, string, error) {
	s, ok := scenarios[name]
	if !ok {
		return window{}, "", fmt.Errorf("unknown scenario %q; known scenarios: %v", name, scenarioNames())
	}
	fmt.Printf("running scenario %s: %s\n", s.name, s.description)

	start := time.Now()
	err := s.run(ctx, env)
	return window{start: start, end: time.Now()}, s.service, err
}

// window is the wall-clock span a scenario occupied.
type window struct {
	start time.Time
	end   time.Time
}

// traceURL builds a Jaeger search bounded to the window. The v3 API requires a
// time range, so a query without one is rejected; padding absorbs clock skew
// between this process and the collector, and the delay before batched spans
// are exported.
func (w window) traceURL(jaegerBase, service string) string {
	const pad = 2 * time.Minute
	return fmt.Sprintf("%s/search?service=%s&start=%d&end=%d&limit=20",
		jaegerBase,
		service,
		w.start.Add(-pad).UnixMicro(),
		w.end.Add(pad).UnixMicro(),
	)
}
