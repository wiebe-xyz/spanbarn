package filter

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEmptyIsNoFilter(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", `{"filters":[]}`} {
		e, err := Parse(raw)
		if err != nil || e != nil {
			t.Fatalf("Parse(%q) = %v, %v; want nil, nil", raw, e, err)
		}
	}
}

func TestParseAcceptsNumberBoolAndStringValues(t *testing.T) {
	e, err := Parse(`{"filters":[{"key":"a","op":"=","value":500},{"key":"b","op":"=","value":true},{"key":"c","op":"=","value":"x"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got := []Scalar{e.Filters[0].Value, e.Filters[1].Value, e.Filters[2].Value}
	want := []Scalar{"500", "true", "x"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"bad json":         `{`,
		"unknown field":    `{"filters":[{"key":"a","op":"=","bogus":1}]}`,
		"unknown op":       `{"filters":[{"key":"a","op":"~","value":"x"}]}`,
		"missing key":      `{"filters":[{"op":"=","value":"x"}]}`,
		"quote in key":     `{"filters":[{"key":"a\"b","op":"=","value":"x"}]}`,
		"in without vals":  `{"filters":[{"key":"a","op":"in"}]}`,
		"bad match":        `{"match":"xor","filters":[{"key":"a","op":"exists"}]}`,
		"two group levels": `{"filters":[{"match":"or","filters":[{"match":"and","filters":[{"key":"a","op":"exists"}]}]}]}`,
		"empty group":      `{"filters":[{"match":"or"}]}`,
		"condition root":   `{"key":"a","op":"exists"}`,
		"mixed node":       `{"filters":[{"key":"a","op":"exists","filters":[{"key":"b","op":"exists"}]}]}`,
		"array value":      `{"filters":[{"key":"a","op":"=","value":[1]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(raw)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestParseRejectsTooManyConditions(t *testing.T) {
	var parts []string
	for i := 0; i < maxConditions+1; i++ {
		parts = append(parts, `{"key":"a","op":"exists"}`)
	}
	_, err := Parse(`{"filters":[` + strings.Join(parts, ",") + `]}`)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	raw := `{"match":"and","filters":[{"key":"kind","op":"=","value":"server"},{"match":"or","filters":[{"key":"url.path","op":"starts-with","value":"/api"},{"key":"x","op":"in","values":["1","2"]}]}]}`
	e, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := Marshal(e); got != raw {
		t.Fatalf("round trip\n got %s\nwant %s", got, raw)
	}
	if Marshal(nil) != "" {
		t.Fatal("nil marshals to empty string")
	}
}

func TestFromLegacy(t *testing.T) {
	if FromLegacy("", "", "", 0) != nil {
		t.Fatal("no fields means no filter")
	}
	e := FromLegacy("api", "GET /x", "error", 2000)
	want := `{"match":"and","filters":[{"key":"service","op":"=","value":"api"},{"key":"name","op":"=","value":"GET /x"},{"key":"status","op":"=","value":"error"},{"key":"duration_us","op":">=","value":"2000"}]}`
	if got := Marshal(e); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileEmpty(t *testing.T) {
	sql, args, err := Compile(nil)
	if sql != "" || args != nil || err != nil {
		t.Fatalf("got %q %v %v", sql, args, err)
	}
}

func TestCompileNeedsNumberForIntColumn(t *testing.T) {
	e := &Expr{Filters: []Node{{Key: "duration_us", Op: OpGt, Value: "slow"}}}
	if _, _, err := Compile(e); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}
