package main

import (
	"context"
	"fmt"
	"io"
)

// cmdRetention is implemented by its workstream (issue #235).
func cmdRetention(_ context.Context, _ options, _ io.Writer) error {
	return fmt.Errorf("retention: not implemented yet")
}
