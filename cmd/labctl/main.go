// Command labctl drives the lab: it runs reproducible failure scenarios and
// injects infrastructure chaos.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	orderURL := flag.String("order-url", envOr("LABCTL_ORDER_URL", "http://localhost:8080"),
		"base URL of the order service")
	jaegerURL := flag.String("jaeger-url", envOr("LABCTL_JAEGER_URL", "http://localhost:16686"),
		"base URL of the Jaeger UI")
	flag.Parse()

	if flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	env := environment{orderBaseURL: *orderURL, jaegerURL: *jaegerURL}

	switch flag.Arg(0) {
	case "scenario":
		if flag.NArg() < 2 {
			fmt.Fprintf(os.Stderr, "scenario requires a name; known scenarios: %v\n", scenarioNames())
			os.Exit(2)
		}
		if err := runScenario(ctx, flag.Arg(1), env); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("PASS\n")
		fmt.Printf("trace: %s/search?service=order\n", env.jaegerURL)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: labctl [flags] scenario <name>\n\nknown scenarios:\n")
	for _, name := range scenarioNames() {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", name, scenarios[name].description)
	}
	flag.PrintDefaults()
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
