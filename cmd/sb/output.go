package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// outputMode is set by the shared --output flag (json|table). JSON is the
// default so the CLI is scriptable by users and agents.
var outputMode = "json"

// emit writes an API response either as indented JSON (default) or as a table.
func emit(data json.RawMessage) error {
	if outputMode == "table" {
		if renderTable(data) {
			return nil
		}
		// Not tabular — fall through to JSON.
	}
	return writeRaw(data)
}

// writeOut prints an arbitrary value as indented JSON.
func writeOut(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// writeRaw pretty-prints raw JSON.
func writeRaw(data json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		os.Stdout.Write(data)
		fmt.Println()
		return nil
	}
	buf.WriteByte('\n')
	_, err := buf.WriteTo(os.Stdout)
	return err
}

// renderTable prints a JSON array of objects as an aligned table. It returns
// false when the payload is not an array of objects (caller should fall back to
// JSON).
func renderTable(data json.RawMessage) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return false
	}
	if len(rows) == 0 {
		fmt.Println("(no rows)")
		return true
	}

	cols := orderedKeys(rows[0])
	if len(cols) == 0 {
		return false
	}

	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(r, &obj); err != nil {
			return false
		}
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = scalarString(obj[c])
		}
		tableRows = append(tableRows, row)
	}
	printTable(cols, tableRows)
	return true
}

// orderedKeys extracts an object's top-level keys in document order.
func orderedKeys(obj json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(obj))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		t, err := dec.Token()
		if err != nil {
			break
		}
		switch d := t.(type) {
		case json.Delim:
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
			continue
		case string:
			if depth == 0 {
				keys = append(keys, d)
				// Skip this key's value.
				if err := skipValue(dec); err != nil {
					return keys
				}
			}
		}
	}
	return keys
}

// skipValue consumes the next JSON value from the decoder.
func skipValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); ok && (d == '{' || d == '[') {
		depth := 1
		for depth > 0 {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			if dd, ok := t.(json.Delim); ok {
				if dd == '{' || dd == '[' {
					depth++
				} else {
					depth--
				}
			}
		}
	}
	return nil
}

// scalarString renders a JSON value for a table cell. Nested objects/arrays are
// shown compact and truncated.
func scalarString(v json.RawMessage) string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return ""
	}
	switch v[0] {
	case '"':
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
	case '{', '[':
		s := string(v)
		if len(s) > 60 {
			s = s[:57] + "..."
		}
		return s
	}
	if string(v) == "null" {
		return ""
	}
	return string(v)
}

// printTable prints a simple aligned table to stdout.
func printTable(headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var b strings.Builder
	for i, h := range headers {
		if i > 0 {
			b.WriteString("  ")
		}
		fmt.Fprintf(&b, "%-*s", widths[i], h)
	}
	b.WriteByte('\n')
	for i, w := range widths {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(strings.Repeat("-", w))
	}
	b.WriteByte('\n')
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			if i < len(widths) {
				fmt.Fprintf(&b, "%-*s", widths[i], cell)
			}
		}
		b.WriteByte('\n')
	}
	fmt.Print(b.String())
}
