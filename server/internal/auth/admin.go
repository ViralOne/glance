// Package auth guards the admin UI and API with a single administrator login.
//
// The credential comes from the environment when GLANCE_ADMIN_USER and
// GLANCE_ADMIN_PASSWORD are set, and otherwise from the database, where it can
// be changed from the dashboard. If neither exists, one is generated on first
// boot and printed once to the log: an instance is never silently public, which
// is what happened before when the environment variables were simply forgotten.
// GLANCE_DISABLE_AUTH is the deliberate way to run without a login.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ViralOne/glance/server/internal/ids"
)

// SessionCookie is the name of the admin session cookie.
const SessionCookie = "glance_session"

// SessionTTL is how long an admin login lasts.
const SessionTTL = 30 * 24 * time.Hour

// Admin guards the dashboard and the admin endpoints. When Enabled is false
// everything is open, which now only happens if the operator asked for it.
//
// Sessions are kept in memory and, when a SessionStore is set, in SQLite so
// they survive restarts. A session is only valid for the password hash it was
// created with, so changing the password logs everyone out.
type Admin struct {
	store *SessionStore

	mu       sync.Mutex
	username string
	password string // PBKDF2 encoding; see credential.go
	enabled  bool
	// fromEnv means the credential came from the environment and cannot be
	// changed from the dashboard: the environment would override it on the
	// next restart, so offering the change would be a lie.
	fromEnv bool
	source  string

	sessions map[string]time.Time // token hash -> expiry (cache)
	// verified caches credentials already proven correct, so repeated HTTP
	// Basic requests do not each pay the KDF. A wrong guess is never cached,
	// so brute force still costs an attacker the full derivation every time.
	verified map[string]time.Time
	now      func() time.Time
}

// verifiedTTL is how long a proven credential stays cached.
const verifiedTTL = time.Minute

// NewAdmin returns an Admin with no credential yet; call SetEnv or Load.
func NewAdmin(store *SessionStore) *Admin {
	return &Admin{
		sessions: map[string]time.Time{},
		verified: map[string]time.Time{},
		now:      time.Now,
		store:    store,
	}
}

// SetEnv installs a credential from the environment, which takes precedence
// over anything stored and cannot be changed from the dashboard.
func (a *Admin) SetEnv(username, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.username, a.password, a.enabled, a.fromEnv, a.source = username, hash, true, true, SourceEnv
	a.credentialChangedLocked()
	return nil
}

// SetStored installs a credential read from the database.
func (a *Admin) SetStored(c Credential) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fromEnv {
		return // the environment wins
	}
	a.username, a.password, a.enabled, a.source = c.Username, c.PasswordHash, true, c.Source
	a.credentialChangedLocked()
}

// credentialChangedLocked drops both caches after the credential changes.
//
// Clearing the session cache is not tidiness. Valid() answers from that cache
// without re-checking the password, and only the database fallback compares the
// stored hash — so a session opened against the old password kept working after
// a change until the process restarted. Emptying the cache forces every session
// back through the database, where the hash comparison rejects it.
//
// Callers must hold a.mu.
func (a *Admin) credentialChangedLocked() {
	a.sessions = map[string]time.Time{}
	a.verified = map[string]time.Time{}
}

// Disable turns authentication off. Only for GLANCE_DISABLE_AUTH.
func (a *Admin) Disable() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled, a.source = false, ""
}

// Enabled reports whether admin authentication is in force.
func (a *Admin) Enabled() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled
}

// FromEnv reports whether the credential is pinned by the environment.
func (a *Admin) FromEnv() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fromEnv
}

// Source reports where the credential came from: env, generated or set.
func (a *Admin) Source() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.source
}

// Username returns the administrator's name, for display.
func (a *Admin) Username() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.username
}

// Check verifies a username/password pair.
//
// The username is compared in constant time and the password through the KDF,
// and both are always evaluated so a wrong username costs the same as a wrong
// password. Returning early on the username would leak which names exist.
func (a *Admin) Check(username, password string) bool {
	a.mu.Lock()
	enabled, wantUser, wantPass := a.enabled, a.username, a.password
	cacheKey := Hash(username + "\x00" + password + "\x00" + wantPass)
	if exp, ok := a.verified[cacheKey]; ok && a.now().Before(exp) {
		a.mu.Unlock()
		return true
	}
	a.mu.Unlock()
	if !enabled {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(Hash(username)), []byte(Hash(wantUser))) == 1
	passOK := VerifyPassword(wantPass, password)
	if !(userOK && passOK) {
		return false
	}
	a.mu.Lock()
	a.verified[cacheKey] = a.now().Add(verifiedTTL)
	// The cache is only ever as large as the number of distinct correct
	// credentials, which is one, plus expired entries; sweep them.
	for k, exp := range a.verified {
		if a.now().After(exp) {
			delete(a.verified, k)
		}
	}
	a.mu.Unlock()
	return true
}

// PasswordHash returns the stored encoding, for session invalidation.
func (a *Admin) PasswordHash() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.password
}

// Login creates a session and returns its raw token.
func (a *Admin) Login(ctx context.Context, username, password string) (string, bool) {
	if !a.Check(username, password) {
		return "", false
	}
	tok := "glance_sess_" + ids.Random(20)
	exp := a.now().Add(SessionTTL)
	a.mu.Lock()
	a.sessions[Hash(tok)] = exp
	pw := a.password
	a.mu.Unlock()
	if a.store != nil {
		// The password encoding is stored with the session, so changing the
		// password invalidates every session that was opened against the old
		// one without needing to enumerate them.
		_ = a.store.Save(ctx, Hash(tok), pw, exp)
	}
	return tok, true
}

// Logout revokes a session token.
func (a *Admin) Logout(ctx context.Context, token string) {
	a.mu.Lock()
	delete(a.sessions, Hash(token))
	a.mu.Unlock()
	if a.store != nil {
		_ = a.store.Delete(ctx, Hash(token))
	}
}

// Valid reports whether a raw session token is live.
func (a *Admin) Valid(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	h := Hash(token)
	a.mu.Lock()
	exp, ok := a.sessions[h]
	current := a.password
	a.mu.Unlock()
	if !ok && a.store != nil {
		pw, storedExp, found, err := a.store.Lookup(ctx, h)
		if err != nil || !found || !Equal(pw, current) {
			return false
		}
		exp, ok = storedExp, true
		a.mu.Lock()
		a.sessions[h] = exp
		a.mu.Unlock()
	}
	if !ok {
		return false
	}
	if a.now().After(exp) {
		a.mu.Lock()
		delete(a.sessions, h)
		a.mu.Unlock()
		return false
	}
	return true
}

// Authorized reports whether r carries a valid session cookie or HTTP Basic
// credentials. Always true when auth is not enabled.
func (a *Admin) Authorized(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	if c, err := r.Cookie(SessionCookie); err == nil && a.Valid(r.Context(), c.Value) {
		return true
	}
	if u, p, ok := r.BasicAuth(); ok && a.Check(u, p) {
		return true
	}
	return false
}

// SetCookie writes the session cookie for token onto w.
func (a *Admin) SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: isHTTPS(r), MaxAge: int(SessionTTL.Seconds()),
	})
}

// ClearCookie removes the session cookie.
func (a *Admin) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: -1})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// Hash returns the hex SHA-256 of s.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Equal compares two hashes in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
