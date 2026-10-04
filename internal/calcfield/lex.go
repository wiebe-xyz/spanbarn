package calcfield

import (
	"fmt"
	"strconv"
	"strings"
)

type tokType int

const (
	tEOF tokType = iota
	tNumber
	tString
	tWord // bare identifier, keyword or function name
	tQuoted
	tOp
	tLParen
	tRParen
	tComma
)

type token struct {
	typ  tokType
	text string
	num  any
}

func (t token) describe() string {
	switch t.typ {
	case tEOF:
		return "end of the expression"
	case tNumber:
		return fmt.Sprintf("number %v", t.num)
	case tString:
		return "a string"
	case tLParen:
		return `"("`
	case tRParen:
		return `")"`
	case tComma:
		return `","`
	}
	return fmt.Sprintf("%q", t.text)
}

var reserved = map[string]bool{"and": true, "or": true, "not": true}

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordPart(c byte) bool { return isWordStart(c) || isDigit(c) || c == '.' }

// lex splits src into tokens. Any character outside the grammar is an error, so
// quotes, semicolons and comment markers never reach the parser.
func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		t, n, err := lexOne(src[i:])
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		i += n
	}
	return append(toks, token{typ: tEOF}), nil
}

func lexOne(s string) (token, int, error) {
	c := s[0]
	switch {
	case isDigit(c):
		return lexNumber(s)
	case isWordStart(c):
		return lexWord(s)
	case c == '\'':
		return lexString(s)
	case c == '`':
		return lexQuoted(s)
	}
	return lexSymbol(s)
}

func lexWord(s string) (token, int, error) {
	n := 1
	for n < len(s) && isWordPart(s[n]) {
		n++
	}
	if n > maxIdentLen {
		return token{}, 0, invalid("identifier longer than %d characters", maxIdentLen)
	}
	text := s[:n]
	if low := strings.ToLower(text); reserved[low] {
		text = low
	}
	return token{typ: tWord, text: text}, n, nil
}

func lexNumber(s string) (token, int, error) {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	isFloat := false
	if n+1 < len(s) && s[n] == '.' && isDigit(s[n+1]) {
		isFloat = true
		n++
		for n < len(s) && isDigit(s[n]) {
			n++
		}
	}
	if n < len(s) && (isWordStart(s[n]) || s[n] == '.') {
		return token{}, 0, invalid("a number cannot be followed by %q", s[n:n+1])
	}
	lit := s[:n]
	if !isFloat {
		if v, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return token{typ: tNumber, text: lit, num: v}, n, nil
		}
	}
	v, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return token{}, 0, invalid("bad number %q", lit)
	}
	return token{typ: tNumber, text: lit, num: v}, n, nil
}

// lexString reads 'text' where ” stands for one quote.
func lexString(s string) (token, int, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != '\'' {
			if s[i] == 0 {
				return token{}, 0, invalid("a string cannot hold a NUL byte")
			}
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		return token{typ: tString, text: b.String()}, i + 1, nil
	}
	return token{}, 0, invalid("unterminated string")
}

// lexQuoted reads `key` for an identifier with characters a bare word cannot
// hold. The characters a filter key refuses stay refused.
func lexQuoted(s string) (token, int, error) {
	end := strings.IndexByte(s[1:], '`')
	if end < 0 {
		return token{}, 0, invalid("unterminated `identifier`")
	}
	name := s[1 : 1+end]
	if name == "" || len(name) > maxIdentLen || strings.ContainsAny(name, "\"\\\x00") {
		return token{}, 0, invalid("a `identifier` holds 1 to %d characters without double quotes or backslashes", maxIdentLen)
	}
	return token{typ: tQuoted, text: name}, end + 2, nil
}

func lexSymbol(s string) (token, int, error) {
	if len(s) >= 2 {
		switch two := s[:2]; two {
		case "<=", ">=", "!=", "<>", "==":
			return token{typ: tOp, text: two}, 2, nil
		}
	}
	switch c := s[0]; c {
	case '+', '-', '*', '/', '%', '=', '<', '>':
		return token{typ: tOp, text: string(c)}, 1, nil
	case '(':
		return token{typ: tLParen, text: "("}, 1, nil
	case ')':
		return token{typ: tRParen, text: ")"}, 1, nil
	case ',':
		return token{typ: tComma, text: ","}, 1, nil
	}
	return token{}, 0, invalid("unexpected character %q", s[:1])
}
