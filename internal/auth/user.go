package auth

import (
	"crypto/subtle"
	"errors"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned when username/password don't match.
var ErrInvalidCredentials = errors.New("invalid credentials")

// UserLookup abstracts the repository method needed for user authentication.
type UserLookup interface {
	GetUserByUsername(username string) (UserRecord, error)
}

// UserRecord holds the fields returned by a user lookup.
type UserRecord struct {
	ID           int64
	Username     string
	PasswordHash string
}

// UserAuthenticator handles username/password authentication using bcrypt.
type UserAuthenticator struct {
	repo   UserLookup
	logger *slog.Logger
}

// NewUserAuthenticator creates a UserAuthenticator backed by a user repository.
func NewUserAuthenticator(repo UserLookup, logger *slog.Logger) *UserAuthenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &UserAuthenticator{repo: repo, logger: logger}
}

// Authenticate checks a username/password pair against the database.
func (a *UserAuthenticator) Authenticate(username, password string) error {
	if username == "" || password == "" {
		return ErrInvalidCredentials
	}

	user, err := a.repo.GetUserByUsername(username)
	if err != nil {
		// Perform a dummy bcrypt compare to avoid timing leaks on unknown users.
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$000000000000000000000uVGW.0nPSViNwwYDqFSaaQm7aIoJbazy"),
			[]byte(password),
		)
		return ErrInvalidCredentials
	}

	// Timing-safe username comparison (defense-in-depth).
	if subtle.ConstantTimeCompare([]byte(username), []byte(user.Username)) != 1 {
		return ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return ErrInvalidCredentials
	}

	return nil
}

// HashPassword hashes a plaintext password using bcrypt. Useful for CLI user creation.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
