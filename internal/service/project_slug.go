package service

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidProjectSlug marks a slug that names an environment or a legacy
// prefix. One project holds a product; testing, staging and production are an
// environment on the record (#558).
var ErrInvalidProjectSlug = errors.New("invalid project slug")

// environmentSlugSuffixes are the endings that make a slug an environment
// copy of another project.
var environmentSlugSuffixes = []string{
	"-prod", "-production", "-staging", "-stage", "-test", "-testing", "-e2e", "-dev",
}

// ValidateProjectSlug refuses a slug that ends in an environment name or
// starts with "rr-". The error names the environment field, which is where the
// value belongs: an ingest key's environment or the sender's
// deployment.environment.
func ValidateProjectSlug(slug string) error {
	s := strings.ToLower(strings.TrimSpace(slug))
	for _, suffix := range environmentSlugSuffixes {
		if strings.HasSuffix(s, suffix) {
			return fmt.Errorf("%w: %q ends in %q; use one project per product and send the environment as deployment.environment (or set it on the ingest key)", ErrInvalidProjectSlug, slug, suffix)
		}
	}
	if strings.HasPrefix(s, "rr-") {
		return fmt.Errorf("%w: %q starts with \"rr-\"; use the product or site name and send the service as service.name", ErrInvalidProjectSlug, slug)
	}
	return nil
}
