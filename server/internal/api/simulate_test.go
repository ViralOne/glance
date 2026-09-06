package api

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ViralOne/glance/server/internal/rollup"
)

// step is one thing a visitor did, used both to generate traffic and to
// replay it when checking a funnel.
type step struct {
	at     time.Time
	kind   string // pageview | event
	target string // path, or event name
}

// TestSimulatedTraffic drives a week of plausible traffic through the real HTTP
// handler and then checks that every reported number agrees with the traffic
// that was generated.
//
// It exists because most of the features added here are aggregates, and an
// aggregate is the kind of thing that can be confidently wrong. Unit tests
// check that a query runs; this checks that the answer is the truth. Every
// expectation below is computed from the generated traffic rather than
// hardcoded, so the simulation can be changed without rewriting the assertions.
func TestSimulatedTraffic(t *testing.T) {
	s := newServer(t, "", "")
	// A fixed clock and a seeded generator: the same traffic every run, so a
	// failure is reproducible rather than a coin toss.
	now := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	rng := rand.New(rand.NewSource(1))
	h := s.Handler()

	rr := do(t, h, "POST", "/api/v1/sites", map[string]any{"name": "Shop", "domain": "shop.example"}, nil)
	if rr.Code != 201 {
		t.Fatalf("create site: %d %s", rr.Code, rr.Body)
	}
	var site siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &site)

	// ---- generate ----

	type want struct {
		pageviews  int
		visitorSet map[string]map[string]bool // day -> visitor
		pages      map[string]int
		refs       map[string]int
		countries  map[string]int
		events     map[string]int
		eventValue map[string]int
		bots       map[string]int
		signupDays map[string]map[string]bool // day -> visitor who signed up
		// seq is every visitor's ordered steps for the day, so a funnel
		// expectation can be replayed rather than approximated.
		seq map[string][]step
	}
	w := want{
		visitorSet: map[string]map[string]bool{}, pages: map[string]int{}, refs: map[string]int{},
		countries: map[string]int{}, events: map[string]int{}, eventValue: map[string]int{},
		bots: map[string]int{}, signupDays: map[string]map[string]bool{},
		seq: map[string][]step{},
	}
	mark := func(m map[string]map[string]bool, day, visitor string) {
		if m[day] == nil {
			m[day] = map[string]bool{}
		}
		m[day][visitor] = true
	}

	// Events are inserted directly, not through /collect, for one reason: the
	// collector stamps every event with "now", and a week of traffic needs
	// timestamps spread over a week. The enrichment the collector would do
	// (referrer host, country, device) is reproduced here, and a separate pass
	// below drives the real endpoint to check that path too.
	insert := func(ts time.Time, visitor, kind, name, path, ref, country, device, props string, value int) {
		t.Helper()
		_, err := s.DB.Exec(`INSERT INTO events
			(site_id, ts, kind, name, path, ref_host, country, device, browser, os, region, city, utm_source, utm_campaign, utm_medium, visitor, props, value)
			VALUES (?,?,?,?,?,?,?,?,'Chrome','macOS','','','','','',?,?,?)`,
			site.ID, ts.UTC().Format("2006-01-02T15:04:05.000000000Z"), kind, name, path, ref, country, device, visitor, props, value)
		if err != nil {
			t.Fatal(err)
		}
		day := ts.UTC().Format("2006-01-02")
		key := day + "|" + visitor
		switch kind {
		case "pageview":
			w.pageviews++
			w.pages[path]++
			w.refs[ref]++
			w.countries[country]++
			mark(w.visitorSet, day, visitor)
			w.seq[key] = append(w.seq[key], step{at: ts, kind: kind, target: path})
		case "event":
			w.events[name]++
			w.eventValue[name] += value
			mark(w.visitorSet, day, visitor)
			w.seq[key] = append(w.seq[key], step{at: ts, kind: kind, target: name})
		case "bot":
			w.bots[name]++
		}
	}

	pages := []string{"/", "/pricing", "/docs", "/blog/launch", "/changelog"}
	refs := []string{"", "", "news.ycombinator.com", "google.com", "x.com", "reddit.com"}
	countries := []string{"GB", "US", "US", "DE", "RO", "FR"}
	devices := []string{"Desktop", "Desktop", "Mobile", "Tablet"}

	// The 7d window is today plus the six days before it, so the traffic is
	// generated into exactly those days. Generating an eighth day and then
	// asserting against it is how the first version of this test "failed".
	for dayOffset := 6; dayOffset >= 0; dayOffset-- {
		dayStart := now.AddDate(0, 0, -dayOffset).Truncate(24 * time.Hour)
		day := dayStart.Format("2006-01-02")
		// Between 12 and 30 visitors a day.
		visitors := 12 + rng.Intn(19)
		for v := 0; v < visitors; v++ {
			visitor := fmt.Sprintf("d%dv%02d", dayOffset, v)
			ref := refs[rng.Intn(len(refs))]
			country := countries[rng.Intn(len(countries))]
			device := devices[rng.Intn(len(devices))]
			at := dayStart.Add(time.Duration(rng.Intn(20)+2)*time.Hour + time.Duration(rng.Intn(60))*time.Minute)

			// Everyone lands somewhere.
			landing := pages[rng.Intn(len(pages))]
			insert(at, visitor, "pageview", "", landing, ref, country, device, "", 0)

			// Roughly half read a second page.
			if rng.Float64() < 0.5 {
				at = at.Add(time.Duration(rng.Intn(5)+1) * time.Minute)
				insert(at, visitor, "pageview", "", pages[rng.Intn(len(pages))], ref, country, device, "", 0)
			}

			// A fifth sign up, and a third of those pay.
			if rng.Float64() < 0.2 {
				at = at.Add(2 * time.Minute)
				plan := []string{"free", "pro", "team"}[rng.Intn(3)]
				insert(at, visitor, "event", "signup", "/pricing", ref, country, device,
					`{"plan":"`+plan+`"}`, 0)
				mark(w.signupDays, day, visitor)
				if rng.Float64() < 0.33 {
					at = at.Add(time.Minute)
					insert(at, visitor, "event", "purchase", "/checkout", ref, country, device, "", 1900)
				}
			}
		}
		// A handful of crawler visits a day, which must stay out of every
		// human total.
		for _, bot := range []string{"GPTBot", "Googlebot", "ClaudeBot", "bingbot"} {
			if rng.Float64() < 0.7 {
				insert(dayStart.Add(3*time.Hour), "", "bot", bot, "/docs", "", "US", "", "", 0)
			}
		}
	}

	for d := 0; d <= 8; d++ {
		if err := rollup.Day(t.Context(), s.DB, site.ID, now.AddDate(0, 0, -d)); err != nil {
			t.Fatal(err)
		}
	}

	// ---- expected totals ----

	// Visitors over a multi-day window are the sum of daily uniques, which is
	// Glance's stated convention and the same one Plausible uses.
	wantVisitors := 0
	for _, set := range w.visitorSet {
		wantVisitors += len(set)
	}
	wantSignupConversions := 0
	for _, set := range w.signupDays {
		wantSignupConversions += len(set)
	}

	// ---- check the dashboard ----

	rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/stats?range=7d", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("stats: %d %s", rr.Code, rr.Body)
	}
	var statsResp struct {
		Stats struct {
			Totals struct {
				Pageviews int `json:"pageviews"`
				Visitors  int `json:"visitors"`
			} `json:"totals"`
			Breakdowns map[string][]struct {
				Key       string `json:"key"`
				Pageviews int    `json:"pageviews"`
				Visitors  int    `json:"visitors"`
				Value     int    `json:"value"`
			} `json:"breakdowns"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &statsResp); err != nil {
		t.Fatal(err)
	}
	got := statsResp.Stats

	if got.Totals.Pageviews != w.pageviews {
		t.Errorf("pageviews: got %d, generated %d", got.Totals.Pageviews, w.pageviews)
	}
	if got.Totals.Visitors != wantVisitors {
		t.Errorf("visitors: got %d, generated %d daily uniques", got.Totals.Visitors, wantVisitors)
	}
	// Every breakdown must sum to the same total it is a breakdown of.
	sumOf := func(dim string) int {
		n := 0
		for _, r := range got.Breakdowns[dim] {
			n += r.Pageviews
		}
		return n
	}
	// Only the top 10 are returned, so compare against the generated top 10
	// rather than the whole set — except for pages, of which there are five.
	if n := sumOf("page"); n != w.pageviews {
		t.Errorf("page breakdown sums to %d, pageviews are %d", n, w.pageviews)
	}
	for _, r := range got.Breakdowns["page"] {
		if w.pages[r.Key] != r.Pageviews {
			t.Errorf("page %q: got %d, generated %d", r.Key, r.Pageviews, w.pages[r.Key])
		}
	}
	for _, r := range got.Breakdowns["country"] {
		if w.countries[r.Key] != r.Pageviews {
			t.Errorf("country %q: got %d, generated %d", r.Key, r.Pageviews, w.countries[r.Key])
		}
	}
	for _, r := range got.Breakdowns["ref"] {
		if w.refs[r.Key] != r.Pageviews {
			t.Errorf("referrer %q: got %d, generated %d", r.Key, r.Pageviews, w.refs[r.Key])
		}
	}
	// Events and their values.
	for _, r := range got.Breakdowns["event"] {
		if w.events[r.Key] != r.Pageviews {
			t.Errorf("event %q: got %d, generated %d", r.Key, r.Pageviews, w.events[r.Key])
		}
		if w.eventValue[r.Key] != r.Value {
			t.Errorf("event %q value: got %d, generated %d", r.Key, r.Value, w.eventValue[r.Key])
		}
	}
	if len(got.Breakdowns["event"]) != 2 {
		t.Errorf("want signup and purchase events, got %+v", got.Breakdowns["event"])
	}

	// Crawlers are recorded, and are not visitors.
	for _, r := range got.Breakdowns["bot"] {
		if w.bots[r.Key] != r.Pageviews {
			t.Errorf("crawler %q: got %d, generated %d", r.Key, r.Pageviews, w.bots[r.Key])
		}
		if r.Visitors != 0 {
			t.Errorf("crawler %q was given %d visitors", r.Key, r.Visitors)
		}
	}
	aiSeen := map[string]bool{}
	for _, r := range got.Breakdowns["aibot"] {
		aiSeen[r.Key] = true
	}
	if !aiSeen["GPTBot"] || !aiSeen["ClaudeBot"] {
		t.Errorf("AI crawlers missing from the ai cut: %v", aiSeen)
	}
	if aiSeen["Googlebot"] || aiSeen["bingbot"] {
		t.Errorf("search crawlers must not be in the ai cut: %v", aiSeen)
	}
	// Property breakdown: the plans people chose.
	plans := 0
	for _, r := range got.Breakdowns["prop"] {
		if !strings.HasPrefix(r.Key, `{"plan":`) {
			t.Errorf("unexpected property key %q", r.Key)
		}
		plans += r.Pageviews
	}
	if plans != w.events["signup"] {
		t.Errorf("property rows sum to %d, signups are %d", plans, w.events["signup"])
	}

	// ---- goals agree with the traffic ----

	if rr := do(t, h, "POST", "/api/v1/sites/"+site.ID+"/goals",
		map[string]any{"name": "Signup", "kind": "event", "target": "signup"}, nil); rr.Code != 201 {
		t.Fatalf("goal: %d %s", rr.Code, rr.Body)
	}
	rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/goals?range=7d", nil, nil)
	var goalsResp struct {
		Visitors int `json:"visitors"`
		Goals    []struct {
			Name        string  `json:"name"`
			Conversions int     `json:"conversions"`
			Completions int     `json:"completions"`
			Rate        float64 `json:"rate"`
		} `json:"goals"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &goalsResp); err != nil {
		t.Fatal(err)
	}
	if len(goalsResp.Goals) != 1 {
		t.Fatalf("goals: %+v", goalsResp)
	}
	g := goalsResp.Goals[0]
	if g.Conversions != wantSignupConversions {
		t.Errorf("goal conversions: got %d, generated %d", g.Conversions, wantSignupConversions)
	}
	if g.Completions != w.events["signup"] {
		t.Errorf("goal completions: got %d, generated %d", g.Completions, w.events["signup"])
	}
	// The rate must be conversions over the visitors the dashboard reports,
	// not over some other denominator.
	wantRate := float64(wantSignupConversions) / float64(goalsResp.Visitors) * 100
	if diff := g.Rate - wantRate; diff > 0.01 || diff < -0.01 {
		t.Errorf("goal rate: got %v, want %v", g.Rate, wantRate)
	}
	if goalsResp.Visitors != got.Totals.Visitors {
		t.Errorf("the goal denominator (%d) disagrees with the dashboard (%d)", goalsResp.Visitors, got.Totals.Visitors)
	}

	// ---- funnels agree with the traffic ----

	if rr := do(t, h, "POST", "/api/v1/sites/"+site.ID+"/funnels", map[string]any{
		"name": "Signup",
		"steps": []map[string]any{
			{"name": "Home", "target": "/"},
			{"name": "Pricing", "target": "/pricing"},
			{"name": "Signup", "kind": "event", "target": "signup"},
		},
	}, nil); rr.Code != 201 {
		t.Fatalf("funnel: %d %s", rr.Code, rr.Body)
	}
	rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/funnels?range=7d", nil, nil)
	var funnelResp struct {
		Funnels []struct {
			Steps []struct {
				Name     string `json:"name"`
				Visitors int    `json:"visitors"`
			} `json:"steps"`
		} `json:"funnels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &funnelResp); err != nil {
		t.Fatal(err)
	}
	if len(funnelResp.Funnels) != 1 {
		t.Fatalf("funnels: %+v", funnelResp)
	}
	steps := funnelResp.Funnels[0].Steps
	// Replay every visitor-day against the funnel definition, the same way the
	// implementation should: a step counts only if it happened at or after the
	// previous one.
	def := []step{{kind: "pageview", target: "/"}, {kind: "pageview", target: "/pricing"}, {kind: "event", target: "signup"}}
	reached := make([]int, len(def))
	for _, events := range w.seq {
		sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
		var since time.Time
		for i, want := range def {
			hit := false
			for _, e := range events {
				if e.kind == want.kind && e.target == want.target && !e.at.Before(since) {
					since, hit = e.at, true
					break
				}
			}
			if !hit {
				break
			}
			reached[i]++
		}
	}
	for i := range def {
		if steps[i].Visitors != reached[i] {
			t.Errorf("funnel step %d (%s): got %d, replaying the generated traffic gives %d",
				i+1, def[i].target, steps[i].Visitors, reached[i])
		}
	}
	// A funnel can only narrow.
	for i := 1; i < len(steps); i++ {
		if steps[i].Visitors > steps[i-1].Visitors {
			t.Errorf("funnel step %d (%d) exceeds step %d (%d)", i+1, steps[i].Visitors, i, steps[i-1].Visitors)
		}
	}

	// ---- the live collector agrees with the same rules ----

	// A second pass through the real endpoint, to prove the write path and the
	// read path share one definition of a visitor.
	before := got.Totals.Visitors
	for i := 0; i < 5; i++ {
		b, _ := json.Marshal(map[string]any{
			"s": site.ID, "u": "https://shop.example/live", "r": "https://news.ycombinator.com/",
			"w": 1440, "tz": "Europe/London",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", strings.NewReader(string(b)))
		// Same address and user agent five times: one visitor, five pageviews.
		req.RemoteAddr = "203.0.113.50:9000"
		req.Header.Set("User-Agent", chromeMac)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 202 {
			t.Fatalf("live collect: %d", rec.Code)
		}
	}
	if err := s.Writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := rollup.Day(t.Context(), s.DB, site.ID, now); err != nil {
		t.Fatal(err)
	}
	rr = do(t, h, "GET", "/api/v1/sites/"+site.ID+"/stats?range=7d", nil, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &statsResp); err != nil {
		t.Fatal(err)
	}
	if n := statsResp.Stats.Totals.Visitors; n != before+1 {
		t.Errorf("five hits from one address should add one visitor: %d -> %d", before, n)
	}
	if n := statsResp.Stats.Totals.Pageviews; n != w.pageviews+5 {
		t.Errorf("pageviews after live traffic: got %d, want %d", n, w.pageviews+5)
	}

	// ---- the export round-trips ----

	rr = do(t, h, "GET", "/api/v1/export", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("export: %d", rr.Code)
	}
	exported := rr.Body.Bytes()

	// Import the export into a second site: the numbers must survive.
	rr = do(t, h, "POST", "/api/v1/sites", map[string]any{"name": "Copy", "domain": "copy.example"}, nil)
	var copySite siteView
	_ = json.Unmarshal(rr.Body.Bytes(), &copySite)
	req := httptest.NewRequest("POST", "/api/v1/sites/"+copySite.ID+"/import?format=glance", strings.NewReader(string(exported)))
	req.RemoteAddr = "192.0.2.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("import own export: %d %s", rec.Code, rec.Body)
	}
	rr = do(t, h, "GET", "/api/v1/sites/"+copySite.ID+"/stats?range=7d", nil, nil)
	var copyResp struct {
		Stats struct {
			Totals struct {
				Pageviews int `json:"pageviews"`
				Visitors  int `json:"visitors"`
			} `json:"totals"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &copyResp); err != nil {
		t.Fatal(err)
	}
	// The export carries every site's rows; the importer writes them all into
	// the target site, so the copy holds the original's totals.
	if copyResp.Stats.Totals.Pageviews != statsResp.Stats.Totals.Pageviews {
		t.Errorf("export/import lost pageviews: %d became %d",
			statsResp.Stats.Totals.Pageviews, copyResp.Stats.Totals.Pageviews)
	}
	if copyResp.Stats.Totals.Visitors != statsResp.Stats.Totals.Visitors {
		t.Errorf("export/import lost visitors: %d became %d",
			statsResp.Stats.Totals.Visitors, copyResp.Stats.Totals.Visitors)
	}
}
