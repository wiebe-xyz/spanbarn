package aggregation

import "strings"

// httpStatusKeys are the attributes a span's response code is read from, in
// order: the current semantic convention first, then the older one. The
// spans.http_status column reads the same two.
var httpStatusKeys = []string{`"http.response.status_code"`, `"http.status_code"`}

// httpStatusOf returns the response code in a span's attributes JSON, or 0.
// It looks the keys up as text instead of decoding the whole object, because
// it runs for every root span on the write path. A code may be stored as a
// number or as a string of digits.
func httpStatusOf(attrs string) int64 {
	for _, key := range httpStatusKeys {
		if code := codeAfterKey(attrs, key); code > 0 {
			return code
		}
	}
	return 0
}

func codeAfterKey(attrs, key string) int64 {
	i := strings.Index(attrs, key)
	if i < 0 {
		return 0
	}
	rest := strings.TrimLeft(attrs[i+len(key):], " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return 0
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	rest = strings.TrimPrefix(rest, `"`)
	var code int64
	n := 0
	for n < len(rest) && n < 4 && rest[n] >= '0' && rest[n] <= '9' {
		code = code*10 + int64(rest[n]-'0')
		n++
	}
	if n == 0 || n > 3 {
		return 0
	}
	return code
}
