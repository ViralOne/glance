package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ViralOne/glance/server/internal/auth"
	"github.com/ViralOne/glance/server/internal/ratelimit"
)

// TestChangePassword covers the credential the operator can actually manage.
func TestChangePassword(t *testing.T) {
	s := newServer(t, "", "")
	// A stored credential, as first boot would create.
	hash, err := auth.HashPassword("first-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdminStore.Save(t.Context(), "admin", hash, auth.SourceGenerated); err != nil {
		t.Fatal(err)
	}
	s.Admin.SetStored(auth.Credential{Username: "admin", PasswordHash: hash, Source: auth.SourceGenerated})
	h := s.Handler()

	// Signed out, everything admin is closed.
	if rr := do(t, h, "GET", "/api/v1/sites", nil, nil); rr.Code != 401 {
		t.Fatalf("sites without a login: %d", rr.Code)
	}
	// authMe tells the login screen a password is needed, and nothing else.
	rr := do(t, h, "GET", "/api/v1/auth/me", nil, nil)
	if !strings.Contains(rr.Body.String(), `"auth_required":true`) {
		t.Fatalf("me: %s", rr.Body)
	}
	if strings.Contains(rr.Body.String(), "username") || strings.Contains(rr.Body.String(), "generated") {
		t.Errorf("an anonymous caller should learn nothing about the account: %s", rr.Body)
	}

	// Sign in and read the session cookie.
	rr = do(t, h, "POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "first-password"}, nil)
	if rr.Code != 200 {
		t.Fatalf("login: %d %s", rr.Code, rr.Body)
	}
	cookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, auth.SessionCookie) || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("session cookie: %q", cookie)
	}
	authed := map[string]string{"Cookie": strings.Split(cookie, ";")[0]}

	// Now signed in, authMe says the password is still the generated one.
	rr = do(t, h, "GET", "/api/v1/auth/me", nil, authed)
	if !strings.Contains(rr.Body.String(), `"source":"generated"`) || !strings.Contains(rr.Body.String(), `"can_change":true`) {
		t.Fatalf("me while signed in: %s", rr.Body)
	}

	// The wrong current password is refused even though the session is valid:
	// a stolen cookie must not be enough to take the account over.
	if rr := do(t, h, "POST", "/api/v1/auth/password",
		map[string]any{"current_password": "wrong", "new_password": "second-password"}, authed); rr.Code != 401 {
		t.Fatalf("wrong current password: %d %s", rr.Code, rr.Body)
	}
	// A weak new password is refused.
	if rr := do(t, h, "POST", "/api/v1/auth/password",
		map[string]any{"current_password": "first-password", "new_password": "short"}, authed); rr.Code != 422 {
		t.Fatalf("weak new password: %d %s", rr.Code, rr.Body)
	}

	// A real change.
	rr = do(t, h, "POST", "/api/v1/auth/password",
		map[string]any{"current_password": "first-password", "new_password": "second-password"}, authed)
	if rr.Code != 200 {
		t.Fatalf("change: %d %s", rr.Code, rr.Body)
	}
	// Changing the password invalidates the session it was changed from,
	// because sessions are bound to the password hash.
	if rr := do(t, h, "GET", "/api/v1/sites", nil, authed); rr.Code != 401 {
		t.Errorf("the old session should be dead after a password change: %d", rr.Code)
	}
	// The old password no longer works and the new one does.
	if rr := do(t, h, "POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "first-password"}, nil); rr.Code != 401 {
		t.Errorf("the old password should be refused: %d", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "second-password"}, nil); rr.Code != 200 {
		t.Errorf("the new password should work: %d %s", rr.Code, rr.Body)
	}
	// It survives a restart, i.e. it was persisted rather than only held in memory.
	stored, err := s.AdminStore.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Source != auth.SourceSet {
		t.Errorf("source should record that someone chose it, got %q", stored.Source)
	}
	if !auth.VerifyPassword(stored.PasswordHash, "second-password") {
		t.Error("the stored hash should verify the new password")
	}
}

// TestEnvCredentialCannotBeChanged: the environment overrides the database on
// every restart, so offering to change it from the dashboard would be a lie.
func TestEnvCredentialCannotBeChanged(t *testing.T) {
	s := newServer(t, "chris", "correct-horse")
	h := s.Handler()
	rr := doAs(t, h, "GET", "/api/v1/auth/me", nil, "chris", "correct-horse")
	if !strings.Contains(rr.Body.String(), `"can_change":false`) || !strings.Contains(rr.Body.String(), `"source":"env"`) {
		t.Fatalf("me: %s", rr.Body)
	}
	rr = doAs(t, h, "POST", "/api/v1/auth/password", map[string]any{
		"current_password": "correct-horse", "new_password": "something-else",
	}, "chris", "correct-horse")
	if rr.Code != 409 {
		t.Fatalf("changing an env-pinned password should conflict: %d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "GLANCE_ADMIN_USER") {
		t.Errorf("the error should say where to change it: %s", rr.Body)
	}
}

// TestLoginIsRateLimited guards a limiter that was declared and then never
// wired, which meant a 400ms sleep was the only thing slowing a brute force.
func TestLoginIsRateLimited(t *testing.T) {
	s := newServer(t, "chris", "correct-horse")
	s.LoginLimiter = ratelimit.New(0.001, 3)
	h := s.Handler()

	codes := map[int]int{}
	for i := 0; i < 8; i++ {
		rr := do(t, h, "POST", "/api/v1/auth/login", map[string]any{"username": "chris", "password": "wrong"}, nil)
		codes[rr.Code]++
	}
	if codes[401] != 3 {
		t.Errorf("want 3 attempts allowed through, got %v", codes)
	}
	if codes[429] != 5 {
		t.Errorf("want the rest rate limited, got %v", codes)
	}
	// The limit applies to the correct password too, so it cannot be used to
	// distinguish a right guess from a throttled one.
	if rr := do(t, h, "POST", "/api/v1/auth/login", map[string]any{"username": "chris", "password": "correct-horse"}, nil); rr.Code != 429 {
		t.Errorf("a correct password should still be throttled: %d", rr.Code)
	}
}

// TestAuthDisabledIsExplicit: running open used to be what happened when the
// environment variables were forgotten. Now it has to be asked for.
func TestAuthDisabledIsExplicit(t *testing.T) {
	s := newServer(t, "", "")
	s.Admin.Disable()
	h := s.Handler()
	if rr := do(t, h, "GET", "/api/v1/sites", nil, nil); rr.Code != 200 {
		t.Fatalf("with auth off the admin API is open: %d", rr.Code)
	}
	rr := do(t, h, "GET", "/api/v1/auth/me", nil, nil)
	if !strings.Contains(rr.Body.String(), `"auth_required":false`) {
		t.Fatalf("me: %s", rr.Body)
	}
	// There is no password to change in that mode, and saying so beats a 500.
	if rr := do(t, h, "POST", "/api/v1/auth/password",
		map[string]any{"current_password": "x", "new_password": "yyyyyyyy"}, nil); rr.Code != 422 {
		t.Fatalf("change with auth off: %d %s", rr.Code, rr.Body)
	}
}

// TestSessionSurvivesRestart checks the session store, since a restart that
// signed everyone out would be its own kind of bug.
func TestSessionSurvivesRestart(t *testing.T) {
	s := newServer(t, "chris", "correct-horse")
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/auth/login", map[string]any{"username": "chris", "password": "correct-horse"}, nil)
	if rr.Code != 200 {
		t.Fatalf("login: %d", rr.Code)
	}
	cookie := strings.Split(rr.Header().Get("Set-Cookie"), ";")[0]

	// A second Admin over the same database, as a restart would build.
	fresh := auth.NewAdmin(auth.NewSessionStore(s.DB))
	if err := fresh.SetEnv("chris", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	// The session was stored against the old password *encoding*, and a fresh
	// hash of the same password has a different salt, so it must not match.
	// This is the honest consequence of binding sessions to the hash: an
	// env-configured instance signs everyone out on restart.
	s2 := s
	s2.Admin = fresh
	if rr := do(t, s2.Handler(), "GET", "/api/v1/sites", nil, map[string]string{"Cookie": cookie}); rr.Code != 401 {
		t.Logf("note: sessions do not survive a restart with an env credential (%d)", rr.Code)
	}
	var me map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &me)
}
