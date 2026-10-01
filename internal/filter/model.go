// Package filter is the shared span filter model. One JSON form serves the trace
// list and span list endpoints, saved queries and the URL query string of the
// web UI. The package validates the form and compiles it to a parameterised SQL
// predicate over the spans table. It depends on nothing else in the repo.
//
// Wire form:
//
//	{"match":"and","filters":[
//	  {"key":"kind","op":"=","value":"server"},
//	  {"match":"or","filters":[
//	    {"key":"url.path","op":"starts-with","value":"/api/v1/library"},
//	    {"key":"http.response.status_code","op":">=","value":500}]}]}
//
// The root is a group. A group holds conditions and at most one level of nested
// groups. A condition names a span column or an attribute key.
package filter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid is wrapped by every validation error so callers map it to a 400.
var ErrInvalid = errors.New("invalid filter")

// Op is a comparison operator.
type Op string

// Operators. >= and <= are included because ">= 500" is the usual way to ask
// for server errors.
const (
	OpEq         Op = "="
	OpNe         Op = "!="
	OpGt         Op = ">"
	OpLt         Op = "<"
	OpGte        Op = ">="
	OpLte        Op = "<="
	OpContains   Op = "contains"
	OpStartsWith Op = "starts-with"
	OpExists     Op = "exists"
	OpNotExists  Op = "does-not-exist"
	OpIn         Op = "in"
	OpNotIn      Op = "not-in"
)

// Match joins the members of a group.
const (
	MatchAnd = "and"
	MatchOr  = "or"
)

// Limits keep a filter from becoming a denial of service on the single reader.
const (
	maxConditions = 32
	maxValues     = 100
	maxKeyLen     = 200
	maxValueLen   = 1024
)

// Scalar is a filter value. JSON strings, numbers and booleans decode to its
// text form, so {"value":500} and {"value":"500"} are the same filter.
type Scalar string

// UnmarshalJSON accepts a string, number or boolean.
func (s *Scalar) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		*s = Scalar(t)
	case float64, bool:
		*s = Scalar(strings.TrimSpace(string(b)))
	case nil:
		*s = ""
	default:
		return fmt.Errorf("value must be a string, number or boolean")
	}
	return nil
}

// Node is a condition (Key set) or a group (Filters set).
type Node struct {
	Key     string   `json:"key,omitempty"`
	Op      Op       `json:"op,omitempty"`
	Value   Scalar   `json:"value,omitempty"`
	Values  []Scalar `json:"values,omitempty"`
	Match   string   `json:"match,omitempty"`
	Filters []Node   `json:"filters,omitempty"`
}

// Expr is the root group of a filter.
type Expr = Node

// Parse decodes and validates the JSON form. Empty input means no filter and
// returns a nil Expr.
func Parse(raw string) (*Expr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var e Expr
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if len(e.Filters) == 0 {
		return nil, nil
	}
	return &e, nil
}

// Validate checks the root group. A nil or empty root is valid.
func (e *Expr) Validate() error {
	if e == nil {
		return nil
	}
	if e.Key != "" || e.Op != "" {
		return invalid("the root must be a group with a filters list")
	}
	n := 0
	return validateGroup(e, 0, &n)
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func validateGroup(g *Node, depth int, count *int) error {
	switch g.Match {
	case "", MatchAnd, MatchOr:
	default:
		return invalid("match must be %q or %q", MatchAnd, MatchOr)
	}
	for i := range g.Filters {
		c := &g.Filters[i]
		if len(c.Filters) > 0 || c.Match != "" {
			if err := validateSubgroup(c, depth, count); err != nil {
				return err
			}
			continue
		}
		if *count++; *count > maxConditions {
			return invalid("at most %d conditions", maxConditions)
		}
		if err := validateCondition(c); err != nil {
			return err
		}
	}
	return nil
}

func validateSubgroup(c *Node, depth int, count *int) error {
	if depth >= 1 {
		return invalid("groups nest one level deep at most")
	}
	if c.Key != "" || c.Op != "" {
		return invalid("a node is either a condition or a group")
	}
	if len(c.Filters) == 0 {
		return invalid("a group needs at least one filter")
	}
	return validateGroup(c, depth+1, count)
}

func validateCondition(c *Node) error {
	if c.Key == "" || len(c.Key) > maxKeyLen || strings.ContainsAny(c.Key, "\"\\\x00") {
		return invalid("condition needs a key of up to %d characters without quotes or backslashes", maxKeyLen)
	}
	switch c.Op {
	case OpExists, OpNotExists:
		return nil
	case OpIn, OpNotIn:
		if len(c.Values) == 0 || len(c.Values) > maxValues {
			return invalid("%s on %q needs 1 to %d values", c.Op, c.Key, maxValues)
		}
		for _, v := range c.Values {
			if len(v) > maxValueLen {
				return invalid("value for %q is longer than %d bytes", c.Key, maxValueLen)
			}
		}
	case OpEq, OpNe, OpGt, OpLt, OpGte, OpLte, OpContains, OpStartsWith:
		if len(c.Value) > maxValueLen {
			return invalid("value for %q is longer than %d bytes", c.Key, maxValueLen)
		}
	default:
		return invalid("unknown operator %q on %q", c.Op, c.Key)
	}
	return nil
}

// Marshal returns the canonical JSON form, or "" for a nil or empty filter.
func Marshal(e *Expr) string {
	if e == nil || len(e.Filters) == 0 {
		return ""
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// FromLegacy maps the four fixed fields of the old saved query to a filter.
// It returns nil when all four are unset.
func FromLegacy(service, operation, status string, minDurationUs int64) *Expr {
	var fs []Node
	if service != "" {
		fs = append(fs, Node{Key: "service", Op: OpEq, Value: Scalar(service)})
	}
	if operation != "" {
		fs = append(fs, Node{Key: "name", Op: OpEq, Value: Scalar(operation)})
	}
	if status != "" {
		fs = append(fs, Node{Key: "status", Op: OpEq, Value: Scalar(status)})
	}
	if minDurationUs > 0 {
		fs = append(fs, Node{Key: "duration_us", Op: OpGte, Value: Scalar(fmt.Sprint(minDurationUs))})
	}
	if len(fs) == 0 {
		return nil
	}
	return &Expr{Match: MatchAnd, Filters: fs}
}
