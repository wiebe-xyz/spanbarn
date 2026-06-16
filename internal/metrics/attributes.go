package metrics

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
)

// ParseAttributes decodes a stored OTLP attributes JSON object into string
// labels. Non-string values (numbers, bools) are stringified so they can still
// identify and group a series. Invalid or empty input yields an empty map.
func ParseAttributes(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	s := string(raw)
	if s == "{}" || s == "null" {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, v := range m {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", t)
		}
	}
	return out
}

// Fingerprint returns a stable identifier for a label set, independent of map
// iteration order. Two attribute maps with the same key/value pairs always
// produce the same fingerprint.
func Fingerprint(attrs map[string]string) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := fnv.New64a()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(attrs[k]))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// CanonicalAttributes serialises a label set to a JSON object with keys in
// sorted order (Go's json.Marshal already sorts map keys), suitable for storage.
func CanonicalAttributes(attrs map[string]string) string {
	if len(attrs) == 0 {
		return "{}"
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		return "{}"
	}
	return string(b)
}
