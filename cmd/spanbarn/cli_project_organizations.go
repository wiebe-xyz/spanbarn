package main

import (
	"fmt"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// splitOrganizations reads a comma separated organization list, dropping blanks.
func splitOrganizations(raw string) []string {
	var out []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// normalizeOrganizations stores the list without blanks or spaces.
func normalizeOrganizations(raw string) string {
	return strings.Join(splitOrganizations(raw), ",")
}

// setProjectOrganizations stores the project's extra organization list and
// says how many it holds.
func setProjectOrganizations(svc *service.ProjectService, id int64, slug, raw string) error {
	if err := svc.SetOrganizations(id, normalizeOrganizations(raw)); err != nil {
		return err
	}
	fmt.Printf("%s: organizations set, %d listed\n", slug, len(splitOrganizations(raw)))
	return nil
}

// checkProjectSetOptions validates the flags of project set; an organization
// list counts as something to set.
func checkProjectSetOptions(setOrgs bool, traceRatio, logHours int, op, usage string) error {
	if setOrgs && traceRatio == -1 && logHours == -1 {
		return nil
	}
	return checkProjectSetFlags(traceRatio, logHours, op, usage)
}
