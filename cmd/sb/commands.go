package main

import (
	"flag"
	"net/url"
	"time"
)

// commonFlags registers the flags shared by every data command: --output and
// --project. It binds --output directly to the global outputMode.
func commonFlags(fs *flag.FlagSet) *string {
	fs.StringVar(&outputMode, "output", "json", "output format: json|table")
	return fs.String("project", "", "project slug (overrides .spanbarn.json and config default)")
}

// addTimeFlags registers --from/--to and returns their pointers.
func addTimeFlags(fs *flag.FlagSet) (from, to *string) {
	from = fs.String("from", "", "start time (RFC3339 or unix seconds; default: 1h ago)")
	to = fs.String("to", "", "end time (RFC3339 or unix seconds; default: now)")
	return from, to
}

// applyTimeRange sets from/to on params, defaulting to the last hour.
func applyTimeRange(params url.Values, from, to string) {
	if from != "" {
		params.Set("from", from)
	} else {
		params.Set("from", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	}
	if to != "" {
		params.Set("to", to)
	} else {
		params.Set("to", time.Now().UTC().Format(time.RFC3339))
	}
}

// scopedClient builds a client and resolves the project slug to scope queries.
func scopedClient(projectFlag string) (*Client, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	c.project = resolveProject(projectFlag, c.cfg)
	return c, nil
}

// runQueryCmd handles the common CLI shape: the standard project + time-range
// flags, an optional --service filter, then a GET to endpoint and emit.
// Commands with additional flags keep their own implementation.
func runQueryCmd(name, endpoint string, args []string, withService bool) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	var service *string
	if withService {
		service = fs.String("service", "", "filter by service")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if service != nil && *service != "" {
		params.Set("service", *service)
	}
	data, err := client.query(endpoint, params)
	if err != nil {
		return err
	}
	return emit(data)
}
