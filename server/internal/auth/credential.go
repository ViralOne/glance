package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ViralOne/glance/server/internal/ids"
)

// A password is stored as a PBKDF2-HMAC-SHA256 derivation with a random
// per-credential salt, encoded as:
//
//	pbkdf2-sha256$<iterations>$<salt base64>$<key base64>
//
// Plain SHA-256 was what this used before, and it is the wrong tool for a
// password: it is designed to be fast, so an attacker holding the database can
// try billions of guesses a second. A KDF is deliberately slow, which costs a
// legitimate login a few hundred milliseconds once and costs an attacker the
// same for every guess.
//
// PBKDF2 rather than argon2 or bcrypt because it is in the standard library as
// of Go 1.24, and Glance takes no dependency it can avoid. The iteration count
// follows OWASP's current guidance for PBKDF2-HMAC-SHA256.
const (
	pbkdf2Scheme     = "pbkdf2-sha256"
	pbkdf2Iterations = 210_000
	pbkdf2KeyLen     = 32
	saltLen          = 16
)

// MinPasswordLength is the shortest password accepted. Short enough not to be
// obstructive for a single-operator tool, long enough that the KDF is doing
// meaningful work rather than covering for a four-character password.
const MinPasswordLength = 8

// ErrWeakPassword is returned when a password is too short.
var ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLength)

// HashPassword derives a storable representation of password.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	return strings.Join([]string{
		pbkdf2Scheme,
		strconv.Itoa(pbkdf2Iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword reports whether password matches the stored encoding.
//
// A malformed or empty encoding fails rather than erroring, so a corrupted row
// locks the account instead of opening it.
func VerifyPassword(encoded, password string) bool {
	scheme, iterations, salt, want, err := parseEncoded(encoded)
	if err != nil {
		return false
	}
	if scheme != pbkdf2Scheme {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash reports whether a stored encoding should be replaced on the next
// successful login, because the iteration count has since been raised.
func NeedsRehash(encoded string) bool {
	scheme, iterations, _, _, err := parseEncoded(encoded)
	if err != nil {
		return true
	}
	return scheme != pbkdf2Scheme || iterations < pbkdf2Iterations
}

func parseEncoded(encoded string) (scheme string, iterations int, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return "", 0, nil, nil, errors.New("malformed password encoding")
	}
	iterations, err = strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return "", 0, nil, nil, errors.New("bad iteration count")
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[2]); err != nil {
		return "", 0, nil, nil, errors.New("bad salt")
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[3]); err != nil {
		return "", 0, nil, nil, errors.New("bad key")
	}
	if len(salt) == 0 || len(key) == 0 {
		return "", 0, nil, nil, errors.New("empty salt or key")
	}
	return parts[0], iterations, salt, key, nil
}

// GeneratePassword returns a readable random password for the first-boot
// credential: 24 lowercase base32 characters, 120 bits of entropy, prefixed so
// it is obvious in a log line what it is. Lowercase base32 avoids any question
// of case or lookalike punctuation when it is copied out of a log and typed.
func GeneratePassword() string { return "glance-" + ids.Random(15) }
