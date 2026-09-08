package main

import (
	"context"
	"fmt"
	"sort"
)

// scenario is one reproducible demo. It asserts its own outcome.
type scenario struct {
	name        string
	description string
	run         func(ctx context.Context, env environment) error
}

type environment struct {
	orderBaseURL string
	jaegerURL    string
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

func runScenario(ctx context.Context, name string, env environment) error {
	s, ok := scenarios[name]
	if !ok {
		return fmt.Errorf("unknown scenario %q; known scenarios: %v", name, scenarioNames())
	}
	fmt.Printf("running scenario %s: %s\n", s.name, s.description)
	return s.run(ctx, env)
}
