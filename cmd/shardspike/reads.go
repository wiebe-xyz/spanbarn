package main

import (
	"context"
	"fmt"
	"io"
)

// cmdReads is implemented by its workstream (issue #235).
func cmdReads(_ context.Context, _ options, _ io.Writer) error {
	return fmt.Errorf("reads: not implemented yet")
}
