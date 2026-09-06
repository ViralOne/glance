package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ViralOne/glance/server/internal/ratelimit"
	"github.com/ViralOne/glance/server/internal/rollup"
)

// collectFrom posts one event with an explicit peer address, so the tests can
// distinguish "the request came from a LAN" from "the body claims a LAN page".
func collectFrom(t *testing.T, h http.Handler, peer string, body map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/collect", strings.NewReader(string(b)))
	req.RemoteAddr = peer
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", chromeMac)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func countEvents(t *testing.T, s *Server) int {
	t.Helper()
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCollectRejectsSpoofedDevelopmentHost is the regression test for the
// central ingest hole: the page host arrives in the request body, so accepting
// a development-looking host from anywhere let anyone who knew a site id (it is
// public, in the snippet) write arbitrary pages, referrers and visitors into
// someone else's stats.
func TestCollectRejectsSpoofedDevelopmentHost(t *testing.T) {
	s := newServer(t, "", "")
	s.Now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()

	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	// Every one of these claims a host that used to be waved through.
	for _, page := range []string{
		"http://anything.test/fake",
		"http://evil.local/fake",
		"http://x.localhost/fake",
		"http://10.evil.com/fake", // a *name* starting with "10.", not an address
		"http://192.168.attacker.com/fake",
		"http://127.0.0.1/fake",
		"http://localhost/fake",
	} {
		got := collectFrom(t, h, "203.0.113.9:1111", map[string]any{"s": site.ID, "u": page, "tz": "Europe/London"}, nil)
		if got.Code != 202 {
			t.Fatalf("%s: collect must always answer 202, got %d", page, got.Code)
		}
	}
	if n := countEvents(t, s); n != 0 {
		t.Fatalf("a public client claiming a development page host must be dropped; %d events stored", n)
	}

	// The same claim from a private address is a developer testing the snippet.
	if got := collectFrom(t, h, "192.168.1.20:5000", map[string]any{"s": site.ID, "u": "http://localhost:5173/", "tz": "Europe/London"}, nil); got.Code != 202 {
		t.Fatalf("local collect: %d", got.Code)
	}
	if n := countEvents(t, s); n != 1 {
		t.Fatalf("a LAN client testing locally should be accepted; got %d events", n)
	}

	// And the opt-in restores the old behaviour for someone whose collector is
	// not on the internet.
	s.AllowLocalEvents = true
	if got := collectFrom(t, h, "203.0.113.9:1111", map[string]any{"s": site.ID, "u": "http://anything.test/x", "tz": "Europe/London"}, nil); got.Code != 202 {
		t.Fatalf("opt-in collect: %d", got.Code)
	}
	if n := countEvents(t, s); n != 2 {
		t.Fatalf("GLANCE_ALLOW_LOCAL_EVENTS should accept it; got %d events", n)
	}
}

// TestClientIPCountsFromTheRight checks the other half of the same problem:
// with the leftmost X-Forwarded-For entry trusted, a caller could mint a new
// IP per request and multiply the unique-visitor count without limit.
func TestClientIPCountsFromTheRight(t *testing.T) {
	s := newServer(t, "", "")
	s.TrustedProxyHops = 1
	r := httptest.NewRequest("POST", "/api/v1/collect", nil)
	r.RemoteAddr = "10.0.0.5:4321"

	// One proxy: the header holds only what that proxy saw.
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := s.clientIP(r); got != "203.0.113.7" {
		t.Fatalf("one hop: got %q", got)
	}
	// A client that prepends its own value must not be believed.
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.7")
	if got := s.clientIP(r); got != "203.0.113.7" {
		t.Fatalf("forged leftmost entry was trusted: got %q", got)
	}
	// Two proxies: the client is one further left.
	s.TrustedProxyHops = 2
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 172.16.0.1")
	if got := s.clientIP(r); got != "203.0.113.7" {
		t.Fatalf("two hops: got %q", got)
	}
	// Fewer entries than trusted hops: fall back to the leftmost rather than
	// indexing out of range.
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := s.clientIP(r); got != "203.0.113.7" {
		t.Fatalf("short chain: got %q", got)
	}
	// No trust at all means the header is ignored entirely.
	s.TrustedProxyHops = 0
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := s.clientIP(r); got != "10.0.0.5" {
		t.Fatalf("untrusted proxy: got %q", got)
	}
}

func TestCollectRateLimited(t *testing.T) {
	s := newServer(t, "", "")
	s.CollectLimiter = ratelimit.New(0.001, 3) // three, then effectively none
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	for i := 0; i < 10; i++ {
		collectFrom(t, h, "203.0.113.9:1111", map[string]any{"s": site.ID, "u": "https://example.com/", "tz": "Europe/London"}, nil)
	}
	if n := countEvents(t, s); n != 3 {
		t.Fatalf("want 3 events past the limiter, got %d", n)
	}
	// A different address has its own budget.
	collectFrom(t, h, "198.51.100.4:2222", map[string]any{"s": site.ID, "u": "https://example.com/", "tz": "Europe/London"}, nil)
	if n := countEvents(t, s); n != 4 {
		t.Fatalf("a second client should not be limited by the first: %d", n)
	}
}

func TestCollectHonoursDNTAndGPC(t *testing.T) {
	s := newServer(t, "", "")
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	body := map[string]any{"s": site.ID, "u": "https://example.com/", "tz": "Europe/London"}
	for _, hdr := range []map[string]string{{"DNT": "1"}, {"Sec-GPC": "1"}} {
		if got := collectFrom(t, h, "203.0.113.9:1111", body, hdr); got.Code != 202 {
			t.Fatalf("opted-out collect must still answer 202, got %d", got.Code)
		}
	}
	if n := countEvents(t, s); n != 0 {
		t.Fatalf("DNT and GPC must suppress recording; %d events stored", n)
	}
	// DNT: 0 is an explicit "yes you may".
	if got := collectFrom(t, h, "203.0.113.9:1111", body, map[string]string{"DNT": "0"}); got.Code != 202 {
		t.Fatal("DNT: 0 should be accepted")
	}
	if n := countEvents(t, s); n != 1 {
		t.Fatalf("DNT: 0 must not suppress recording; got %d", n)
	}
}

func TestCollectStoresEventPropertiesAndValue(t *testing.T) {
	s := newServer(t, "", "")
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return fixed }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	collectFrom(t, h, "203.0.113.9:1111", map[string]any{
		"s": site.ID, "n": "signup", "u": "https://example.com/pricing", "tz": "Europe/London",
		"x": map[string]any{"plan": "pro", "seats": 3, "trial": false, "nested": map[string]any{"no": 1}},
		"v": 1900,
	}, nil)
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	var props string
	var value int
	if err := s.DB.QueryRow(`SELECT props, value FROM events WHERE kind = 'event'`).Scan(&props, &value); err != nil {
		t.Fatal(err)
	}
	if value != 1900 {
		t.Fatalf("value: %d", value)
	}
	// Scalars are kept as strings in a stable order; the nested object is
	// dropped rather than flattened, because a breakdown can only group by a
	// scalar.
	want := `{"plan":"pro","seats":"3","trial":"false"}`
	if props != want {
		t.Fatalf("props: got %s want %s", props, want)
	}

	if err := rollup.Run(t.Context(), s.DB, s.Log, fixed); err != nil {
		t.Fatal(err)
	}
	var propKey string
	var propValue int
	if err := s.DB.QueryRow(`SELECT key, value FROM daily_stats WHERE dim = 'prop'`).Scan(&propKey, &propValue); err != nil {
		t.Fatalf("property breakdown missing: %v", err)
	}
	if propKey != want || propValue != 1900 {
		t.Fatalf("prop rollup: %s %d", propKey, propValue)
	}
}

func TestCollectRecordsCrawlersSeparately(t *testing.T) {
	s := newServer(t, "", "")
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return fixed }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	body := map[string]any{"s": site.ID, "u": "https://example.com/docs", "tz": "Europe/London"}
	for _, ua := range []string{
		"Mozilla/5.0 (compatible; GPTBot/1.1; +https://openai.com/gptbot)",
		"Mozilla/5.0 (compatible; ClaudeBot/1.0; +claudebot@anthropic.com)",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
	} {
		collectFrom(t, h, "203.0.113.9:1111", body, map[string]string{"User-Agent": ua})
	}
	collectFrom(t, h, "198.51.100.4:2222", body, nil) // one human

	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := rollup.Run(t.Context(), s.DB, s.Log, fixed); err != nil {
		t.Fatal(err)
	}

	// Crawlers are never visitors: one human, one visitor, one pageview.
	var pageviews, visitors int
	if err := s.DB.QueryRow(`SELECT pageviews, visitors FROM daily_stats WHERE dim = 'total'`).Scan(&pageviews, &visitors); err != nil {
		t.Fatal(err)
	}
	if pageviews != 1 || visitors != 1 {
		t.Fatalf("crawler hits leaked into human totals: %d pageviews, %d visitors", pageviews, visitors)
	}

	bots := map[string]int{}
	rows, err := s.DB.Query(`SELECT key, pageviews FROM daily_stats WHERE dim = 'bot'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var n int
		_ = rows.Scan(&k, &n)
		bots[k] = n
	}
	rows.Close()
	for _, want := range []string{"GPTBot", "ClaudeBot", "Googlebot"} {
		if bots[want] != 1 {
			t.Fatalf("crawler %q not counted: %v", want, bots)
		}
	}

	// Only the model-feeding crawlers are in the AI cut.
	ai := map[string]bool{}
	rows, err = s.DB.Query(`SELECT key FROM daily_stats WHERE dim = 'aibot'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		ai[k] = true
	}
	rows.Close()
	if !ai["GPTBot"] || !ai["ClaudeBot"] {
		t.Fatalf("AI crawlers missing: %v", ai)
	}
	if ai["Googlebot"] {
		t.Fatalf("Googlebot is a search crawler, not an AI one: %v", ai)
	}
}

func TestCollectRespectsSiteExclusions(t *testing.T) {
	s := newServer(t, "", "")
	s.Now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	rr = do(t, h, "PATCH", "/api/v1/sites/"+site.ID, map[string]any{
		"exclude_paths": []string{"/admin/*", "/health"},
		"exclude_ips":   []string{"203.0.113.9", "198.51.100.0/24"},
		"domains":       []string{"example.org", "app.example.net"},
	}, nil)
	if rr.Code != 200 {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body)
	}

	page := func(peer, url string) {
		collectFrom(t, h, peer, map[string]any{"s": site.ID, "u": url, "tz": "Europe/London"}, nil)
	}
	page("192.0.2.1:1", "https://example.com/admin/users") // excluded path (prefix)
	page("192.0.2.1:1", "https://example.com/health")      // excluded path (exact)
	page("203.0.113.9:1", "https://example.com/")          // excluded IP
	page("198.51.100.77:1", "https://example.com/")        // excluded CIDR
	if n := countEvents(t, s); n != 0 {
		t.Fatalf("exclusions were not applied; %d events stored", n)
	}

	page("192.0.2.1:1", "https://example.com/pricing")      // fine
	page("192.0.2.1:1", "https://example.org/cross-domain") // extra domain
	page("192.0.2.1:1", "https://app.example.net/deep")     // extra domain
	page("192.0.2.1:1", "https://blog.example.com/post")    // subdomain of its own domain
	if n := countEvents(t, s); n != 4 {
		t.Fatalf("want 4 accepted events, got %d", n)
	}
	page("192.0.2.1:1", "https://someone-else.com/") // still refused
	if n := countEvents(t, s); n != 4 {
		t.Fatalf("an unrelated domain must be refused, got %d", n)
	}
}

func TestCollectRecordsVitals(t *testing.T) {
	s := newServer(t, "", "")
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return fixed }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	// Ten page loads with a rising LCP, so the p75 is predictable.
	for i := 0; i < 10; i++ {
		collectFrom(t, h, "192.0.2.1:1", map[string]any{
			"s": site.ID, "u": "https://example.com/", "tz": "Europe/London", "w": 1440,
			"cwv": map[string]any{"lcp": 1000 + i*200, "cls": 50, "ttfb": 300, "inp": 90},
		}, nil)
	}
	// An impossible reading is discarded rather than dragging the percentile.
	collectFrom(t, h, "192.0.2.1:1", map[string]any{
		"s": site.ID, "u": "https://example.com/", "tz": "Europe/London", "w": 1440,
		"cwv": map[string]any{"lcp": 9_000_000},
	}, nil)

	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM vitals WHERE metric = 'LCP'`).Scan(&n)
	if n != 10 {
		t.Fatalf("want 10 LCP samples (the absurd one dropped), got %d", n)
	}
	if err := rollup.Vitals(t.Context(), s.DB, site.ID, fixed); err != nil {
		t.Fatal(err)
	}

	rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/vitals?range=7d", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("vitals: %d %s", rr.Code, rr.Body)
	}
	var out struct {
		Vitals struct {
			Overall []struct {
				Metric  string  `json:"metric"`
				P75     float64 `json:"p75"`
				Samples int     `json:"samples"`
				Rating  string  `json:"rating"`
			} `json:"overall"`
			Devices []struct {
				Key string `json:"key"`
			} `json:"devices"`
		} `json:"vitals"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byMetric := map[string]float64{}
	ratings := map[string]string{}
	for _, r := range out.Vitals.Overall {
		byMetric[r.Metric] = r.P75
		ratings[r.Metric] = r.Rating
		if r.Samples != 10 {
			t.Fatalf("%s samples: %d", r.Metric, r.Samples)
		}
	}
	// Values 1000..2800 sorted, nearest-rank p75 lands on index 7 = 2400.
	if byMetric["LCP"] != 2400 {
		t.Fatalf("LCP p75: %v (all: %v)", byMetric["LCP"], byMetric)
	}
	if ratings["LCP"] != "good" {
		t.Fatalf("2400ms LCP is inside Google's 2500ms good bound: %q", ratings["LCP"])
	}
	if ratings["CLS"] != "good" {
		t.Fatalf("a CLS of 0.05 is good: %q", ratings["CLS"])
	}
	if len(out.Vitals.Devices) == 0 || out.Vitals.Devices[0].Key != "Desktop" {
		t.Fatalf("device cut missing: %+v", out.Vitals.Devices)
	}
}
