package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Web sessions are token-bound server-side rows (web_sessions table): the
// browser cookie holds only an opaque random handle, and the server keys the
// row by the handle's SHA-256. Nothing about the session (username, expiry,
// IdP tokens) lives client-side, so revoking the row revokes the session
// instantly on every replica that can read the table.

// sessionTokenBytes is the entropy of an opaque session handle. 32 bytes =
// 256 bits, far beyond brute-force reach for a 12h-lived credential.
const sessionTokenBytes = 32

// NewSessionToken returns a fresh opaque session handle for the `session`
// cookie. The value is random — it carries no claims and cannot be validated
// offline; the server must look up its hash in the web_sessions table.
//
// The base64url alphabet contains no ".", so an opaque handle can never be
// mistaken for an IamBarn access-token JWT (two dots) on the shared
// Authorization: Bearer path.
func NewSessionToken() string {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; if it does, the system entropy source
		// is broken and continuing would mint a predictable session handle.
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashSessionToken derives the web_sessions primary key from a cookie value.
// Storing only the SHA-256 means a leaked database (or backup) contains no
// usable session credentials.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
