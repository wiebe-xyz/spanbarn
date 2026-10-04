package main

import (
	"context"
	"fmt"
	"io"
)

// cmdContention is implemented by its workstream (issue #235).
func cmdContention(_ context.Context, _ options, _ io.Writer) error {
	return fmt.Errorf("contention: not implemented yet")
}
