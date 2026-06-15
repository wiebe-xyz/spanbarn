package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "login":
		err = cmdLogin(os.Args[2:])
	case "init":
		err = cmdInit(os.Args[2:])
	case "projects":
		err = cmdProjects(os.Args[2:])
	case "services":
		err = cmdServices(os.Args[2:])
	case "flows":
		err = cmdFlows(os.Args[2:])
	case "traces":
		err = cmdTraces(os.Args[2:])
	case "trace":
		err = cmdTrace(os.Args[2:])
	case "logs":
		err = cmdLogs(os.Args[2:])
	case "metrics":
		err = cmdMetrics(os.Args[2:])
	case "prompts":
		err = cmdPrompts(os.Args[2:])
	case "deps":
		err = cmdDeps(os.Args[2:])
	case "database":
		err = cmdDatabase(os.Args[2:])
	case "service-map":
		err = cmdServiceMap(os.Args[2:])
	case "tui":
		err = cmdTUI(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("sb %s (built %s)\n", Version, BuildTime)
		return
	case "help", "--help", "-h":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		if err == flag.ErrHelp {
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`sb — SpanBarn CLI

Usage: sb <command> [flags]

Setup:
  login         Authenticate with a SpanBarn instance (--url, --api-key)
  init          Set the project for this directory (writes .spanbarn.json)
  projects      List projects

Telemetry (JSON by default; add --output table for a table):
  flows         Problematic flows grouped by root operation (errors, latency)
  traces        Search traces (--errors, --service, --min-duration-us, ...)
  trace <id>    Full span tree for one trace
  logs          Query logs (--trace-id, --severity, --search, ...)
  services      Per-service error rate and latency percentiles
  metrics       OTLP metrics: 'metrics names' | 'metrics series --name N'
  prompts       LLM/prompt samples: 'prompts' | 'prompts detail --name N'
  deps          Service dependency graph
  database      Aggregated database query patterns
  service-map   Full service topology

Interactive:
  tui           Trace/error explorer (drill into spans + correlated logs)

Common flags: --project SLUG, --from, --to, --output json|table

Examples:
  sb login --url https://spanbarn.example.com --api-key KEY
  sb init --project my-app
  sb flows --errors
  sb traces --errors --service api --limit 20
  sb trace 7f3c... | jq '.spans[] | select(.status=="error")'
  sb logs --trace-id 7f3c... --severity 17
  sb tui --errors

Auth: create a read key with
  spanbarn apikey create --project SLUG --name cli --scope read

Config: ~/.config/spanbarn/cli.json (override with SB_CONFIG)
Per-project: .spanbarn.json ({"project":"slug"}) discovered by walking up.
`)
}
