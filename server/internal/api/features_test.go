package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ViralOne/glance/server/internal/rollup"
)

// featureFixture seeds a site with a week of traffic: one visitor per day who
// views /pricing and signs up, plus two who only look.
func featureFixture(t *testing.T) (*Server, http.Handler, string, time.Time) {
	t.Helper()
	s := newServer(t, "", "")
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return fixed }
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	// Events are written directly so their timestamps can be controlled: the
	// collect endpoint always stamps "now", and a funnel needs ordering.
	insert := func(day int, hour int, minute int, visitor, kind, name, path string) {
		t.Helper()
		ts := fixed.AddDate(0, 0, -day).Truncate(24 * time.Hour).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
		_, err := s.DB.Exec(`INSERT INTO events (site_id, ts, kind, name, path, ref_host, country, device, browser, os, region, city, utm_source, utm_campaign, utm_medium, visitor, props, value)
			VALUES (?,?,?,?,?,'','GB','Desktop','Chrome','macOS','','', '', '', '', ?, '', 0)`,
			site.ID, ts.UTC().Format("2006-01-02T15:04:05.000000000Z"), kind, name, path, visitor)
		if err != nil {
			t.Fatal(err)
		}
	}
	for day := 0; day < 3; day++ {
		buyer := fmt.Sprintf("buyer%d", day)
		insert(day, 9, 0, buyer, "pageview", "", "/")
		insert(day, 9, 10, buyer, "pageview", "", "/pricing")
		insert(day, 9, 20, buyer, "event", "signup", "/pricing")
		// A browser who reaches pricing but never signs up.
		looker := fmt.Sprintf("looker%d", day)
		insert(day, 10, 0, looker, "pageview", "", "/")
		insert(day, 10, 5, looker, "pageview", "", "/pricing")
		// And someone who only sees the home page.
		insert(day, 11, 0, fmt.Sprintf("bounce%d", day), "pageview", "", "/")
	}
	// Someone who signed up *before* seeing pricing: present but out of order,
	// so a funnel must not count them.
	insert(0, 14, 0, "backwards", "event", "signup", "/pricing")
	insert(0, 14, 30, "backwards", "pageview", "", "/pricing")

	for day := 0; day < 4; day++ {
		if err := rollup.Day(t.Context(), s.DB, site.ID, fixed.AddDate(0, 0, -day)); err != nil {
			t.Fatal(err)
		}
	}
	return s, h, site.ID, fixed
}

func TestGoals(t *testing.T) {
	s, h, siteID, _ := featureFixture(t)
	base := "/api/v1/sites/" + siteID + "/goals"

	// An event goal, with a value per conversion.
	rr := do(t, h, "POST", base, map[string]any{"name": "Signed up", "kind": "event", "target": "signup", "value": 2500}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var goal struct{ ID, Kind string }
	_ = json.Unmarshal(rr.Body.Bytes(), &goal)
	if goal.Kind != "event" {
		t.Fatalf("kind: %q", goal.Kind)
	}
	// A path goal, inferred from the target's leading slash.
	if rr := do(t, h, "POST", base, map[string]any{"name": "Saw pricing", "target": "/pricing"}, nil); rr.Code != 201 {
		t.Fatalf("create path goal: %d %s", rr.Code, rr.Body)
	}
	// A prefix goal.
	if rr := do(t, h, "POST", base, map[string]any{"name": "Any page", "target": "/*"}, nil); rr.Code != 201 {
		t.Fatalf("create prefix goal: %d %s", rr.Code, rr.Body)
	}
	// The same target twice is refused rather than silently duplicated.
	if rr := do(t, h, "POST", base, map[string]any{"kind": "event", "target": "signup"}, nil); rr.Code != 422 {
		t.Fatalf("duplicate goal should be refused: %d %s", rr.Code, rr.Body)
	}

	rr = do(t, h, "GET", base+"?range=7d", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var out struct {
		Visitors int `json:"visitors"`
		Goals    []struct {
			Name        string  `json:"name"`
			Conversions int     `json:"conversions"`
			Completions int     `json:"completions"`
			Rate        float64 `json:"rate"`
			Value       int     `json:"value"`
		} `json:"goals"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]struct {
		Name        string  `json:"name"`
		Conversions int     `json:"conversions"`
		Completions int     `json:"completions"`
		Rate        float64 `json:"rate"`
		Value       int     `json:"value"`
	}{}
	for _, g := range out.Goals {
		byName[g.Name] = g
	}
	// Ten distinct daily visitors: three buyers, three lookers, three bounces,
	// plus the out-of-order one.
	if out.Visitors != 10 {
		t.Fatalf("visitors: %d", out.Visitors)
	}
	// Four signups: three buyers plus the out-of-order visitor.
	if g := byName["Signed up"]; g.Conversions != 4 || g.Value != 4*2500 {
		t.Fatalf("signup goal: %+v", g)
	}
	if g := byName["Signed up"]; g.Rate < 39 || g.Rate > 41 {
		t.Fatalf("signup rate should be ~40%%: %+v", g)
	}
	// /pricing was seen by three buyers, three lookers and the odd one out.
	if g := byName["Saw pricing"]; g.Conversions != 7 {
		t.Fatalf("pricing goal: %+v", g)
	}
	// The prefix goal matches every page.
	if g := byName["Any page"]; g.Conversions < 7 {
		t.Fatalf("prefix goal should match every page: %+v", g)
	}

	// Update and delete.
	if rr := do(t, h, "PATCH", base+"/"+goal.ID, map[string]any{"name": "Registered"}, nil); rr.Code != 200 {
		t.Fatalf("update: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "DELETE", base+"/"+goal.ID, nil, nil); rr.Code != 204 {
		t.Fatalf("delete: %d", rr.Code)
	}
	if rr := do(t, h, "DELETE", base+"/"+goal.ID, nil, nil); rr.Code != 404 {
		t.Fatalf("second delete: %d", rr.Code)
	}
	_ = s
}

func TestFunnels(t *testing.T) {
	_, h, siteID, _ := featureFixture(t)
	base := "/api/v1/sites/" + siteID + "/funnels"

	rr := do(t, h, "POST", base, map[string]any{
		"name": "Signup",
		"steps": []map[string]any{
			{"name": "Landed", "target": "/"},
			{"name": "Pricing", "target": "/pricing"},
			{"name": "Signed up", "kind": "event", "target": "signup"},
		},
	}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var f struct{ ID string }
	_ = json.Unmarshal(rr.Body.Bytes(), &f)

	// A one-step funnel is not a funnel.
	if rr := do(t, h, "POST", base, map[string]any{"steps": []map[string]any{{"target": "/"}}}, nil); rr.Code != 422 {
		t.Fatalf("single-step funnel should be refused: %d %s", rr.Code, rr.Body)
	}

	rr = do(t, h, "GET", base+"?range=7d", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var out struct {
		Funnels []struct {
			Name       string  `json:"name"`
			Conversion float64 `json:"conversion"`
			Steps      []struct {
				Name        string  `json:"name"`
				Visitors    int     `json:"visitors"`
				Rate        float64 `json:"rate"`
				DropOff     int     `json:"drop_off"`
				DropOffRate float64 `json:"drop_off_rate"`
			} `json:"steps"`
		} `json:"funnels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Funnels) != 1 {
		t.Fatalf("want one funnel, got %d", len(out.Funnels))
	}
	steps := out.Funnels[0].Steps
	if len(steps) != 3 {
		t.Fatalf("steps: %+v", steps)
	}
	// Nine visitors saw "/" (three buyers, three lookers, three bounces); the
	// out-of-order visitor never did.
	if steps[0].Visitors != 9 {
		t.Fatalf("step 1: %+v", steps[0])
	}
	// Six of them went on to /pricing.
	if steps[1].Visitors != 6 || steps[1].DropOff != 3 {
		t.Fatalf("step 2: %+v", steps[1])
	}
	// Three signed up, and the visitor who signed up before seeing pricing is
	// excluded because a funnel is an ordering, not an intersection.
	if steps[2].Visitors != 3 {
		t.Fatalf("step 3 should exclude the out-of-order visitor: %+v", steps[2])
	}
	if got := out.Funnels[0].Conversion; got < 33 || got > 34 {
		t.Fatalf("conversion should be 3/9: %v", got)
	}

	// A range beyond retention is truncated and says so, rather than showing a
	// short window as if it were the whole one.
	rr = do(t, h, "GET", base+"?range=180d", nil, nil)
	var trunc struct {
		Funnels []struct {
			Truncated     bool `json:"truncated"`
			RetentionDays int  `json:"retention_days"`
		} `json:"funnels"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &trunc)
	if !trunc.Funnels[0].Truncated || trunc.Funnels[0].RetentionDays != 7 {
		t.Fatalf("a 180d funnel with 7d retention must report truncation: %+v", trunc.Funnels[0])
	}

	if rr := do(t, h, "DELETE", base+"/"+f.ID, nil, nil); rr.Code != 204 {
		t.Fatalf("delete: %d", rr.Code)
	}
}

func TestNotes(t *testing.T) {
	_, h, siteID, fixed := featureFixture(t)
	base := "/api/v1/sites/" + siteID + "/notes"

	rr := do(t, h, "POST", base, map[string]any{"text": "Launched on Product Hunt"}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var n struct{ ID, Day, Text string }
	_ = json.Unmarshal(rr.Body.Bytes(), &n)
	if n.Day != fixed.UTC().Format("2006-01-02") {
		t.Fatalf("a note with no day should default to today: %q", n.Day)
	}
	// A dated note.
	if rr := do(t, h, "POST", base, map[string]any{"day": "2026-09-01", "text": "Shipped v2"}, nil); rr.Code != 201 {
		t.Fatalf("dated note: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "POST", base, map[string]any{"day": "not-a-date", "text": "x"}, nil); rr.Code != 422 {
		t.Fatalf("bad date should be refused: %d", rr.Code)
	}
	if rr := do(t, h, "POST", base, map[string]any{"text": strings.Repeat("x", 500)}, nil); rr.Code != 422 {
		t.Fatalf("over-long note should be refused: %d", rr.Code)
	}

	rr = do(t, h, "GET", base+"?range=7d", nil, nil)
	var out struct {
		Notes []struct{ Day, Text string } `json:"notes"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out.Notes) != 2 {
		t.Fatalf("want two notes in the window, got %d: %+v", len(out.Notes), out.Notes)
	}
	// A note outside the window is not returned.
	if rr := do(t, h, "POST", base, map[string]any{"day": "2020-01-01", "text": "ancient"}, nil); rr.Code != 201 {
		t.Fatal("old note")
	}
	rr = do(t, h, "GET", base+"?range=7d", nil, nil)
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out.Notes) != 2 {
		t.Fatalf("a note outside the range must not appear: %+v", out.Notes)
	}

	if rr := do(t, h, "PATCH", base+"/"+n.ID, map[string]any{"text": "Front page of HN"}, nil); rr.Code != 200 {
		t.Fatalf("update: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "DELETE", base+"/"+n.ID, nil, nil); rr.Code != 204 {
		t.Fatalf("delete: %d", rr.Code)
	}
}

func TestSharedDashboard(t *testing.T) {
	_, h, siteID, _ := featureFixture(t)
	base := "/api/v1/sites/" + siteID + "/shares"

	rr := do(t, h, "POST", base, nil, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var sh struct {
		Slug        string `json:"slug"`
		URL         string `json:"url"`
		HasPassword bool   `json:"has_password"`
		ShowRevenue bool   `json:"show_revenue"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &sh)
	if len(sh.Slug) < 12 {
		t.Fatalf("the slug is the only credential, so it must be long: %q", sh.Slug)
	}
	if !strings.HasSuffix(sh.URL, "/shared/"+sh.Slug) {
		t.Fatalf("url: %q", sh.URL)
	}
	if sh.HasPassword || sh.ShowRevenue {
		t.Fatalf("a new share should have no password and no revenue: %+v", sh)
	}

	// The public endpoint needs no credentials at all.
	rr = do(t, h, "GET", "/api/v1/shared/"+sh.Slug+"?range=7d", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("public read: %d %s", rr.Code, rr.Body)
	}
	if got := rr.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("a shared dashboard must not be indexable: %q", got)
	}
	var pub map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &pub)
	site, _ := pub["site"].(map[string]any)
	if site["name"] == nil || site["domain"] == nil {
		t.Fatalf("share payload is missing the site: %v", pub)
	}
	// It must not leak the site's configuration or any revenue.
	for _, leaked := range []string{"exclude_ips", "exclude_paths", "domains"} {
		if _, ok := site[leaked]; ok {
			t.Fatalf("share payload leaks %q", leaked)
		}
	}
	if _, ok := pub["revenue"]; ok {
		t.Fatal("revenue must be opt-in per share")
	}
	if pub["stats"] == nil || pub["goals"] == nil || pub["notes"] == nil {
		t.Fatalf("share payload missing stats, goals or notes: %v", pub)
	}

	// An unknown slug is a plain 404.
	if rr := do(t, h, "GET", "/api/v1/shared/nosuchslug", nil, nil); rr.Code != 404 {
		t.Fatalf("unknown slug: %d", rr.Code)
	}

	// Add a password and the same URL stops answering without it.
	if rr := do(t, h, "PATCH", base+"/"+sh.Slug, map[string]any{"password": "correct-horse"}, nil); rr.Code != 200 {
		t.Fatalf("set password: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "GET", "/api/v1/shared/"+sh.Slug, nil, nil); rr.Code != 401 {
		t.Fatalf("password should now be required: %d", rr.Code)
	}
	// The meta endpoint says a password is needed, so the page can prompt.
	rr = do(t, h, "GET", "/api/v1/shared/"+sh.Slug+"/meta", nil, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"password_required":true`) {
		t.Fatalf("meta: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "GET", "/api/v1/shared/"+sh.Slug, nil, map[string]string{"X-Share-Password": "wrong"}); rr.Code != 401 {
		t.Fatalf("wrong password: %d", rr.Code)
	}
	if rr := do(t, h, "GET", "/api/v1/shared/"+sh.Slug, nil, map[string]string{"X-Share-Password": "correct-horse"}); rr.Code != 200 {
		t.Fatalf("right password: %d %s", rr.Code, rr.Body)
	}
	// Too short a password is refused rather than quietly accepted.
	if rr := do(t, h, "PATCH", base+"/"+sh.Slug, map[string]any{"password": "abc"}, nil); rr.Code != 422 {
		t.Fatalf("short password: %d", rr.Code)
	}

	if rr := do(t, h, "DELETE", base+"/"+sh.Slug, nil, nil); rr.Code != 204 {
		t.Fatalf("delete: %d", rr.Code)
	}
	if rr := do(t, h, "GET", "/api/v1/shared/"+sh.Slug, nil, nil); rr.Code != 404 {
		t.Fatalf("a revoked share must stop working: %d", rr.Code)
	}
}

func TestSharedDashboardNeedsNoLoginWhenAdminIsOn(t *testing.T) {
	// The whole point of a share is that it works for someone with no account,
	// so it must sit outside the admin middleware.
	s := newServer(t, "chris", "correct-horse")
	s.Now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	h := s.Handler()
	rr := doAs(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, "chris", "correct-horse")
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)
	rr = doAs(t, h, "POST", "/api/v1/sites/"+site.ID+"/shares", nil, "chris", "correct-horse")
	if rr.Code != 201 {
		t.Fatalf("create share: %d %s", rr.Code, rr.Body)
	}
	var sh struct{ Slug string }
	_ = json.Unmarshal(rr.Body.Bytes(), &sh)
	if rr := do(t, h, "GET", "/api/v1/shared/"+sh.Slug, nil, nil); rr.Code != 200 {
		t.Fatalf("a share must be readable without a login: %d %s", rr.Code, rr.Body)
	}
	// But the admin API still is not.
	if rr := do(t, h, "GET", "/api/v1/sites", nil, nil); rr.Code != 401 {
		t.Fatalf("admin API leaked: %d", rr.Code)
	}
}

func TestAlerts(t *testing.T) {
	s, h, siteID, _ := featureFixture(t)

	// A webhook receiver to prove delivery.
	var got []byte
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = readAll(r)
		w.WriteHeader(200)
	}))
	defer recv.Close()

	rr := do(t, h, "POST", "/api/v1/alerts", map[string]any{
		"site_id": siteID, "kind": "spike", "metric": "visitors", "window": "24h",
		"threshold": 50, "channel": "webhook", "destination": recv.URL,
	}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var a struct{ ID string }
	_ = json.Unmarshal(rr.Body.Bytes(), &a)

	// Validation: a plain http destination that is not local, a bad email, and
	// a spike with no threshold are all refused.
	for _, bad := range []map[string]any{
		{"kind": "spike", "channel": "webhook", "destination": "http://example.com/hook", "threshold": 10},
		{"kind": "spike", "channel": "email", "destination": "not-an-email", "threshold": 10},
		{"kind": "spike", "channel": "webhook", "destination": recv.URL, "threshold": 0},
		{"kind": "nonsense", "channel": "webhook", "destination": recv.URL, "threshold": 10},
	} {
		if rr := do(t, h, "POST", "/api/v1/alerts", bad, nil); rr.Code != 422 {
			t.Fatalf("%v should be refused, got %d %s", bad, rr.Code, rr.Body)
		}
	}

	rr = do(t, h, "GET", "/api/v1/alerts", nil, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"email_configured":false`) {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}

	// The test endpoint delivers regardless of the condition, so a webhook can
	// be proven before it is relied on.
	if rr := do(t, h, "POST", "/api/v1/alerts/"+a.ID+"/test", nil, nil); rr.Code != 200 {
		t.Fatalf("test: %d %s", rr.Code, rr.Body)
	}
	if !bytes.Contains(got, []byte("Glance test")) {
		t.Fatalf("webhook payload: %s", got)
	}
	// The payload carries "content" and "text" so one shape works for a raw
	// endpoint, Slack and Discord alike.
	var payload map[string]any
	_ = json.Unmarshal(got, &payload)
	for _, k := range []string{"content", "text", "alert"} {
		if payload[k] == nil {
			t.Fatalf("payload missing %q: %s", k, got)
		}
	}

	// The engine runs without firing when nothing has changed enough.
	s.AlertEngine.Now = s.Now
	s.AlertEngine.Run(t.Context())

	if rr := do(t, h, "PATCH", "/api/v1/alerts/"+a.ID, map[string]any{"enabled": false}, nil); rr.Code != 200 {
		t.Fatalf("update: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "DELETE", "/api/v1/alerts/"+a.ID, nil, nil); rr.Code != 204 {
		t.Fatalf("delete: %d", rr.Code)
	}
}

// TestAlertFiresOnSpike drives the engine with data that should trip a rule.
func TestAlertFiresOnSpike(t *testing.T) {
	s := newServer(t, "", "")
	fixed := time.Date(2026, 9, 10, 12, 30, 0, 0, time.UTC)
	s.Now = func() time.Time { return fixed }
	s.AlertEngine.Now = s.Now
	h := s.Handler()
	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"domain": "example.com"}, nil)
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	// Baseline: two visitors in the same hour a week ago. Now: twenty.
	hour := func(t2 time.Time) string { return t2.UTC().Format("2006-01-02T15") }
	lastHour := fixed.Truncate(time.Hour).Add(-time.Hour)
	if _, err := s.DB.Exec(`INSERT INTO hourly_stats (site_id, hour, pageviews, visitors) VALUES (?,?,?,?)`,
		site.ID, hour(lastHour.AddDate(0, 0, -7)), 4, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO hourly_stats (site_id, hour, pageviews, visitors) VALUES (?,?,?,?)`,
		site.ID, hour(lastHour), 40, 20); err != nil {
		t.Fatal(err)
	}

	var fired int
	var body []byte
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired++
		body, _ = readAll(r)
		w.WriteHeader(200)
	}))
	defer recv.Close()

	rr = do(t, h, "POST", "/api/v1/alerts", map[string]any{
		"site_id": site.ID, "kind": "spike", "metric": "visitors", "window": "1h",
		"threshold": 100, "channel": "webhook", "destination": recv.URL, "cooldown_min": 60,
	}, nil)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}

	s.AlertEngine.Run(t.Context())
	if fired != 1 {
		t.Fatalf("a 10x rise past a 100%% threshold should fire once, fired %d", fired)
	}
	if !bytes.Contains(body, []byte("up 900%")) {
		t.Fatalf("notification should quantify the rise: %s", body)
	}
	// The cooldown stops it firing again on the same data.
	s.AlertEngine.Run(t.Context())
	if fired != 1 {
		t.Fatalf("the cooldown should suppress a repeat, fired %d", fired)
	}
}

func TestImportPlausibleZip(t *testing.T) {
	_, h, siteID, _ := featureFixture(t)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("visitors.csv", "date,visitors,pageviews\n2026-08-20,120,300\n2026-08-21,140,360\n")
	add("pages.csv", "date,name,visitors,pageviews\n2026-08-20,/,100,200\n2026-08-20,/pricing,40,100\n")
	add("sources.csv", "date,name,visitors,pageviews\n2026-08-20,Direct,60,120\n2026-08-20,https://news.ycombinator.com/,40,80\n")
	add("countries.csv", "date,name,visitors,pageviews\n2026-08-20,GB,70,150\n")
	add("readme.txt", "ignored")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/v1/sites/"+siteID+"/import?format=plausible", bytes.NewReader(buf.Bytes()))
	req.RemoteAddr = "203.0.113.9:1234"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("import: %d %s", rr.Code, rr.Body)
	}
	var res struct {
		Days       int      `json:"days"`
		Visitors   int      `json:"visitors"`
		Pageviews  int      `json:"pageviews"`
		From, To   string   `json:"-"`
		Dimensions []string `json:"dimensions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Days != 2 || res.Visitors != 260 || res.Pageviews != 660 {
		t.Fatalf("import totals: %+v", res)
	}
	dims := strings.Join(res.Dimensions, ",")
	for _, want := range []string{"page", "ref", "country"} {
		if !strings.Contains(dims, want) {
			t.Fatalf("dimension %q missing: %v", want, res.Dimensions)
		}
	}

	// The imported history reads back through the ordinary stats endpoint,
	// which is the whole reason to import into rollups.
	rr = do(t, h, "GET", "/api/v1/sites/"+siteID+"/breakdown?dim=ref&range=180d", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("breakdown: %d %s", rr.Code, rr.Body)
	}
	body := rr.Body.String()
	// "Direct" is normalised to the empty key Glance uses, and the Hacker News
	// URL is reduced to a host.
	if !strings.Contains(body, `"key":"news.ycombinator.com"`) {
		t.Fatalf("referrer host not normalised: %s", body)
	}
	if strings.Contains(body, `"key":"Direct"`) {
		t.Fatalf("Direct should have become the empty key: %s", body)
	}

	// Re-importing the same file replaces rather than doubles.
	req = httptest.NewRequest("POST", "/api/v1/sites/"+siteID+"/import?format=plausible", bytes.NewReader(buf.Bytes()))
	req.RemoteAddr = "203.0.113.9:1234"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("re-import: %d %s", rr.Code, rr.Body)
	}
	var again struct {
		Visitors int `json:"visitors"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &again)
	if again.Visitors != 260 {
		t.Fatalf("re-import should be idempotent, got %d", again.Visitors)
	}
}

func TestImportGA4AndErrors(t *testing.T) {
	_, h, siteID, _ := featureFixture(t)
	post := func(format, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/sites/"+siteID+"/import?format="+format, strings.NewReader(body))
		req.RemoteAddr = "203.0.113.9:1234"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	// GA4 prefixes its downloads with comment lines and uses YYYYMMDD dates.
	ga4 := "# ----------------------------------------\n" +
		"# Start date: 20260820\n" +
		"\n" +
		"Date,Page path and screen class,Total users,Screen page views\n" +
		"20260820,/docs,80,200\n" +
		"20260821,/docs,90,240\n"
	rr := post("ga4", ga4)
	if rr.Code != 200 {
		t.Fatalf("ga4 import: %d %s", rr.Code, rr.Body)
	}
	var res struct {
		Days     int      `json:"days"`
		Visitors int      `json:"visitors"`
		Warnings []string `json:"warnings"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res.Days != 2 || res.Visitors != 170 {
		t.Fatalf("ga4 totals: %+v", res)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("a single-dimension GA4 export should warn that the rest needs separate exports")
	}

	if rr := post("nonsense", "a,b\n1,2\n"); rr.Code != 422 && rr.Code != 400 {
		t.Fatalf("unknown format: %d %s", rr.Code, rr.Body)
	}
	if rr := post("plausible", "no,date,column\n1,2,3\n"); rr.Code != 422 {
		t.Fatalf("a CSV with no date column should be refused: %d %s", rr.Code, rr.Body)
	}
	if rr := post("plausible", ""); rr.Code != 400 {
		t.Fatalf("empty upload: %d %s", rr.Code, rr.Body)
	}
}

// readAll is a tiny helper so the tests do not each import io.
func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
