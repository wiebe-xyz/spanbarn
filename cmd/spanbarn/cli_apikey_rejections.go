package main

import (
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// runAPIKeyRejections prints the keys requests used although no stored key
// matches: fingerprint, reason, requests, first and newest request and, for a
// key revoked since the counters exist, its id and name (#572).
func runAPIKeyRejections(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("apikey rejections", flag.ContinueOnError)
	since := fs.Duration("since", 0, "Only keys refused within this long (default: everything the counters keep)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var from time.Time
	if *since > 0 {
		from = time.Now().Add(-*since)
	}
	rows, err := service.NewKeyRejectionService(repo).Since(from)
	if err != nil {
		return fmt.Errorf("list key rejections: %w", err)
	}
	headers := []string{"Fingerprint", "Reason", "Requests", "First seen", "Last seen", "Key", "Name"}
	table := make([][]string, 0, len(rows))
	for _, k := range rows {
		reason, id, name := "unknown", "", ""
		if k.Revoked {
			reason, id, name = "revoked", strconv.FormatInt(k.KeyID, 10), k.KeyName
		}
		table = append(table, []string{
			k.Fingerprint, reason, strconv.FormatInt(k.Count, 10),
			k.FirstSeen.Format(time.RFC3339), k.LastSeen.Format(time.RFC3339), id, name,
		})
	}
	printTable(headers, table)
	return nil
}
