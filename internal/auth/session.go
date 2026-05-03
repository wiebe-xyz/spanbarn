package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrSessionExpired is returned when a session token has expired.
var ErrSessionExpired = errors.New("session expired")

// ErrSessionInvalid is returned when a session token is malformed or tampered.
var ErrSessionInvalid = errors.New("session invalid")

// sessionClaims are the JSON claims inside a session token.
type sessionClaims struct {
	Sub   string `json:"sub"`
	Exp   int64  `json:"exp"`
	Nonce string `json:"nonce"`
}

// SessionManager creates and validates HMAC-SHA256 session tokens.
type SessionManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // overridable for tests
}

// NewSessionManager creates a SessionManager. If secret is empty, a random one
// is generated (sessions won't survive restarts). ttlSeconds <= 0 defaults to 12h.
func NewSessionManager(secret string, ttlSeconds int64) *SessionManager {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = randomHex(32)
	}
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &SessionManager{
		secret: []byte(secret),
		ttl:    ttl,
		now:    time.Now,
	}
}

// Create generates a new session token for the given username.
func (m *SessionManager) Create(username string) (string, error) {
	claims := sessionClaims{
		Sub:   username,
		Exp:   m.now().Add(m.ttl).Unix(),
		Nonce: randomHex(16),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	sig := signHMAC(m.secret, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, nil
}

// Validate checks a session token and returns the username on success.
func (m *SessionManager) Validate(token string) (string, error) {
	token = strings.TrimSpace(token)
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", ErrSessionInvalid
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrSessionInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrSessionInvalid
	}

	expected := signHMAC(m.secret, payload)
	if !hmac.Equal(sig, expected) {
		return "", ErrSessionInvalid
	}

	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrSessionInvalid
	}

	if claims.Sub == "" {
		return "", ErrSessionInvalid
	}
	if claims.Exp <= m.now().Unix() {
		return "", ErrSessionExpired
	}

	return claims.Sub, nil
}

// TTL returns the session time-to-live duration.
func (m *SessionManager) TTL() time.Duration {
	return m.ttl
}

func signHMAC(secret, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return mac.Sum(nil)
}

// MakeExpiredToken creates a session token that is already expired.
// This is exported for use in integration/middleware tests outside this package.
func MakeExpiredToken(secret, username string) string {
	sm := NewSessionManager(secret, 1)
	sm.now = func() time.Time {
		return time.Now().Add(-2 * time.Minute)
	}
	token, _ := sm.Create(username)
	return token
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use current time (very unlikely).
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return hex.EncodeToString(b)
}
