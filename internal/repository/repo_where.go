package repository

import (
	"strings"
	"time"
)

// whereBuilder accumulates SQL conditions and their bind arguments.
type whereBuilder struct {
	where []string
	args  []any
}

// add appends one condition with its bind arguments.
func (b *whereBuilder) add(cond string, args ...any) {
	b.where = append(b.where, cond)
	b.args = append(b.args, args...)
}

// addNotIn appends "col NOT IN (...)" when values is not empty.
func (b *whereBuilder) addNotIn(col string, values []string) {
	if len(values) == 0 {
		return
	}
	placeholders := strings.Repeat("?,", len(values))
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	b.add(col+" NOT IN ("+placeholders[:len(placeholders)-1]+")", args...)
}

// addTimeRange appends inclusive bounds on col for each non-zero time.
func (b *whereBuilder) addTimeRange(col string, from, to time.Time) {
	if !from.IsZero() {
		b.add(col+" >= ?", from)
	}
	if !to.IsZero() {
		b.add(col+" <= ?", to)
	}
}
