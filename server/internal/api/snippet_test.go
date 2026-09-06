package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSnippetShape guards the properties of the served script that are easy to
// lose in a rebuild. It is checked against the minified bytes that actually go
// out, not against the readable source, because those are what the browser
// runs.
func TestSnippetShape(t *testing.T) {
	s := newServer(t, "", "")
	h := s.Handler()
	rr := do(t, h, "GET", "/glance.js", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	body := rr.Body.String()

	// Small enough to stay out of the way. The budget is deliberate: if a
	// change pushes past it, that is a decision to make rather than a diff to
	// wave through.
	const budget = 4096
	if len(body) > budget {
		t.Errorf("snippet is %d bytes, over the %d-byte budget", len(body), budget)
	}

	for _, want := range []struct{ needle, why string }{
		{"sendBeacon", "events must survive the page being closed"},
		{"doNotTrack", "Do Not Track is honoured in the browser, not only server-side"},
		{"globalPrivacyControl", "Global Privacy Control is honoured in the browser"},
		{"webdriver", "automated browsers are not visitors"},
		{"pushState", "single-page apps must report navigations"},
		{"popstate", "back and forward must report navigations"},
		{"PerformanceObserver", "Core Web Vitals are sampled from real page loads"},
		{"largest-contentful-paint", "LCP"},
		{"layout-shift", "CLS"},
		{"data-attribution", "opt-in first-touch attribution"},
		{"glance_attr", "the attribution key the site reads"},
	} {
		if !strings.Contains(body, want.needle) {
			t.Errorf("snippet is missing %q: %s", want.needle, want.why)
		}
	}

	// The snippet must not set a cookie or read one, which is the whole
	// privacy claim.
	for _, forbidden := range []string{"document.cookie", "d.cookie", "e.cookie"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("snippet touches cookies via %q", forbidden)
		}
	}

	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the snippet is loaded cross-origin by definition")
	}
	if !strings.Contains(rr.Header().Get("Cache-Control"), "stale-while-revalidate") {
		t.Errorf("cache: %q", rr.Header().Get("Cache-Control"))
	}
	// A rebuild changes the ETag, so a browser picks up a new snippet.
	if rr.Header().Get("ETag") == "" {
		t.Error("no ETag")
	}
	if rr2 := do(t, h, "GET", "/glance.js", nil, map[string]string{"If-None-Match": rr.Header().Get("ETag")}); rr2.Code != 304 {
		t.Errorf("conditional request: %d", rr2.Code)
	}
}

// TestFetchIsThePrimaryTransport locks in a decision that looks backwards
// until you know why.
//
// sendBeacon is the obvious transport for analytics and it is deliberately the
// fallback here. EasyPrivacy carries a blanket rule, "*$ping,third-party",
// which blocks every third-party sendBeacon whatever the URL is — renaming
// paths does not help, and neither does a fallback, because a blocked
// sendBeacon still returns true so the caller cannot tell. The equivalent
// fetch is matched by nothing. Checked against the real EasyPrivacy and
// EasyList rules; if someone "tidies" this back to beacon-first, every visitor
// running uBlock on a site whose collector is on another domain disappears.
func TestFetchIsThePrimaryTransport(t *testing.T) {
	s := newServer(t, "", "")
	rr := do(t, s.Handler(), "GET", "/glance.js", nil, nil)
	body := rr.Body.String()
	fetchAt := strings.Index(body, "keepalive")
	beaconAt := strings.Index(body, "sendBeacon")
	if fetchAt < 0 || beaconAt < 0 {
		t.Fatalf("expected both transports in the snippet (fetch %d, beacon %d)", fetchAt, beaconAt)
	}
	if fetchAt > beaconAt {
		t.Error("sendBeacon is being tried before fetch; third-party beacons are blocked by EasyPrivacy")
	}
}

// TestVitalsOnlyFlushIsNotAPageview covers the reserved event name the snippet
// uses when a page is hidden before its vitals could ride along on a pageview.
// Counting that as a hit would inflate both pageviews and events.
func TestVitalsOnlyFlushIsNotAPageview(t *testing.T) {
	s := newServer(t, "", "")
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return fixed }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	collectFrom(t, h, "192.0.2.1:1", map[string]any{
		"s": site.ID, "n": "$vitals", "u": "https://example.com/", "tz": "Europe/London", "w": 1440,
		"cwv": map[string]any{"lcp": 1500, "cls": 20},
	}, nil)
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}

	var events int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events)
	if events != 0 {
		t.Fatalf("a vitals-only flush must not record an event, got %d", events)
	}
	var vitals int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM vitals`).Scan(&vitals)
	if vitals != 2 {
		t.Fatalf("want 2 vitals rows, got %d", vitals)
	}
}

// TestCustomPaths covers the ad-blocker workaround: the script and the
// collector are served at operator-chosen paths as well as the defaults, and
// the served script posts to the configured path without the page changing.
func TestCustomPaths(t *testing.T) {
	s := newServer(t, "", "")
	s.SnippetPath = "/js/app.js"
	s.CollectPath = "/i"
	s.Now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()

	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	// The alias serves the script.
	rr = do(t, h, "GET", "/js/app.js", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("alias script: %d", rr.Code)
	}
	body := rr.Body.String()
	// The minifier may quote with backticks, so match the path itself rather
	// than a quoting style.
	if !strings.Contains(body, "/i") {
		t.Errorf("the served script should post to the configured path: %s", body[:200])
	}
	if strings.Contains(body, "/api/v1/collect") {
		t.Error("the default collect path should have been rewritten out")
	}
	// The default path still works, so an existing page keeps measuring.
	if rr := do(t, h, "GET", "/glance.js", nil, nil); rr.Code != 200 {
		t.Fatalf("default script: %d", rr.Code)
	}

	// Both collect paths accept events.
	for _, path := range []string{"/api/v1/collect", "/i"} {
		b, _ := json.Marshal(map[string]any{"s": site.ID, "u": "https://example.com/x", "tz": "Europe/London"})
		req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
		req.RemoteAddr = "192.0.2.1:1"
		req.Header.Set("User-Agent", chromeMac)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 202 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 2 {
		t.Fatalf("want 2 events, got %d", n)
	}
}

// TestAliasPathRefusesCollisions guards the router: registering an alias that
// equals a route Glance already serves panics at start-up.
func TestAliasPathRefusesCollisions(t *testing.T) {
	if got := aliasPath("/glance.js", "/glance.js"); got != "" {
		t.Errorf("an alias equal to the default must be ignored, got %q", got)
	}
	if got := aliasPath("", "/glance.js"); got != "" {
		t.Errorf("empty: %q", got)
	}
	if got := aliasPath("relative.js", "/glance.js"); got != "" {
		t.Errorf("a relative path is not a route, got %q", got)
	}
	if got := aliasPath("/js/app.js", "/glance.js"); got != "/js/app.js" {
		t.Errorf("got %q", got)
	}
}
