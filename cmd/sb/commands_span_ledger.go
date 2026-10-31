package main

import (
	"flag"
	"net/url"
)

// cmdSpanLedger prints per project and 5-minute bucket how many spans were
// accepted, how many left through each exit, and how many were lost on the way
// to storage.
func cmdSpanLedger(args []string) error {
	fs := flag.NewFlagSet("span-ledger", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	data, err := client.query("/api/v1/span-ledger", params)
	if err != nil {
		return err
	}
	return emit(data)
}
