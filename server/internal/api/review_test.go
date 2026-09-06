package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ViralOne/glance/server/internal/alerts"
	"github.com/ViralOne/glance/server/internal/ratelimit"
)

// TestTodayIsInsideEveryRange covers an off-by-a-day that a code review found
// in two places: the daily tables are keyed by day, and the 24h and 48h windows
// end at the next hour boundary rather than at midnight, so reading a day key
// off the exclusive end silently dropped today.
//
// The symptoms were bad in a specific way: a goal reported zero conversions
// while the same goal on 7d reported them, and a note vanished from the very
// range it was added on.
func TestTodayIsInsideEveryRange(t *testing.T) {
	s := newServer(t, "", "")
	now := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "a.example"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	collectFrom(t, h, "192.0.2.1:1", map[string]any{"s": site.ID, "u": "https://a.example/", "tz": "Europe/London"}, nil)
	collectFrom(t, h, "192.0.2.1:1", map[string]any{"s": site.ID, "n": "signup", "u": "https://a.example/", "tz": "Europe/London"}, nil)
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, h, "POST", "/api/v1/sites/"+site.ID+"/goals", map[string]any{"kind": "event", "target": "signup"}, nil); rr.Code != 201 {
		t.Fatalf("goal: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "POST", "/api/v1/sites/"+site.ID+"/notes", map[string]any{"text": "today"}, nil); rr.Code != 201 {
		t.Fatalf("note: %d %s", rr.Code, rr.Body)
	}

	for _, rng := range []string{"24h", "48h", "7d", "30d"} {
		rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/goals?range="+rng, nil, nil)
		var goalsResp struct {
			Goals []struct {
				Conversions int     `json:"conversions"`
				Rate        float64 `json:"rate"`
			} `json:"goals"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &goalsResp); err != nil {
			t.Fatal(err)
		}
		if len(goalsResp.Goals) != 1 || goalsResp.Goals[0].Conversions != 1 {
			t.Errorf("%s: today's conversion is missing: %s", rng, rr.Body)
		}

		rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/notes?range="+rng, nil, nil)
		var notesResp struct {
			Notes []struct{ Day, Text string } `json:"notes"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &notesResp); err != nil {
			t.Fatal(err)
		}
		if len(notesResp.Notes) != 1 {
			t.Errorf("%s: today's note is missing: %s", rng, rr.Body)
		}
	}
}

// TestCreateSiteKeepsExclusions covers input that was parsed and then dropped:
// creating a site with exclude_ips answered 201 with an empty list and went on
// measuring the operator's own visits.
func TestCreateSiteKeepsExclusions(t *testing.T) {
	s := newServer(t, "", "")
	s.Now = func() time.Time { return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{
		"domain":        "a.example",
		"domains":       []string{"b.example"},
		"exclude_paths": []string{"/admin/*"},
		"exclude_ips":   []string{"203.0.113.9"},
	}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)
	if len(site.Domains) != 1 || site.Domains[0] != "b.example" {
		t.Errorf("extra domain dropped on create: %+v", site.Domains)
	}
	if len(site.ExcludePaths) != 1 || len(site.ExcludeIPs) != 1 {
		t.Errorf("exclusions dropped on create: %+v %+v", site.ExcludePaths, site.ExcludeIPs)
	}

	// And they are actually applied, not merely echoed back.
	collectFrom(t, h, "203.0.113.9:1", map[string]any{"s": site.ID, "u": "https://a.example/", "tz": "Europe/London"}, nil)
	collectFrom(t, h, "192.0.2.1:1", map[string]any{"s": site.ID, "u": "https://a.example/admin/x", "tz": "Europe/London"}, nil)
	collectFrom(t, h, "192.0.2.1:1", map[string]any{"s": site.ID, "u": "https://b.example/ok", "tz": "Europe/London"}, nil)
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 1 {
		t.Fatalf("want only the b.example hit recorded, got %d events", n)
	}
	// Invalid values are still refused rather than silently ignored.
	if rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "c.example", "exclude_ips": []string{"not-an-ip"}}, nil); rr.Code != 422 {
		t.Errorf("a bad exclude_ip on create should be refused: %d %s", rr.Code, rr.Body)
	}
}

// TestDigestReachesEverySite covers a rule that silenced itself: an all-sites
// digest delivered for the first site, stamped its cooldown, and left every
// other site without a digest from then on.
func TestDigestReachesEverySite(t *testing.T) {
	s := newServer(t, "", "")
	// A Monday at 08:00 UTC, which is when a default digest is due.
	monday := time.Date(2026, 9, 7, 8, 15, 0, 0, time.UTC)
	s.Now = func() time.Time { return monday }
	s.AlertEngine.Now = s.Now
	h := s.Handler()

	var names []string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		var payload struct {
			Alert struct{ Site string } `json:"alert"`
		}
		_ = json.Unmarshal(body, &payload)
		names = append(names, payload.Alert.Site)
		w.WriteHeader(200)
	}))
	defer recv.Close()

	for _, d := range []string{"one.example", "two.example", "three.example"} {
		if rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"name": d, "domain": d}, nil); rr.Code != 201 {
			t.Fatalf("site %s: %d", d, rr.Code)
		}
	}
	// site_id omitted means every site.
	if rr := do(t, h, "POST", "/api/v1/alerts", map[string]any{
		"kind": "digest", "channel": "webhook", "destination": recv.URL,
	}, nil); rr.Code != 201 {
		t.Fatalf("alert: %d %s", rr.Code, rr.Body)
	}

	s.AlertEngine.Run(t.Context())
	if len(names) != 3 {
		t.Fatalf("a digest covering every site should send three, sent %d (%v)", len(names), names)
	}
	// Running again in the same hour must not duplicate them.
	before := len(names)
	s.AlertEngine.Run(t.Context())
	if len(names) != before {
		t.Fatalf("the digest repeated within the same hour: %v", names)
	}
}

// TestDigestDefaultsToEightHundred covers a guard that could not fire: the hour
// came from the threshold, and the "out of range, use 8" fallback could never
// catch a zero, so an unset digest went out at midnight.
func TestDigestDefaultsToEightHundred(t *testing.T) {
	s := newServer(t, "", "")
	s.Now = func() time.Time { return time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC) }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/alerts", map[string]any{
		"kind": "digest", "channel": "webhook", "destination": "https://example.com/hook",
	}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var a alerts.Alert
	if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Threshold != 8 {
		t.Errorf("a digest with no hour should default to 08:00 UTC, got %v", a.Threshold)
	}
	// Midnight remains a real choice, distinguishable from "unset".
	rr = do(t, h, "POST", "/api/v1/alerts", map[string]any{
		"kind": "digest", "channel": "webhook", "destination": "https://example.com/hook2", "threshold": 0,
	}, nil)
	if rr.Code != 201 {
		t.Fatalf("midnight digest: %d %s", rr.Code, rr.Body)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &a)
	if a.Threshold != 0 {
		t.Errorf("an explicit midnight should stay midnight, got %v", a.Threshold)
	}
	// An hour outside the day is refused.
	if rr := do(t, h, "POST", "/api/v1/alerts", map[string]any{
		"kind": "digest", "channel": "webhook", "destination": "https://example.com/h3", "threshold": 30,
	}, nil); rr.Code != 422 {
		t.Errorf("hour 30 should be refused: %d", rr.Code)
	}
}

// TestFunnelCountsReturnVisits covers the ordering query: only the earliest
// occurrence of each step was considered, so a visitor who hit a step before
// and again after the previous step was dropped.
func TestFunnelCountsReturnVisits(t *testing.T) {
	s := newServer(t, "", "")
	now := time.Date(2026, 9, 10, 15, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "a.example"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	insert := func(minute int, visitor, path string) {
		ts := now.Truncate(24 * time.Hour).Add(9*time.Hour + time.Duration(minute)*time.Minute)
		if _, err := s.DB.Exec(`INSERT INTO events (site_id, ts, kind, name, path, ref_host, country, device, browser, os, region, city, utm_source, utm_campaign, utm_medium, visitor, props, value)
			VALUES (?,?, 'pageview','',?,'','GB','Desktop','Chrome','macOS','','','','','', ?, '', 0)`,
			site.ID, ts.UTC().Format("2006-01-02T15:04:05.000000000Z"), path, visitor); err != nil {
			t.Fatal(err)
		}
	}
	// This visitor sees /pricing first, then /, then comes back to /pricing.
	// They did reach step two after step one, on the return visit.
	insert(0, "wanderer", "/pricing")
	insert(5, "wanderer", "/")
	insert(10, "wanderer", "/pricing")
	// A straightforward one, for contrast.
	insert(0, "direct", "/")
	insert(5, "direct", "/pricing")

	if rr := do(t, h, "POST", "/api/v1/sites/"+site.ID+"/funnels", map[string]any{
		"steps": []map[string]any{{"target": "/"}, {"target": "/pricing"}},
	}, nil); rr.Code != 201 {
		t.Fatalf("funnel: %d %s", rr.Code, rr.Body)
	}
	rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/funnels?range=7d", nil, nil)
	var out struct {
		Funnels []struct {
			Steps []struct{ Visitors int } `json:"steps"`
		} `json:"funnels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	steps := out.Funnels[0].Steps
	if steps[0].Visitors != 2 {
		t.Fatalf("step 1: %d", steps[0].Visitors)
	}
	if steps[1].Visitors != 2 {
		t.Fatalf("step 2 should include the visitor who returned to /pricing after /, got %d", steps[1].Visitors)
	}
}

// TestZeroBurstDisablesTheLimiter covers a configuration footgun: a burst of
// zero with a positive rate capped every bucket at zero and rejected every
// event, while reading in configuration as "no limit".
func TestZeroBurstDisablesTheLimiter(t *testing.T) {
	l := ratelimit.New(4, 0)
	for i := 0; i < 50; i++ {
		if !l.Allow("a") {
			t.Fatalf("a zero burst must disable the limiter, denied at %d", i)
		}
	}
}
