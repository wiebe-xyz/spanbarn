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

// commandTable maps each subcommand to its implementation.
var commandTable = map[string]func(args []string) error{
	"login":       cmdLogin,
	"init":        cmdInit,
	"projects":    cmdProjects,
	"services":    cmdServices,
	"flows":       cmdFlows,
	"traces":      cmdTraces,
	"trace":       cmdTrace,
	"logs":        cmdLogs,
	"metrics":     cmdMetrics,
	"prompts":     cmdPrompts,
	"deps":        cmdDeps,
	"database":    cmdDatabase,
	"service-map": cmdServiceMap,
	"tui":         cmdTUI,
}

func main() {
	if code := run(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// run executes the subcommand named by args[0] and returns the process exit
// code.
func run(args []string) int {
	if len(args) < 1 {
		printUsage()
		return 1
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("sb %s (built %s)\n", Version, BuildTime)
		return 0
	case "help", "--help", "-h":
		printUsage()
		return 0
	}

	command, ok := commandTable[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printUsage()
		return 1
	}

	if err := command(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Print(`sb — SpanBarn CLI

Usage: sb <command> [flags]

Setup:
  login         Authenticate: --api-key, --oidc (IamBarn browser login),
                --client-id/--client-secret (M2M), or --username/--password
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
  sb login --url https://spanbarn.example.com --oidc            # IamBarn, approve in browser
  sb login --url https://spanbarn.example.com --client-id ID --client-secret SECRET  # M2M
  sb login --url https://spanbarn.example.com --username admin  # prompts for password
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
