// Package importer loads historical statistics exported from another
// analytics tool straight into Glance's daily rollups.
//
// Nothing is written to the events table, and that is the point: raw events
// exist only to rebuild the last couple of days and are pruned, so importing
// three years of history as events would be discarded within the month. A
// rollup row is kept forever, and it is exactly what the dashboard reads, so
// imported history behaves identically to history Glance measured itself.
//
// The trade is that imported days cannot be filtered or funnelled, because
// those need the individual events nobody exported. The import reports what it
// wrote so the operator can see what they got.
package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Formats Glance can read.
const (
	// FormatPlausible is Plausible's CSV export, a zip of per-dimension files.
	FormatPlausible = "plausible"
	// FormatGA4 is a Google Analytics 4 report CSV, as downloaded from
	// Explore or the Reports UI.
	FormatGA4 = "ga4"
	// FormatFathom is Fathom Analytics' CSV export.
	FormatFathom = "fathom"
	// FormatUmami is Umami's CSV export.
	FormatUmami = "umami"
	// FormatGlance is Glance's own export, for moving between instances.
	FormatGlance = "glance"
)

// Formats is every supported format.
var Formats = []string{FormatPlausible, FormatGA4, FormatFathom, FormatUmami, FormatGlance}

// ErrInvalid wraps unreadable input.
var ErrInvalid = errors.New("invalid import")

// ErrUnsupported is returned for an unknown format.
var ErrUnsupported = errors.New("unsupported import format")

// Result reports what an import wrote.
type Result struct {
	Format string `json:"format"`
	// Days is how many distinct days were imported.
	Days int `json:"days"`
	// Rows is how many rollup rows were written across every dimension.
	Rows int `json:"rows"`
	// Visitors and Pageviews are the totals imported, for a sanity check
	// against the source tool's own figure.
	Visitors  int `json:"visitors"`
	Pageviews int `json:"pageviews"`
	// From and To bound the imported history.
	From string `json:"from"`
	To   string `json:"to"`
	// Dimensions lists which breakdowns the export actually contained; an
	// export without a referrer file simply has no referrer history.
	Dimensions []string `json:"dimensions"`
	// Warnings explains anything skipped, so a partial import is visible
	// rather than silent.
	Warnings []string `json:"warnings"`
}

// day is one imported day, keyed by dimension then key.
type day struct {
	total cell
	byDim map[string]map[string]cell
}

type cell struct {
	pageviews int
	visitors  int
}

// parsed is the intermediate form every format is converted to before it
// touches the database, so the SQL is written once.
type parsed struct {
	days     map[string]*day
	warnings []string
}

func newParsed() *parsed { return &parsed{days: map[string]*day{}} }

func (p *parsed) day(d string) *day {
	if _, ok := p.days[d]; !ok {
		p.days[d] = &day{byDim: map[string]map[string]cell{}}
	}
	return p.days[d]
}

// addTotal records a day's own totals.
func (p *parsed) addTotal(d string, pageviews, visitors int) {
	day := p.day(d)
	day.total.pageviews += pageviews
	day.total.visitors += visitors
}

// add records one breakdown key on a day.
func (p *parsed) add(d, dim, key string, pageviews, visitors int) {
	if dim == "" || key == "" && dim != "ref" {
		// An empty referrer is meaningful (direct); an empty page is not.
		return
	}
	day := p.day(d)
	if day.byDim[dim] == nil {
		day.byDim[dim] = map[string]cell{}
	}
	c := day.byDim[dim][key]
	c.pageviews += pageviews
	c.visitors += visitors
	day.byDim[dim][key] = c
}

func (p *parsed) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, existing := range p.warnings {
		if existing == msg {
			return // one warning per kind, not one per row
		}
	}
	if len(p.warnings) < 20 {
		p.warnings = append(p.warnings, msg)
	}
}

// Importer writes imported history.
type Importer struct{ db *sql.DB }

// New returns an Importer.
func New(db *sql.DB) *Importer { return &Importer{db: db} }

// Import reads data in the named format and writes it to a site's rollups.
//
// Existing rows for an imported day are replaced, not added to, so re-running
// an import is safe and a corrected export overwrites a wrong one. Days Glance
// measured itself are left alone unless the import covers them.
func (i *Importer) Import(ctx context.Context, siteID, format string, data []byte) (Result, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	var p *parsed
	var err error
	switch format {
	case FormatPlausible:
		p, err = parsePlausible(data)
	case FormatGA4:
		p, err = parseGA4(data)
	case FormatFathom:
		p, err = parseFathom(data)
	case FormatUmami:
		p, err = parseUmami(data)
	case FormatGlance:
		p, err = parseGlance(data)
	default:
		return Result{}, fmt.Errorf("%w: %q; try one of %s", ErrUnsupported, format, strings.Join(Formats, ", "))
	}
	if err != nil {
		return Result{}, err
	}
	if len(p.days) == 0 {
		return Result{}, fmt.Errorf("%w: no dated rows found; check the export covers a date range", ErrInvalid)
	}
	return i.write(ctx, siteID, format, p)
}

func (i *Importer) write(ctx context.Context, siteID, format string, p *parsed) (Result, error) {
	res := Result{Format: format, Warnings: p.warnings, Dimensions: []string{}}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	insert, err := tx.PrepareContext(ctx, `INSERT INTO daily_stats (site_id, day, dim, key, pageviews, visitors)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(site_id, day, dim, key) DO UPDATE SET pageviews = excluded.pageviews, visitors = excluded.visitors`)
	if err != nil {
		return res, err
	}
	defer insert.Close()

	dims := map[string]bool{}
	dayKeys := make([]string, 0, len(p.days))
	for d := range p.days {
		dayKeys = append(dayKeys, d)
	}
	sort.Strings(dayKeys)

	for _, d := range dayKeys {
		day := p.days[d]
		// Clear the day first so a dimension that shrank does not keep stale
		// keys, then rewrite it.
		if _, err := tx.ExecContext(ctx, `DELETE FROM daily_stats WHERE site_id = ? AND day = ?`, siteID, d); err != nil {
			return res, err
		}
		total := day.total
		// Some exports give per-page numbers but no day total; deriving it
		// from the pages is better than showing a day with breakdowns and no
		// headline figure.
		if total.pageviews == 0 && total.visitors == 0 {
			for key, c := range day.byDim["page"] {
				_ = key
				total.pageviews += c.pageviews
				if c.visitors > total.visitors {
					total.visitors = c.visitors
				}
			}
			if total.pageviews > 0 {
				p.warn("day totals were derived from per-page rows, so visitors are approximate")
			}
		}
		if _, err := insert.ExecContext(ctx, siteID, d, "total", "", total.pageviews, total.visitors); err != nil {
			return res, err
		}
		res.Rows++
		res.Pageviews += total.pageviews
		res.Visitors += total.visitors
		for dim, keys := range day.byDim {
			dims[dim] = true
			for key, c := range keys {
				if _, err := insert.ExecContext(ctx, siteID, d, dim, key, c.pageviews, c.visitors); err != nil {
					return res, err
				}
				res.Rows++
			}
		}
		// Imported days have no hourly detail, and spreading a day's total
		// across 24 hours would invent a shape nobody measured. The hourly
		// table is therefore cleared for the day, and stats.Summary detects
		// days like this and buckets the whole chart by day instead.
		if _, err := tx.ExecContext(ctx, `DELETE FROM hourly_stats WHERE site_id = ? AND hour >= ? AND hour < ?`,
			siteID, d+"T00", d+"T24"); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Days = len(dayKeys)
	res.From, res.To = dayKeys[0], dayKeys[len(dayKeys)-1]
	for d := range dims {
		res.Dimensions = append(res.Dimensions, d)
	}
	sort.Strings(res.Dimensions)
	return res, nil
}

// ---- format parsers ----

// plausibleFiles maps the filenames in a Plausible export to Glance's
// dimensions. Files Glance has no home for are ignored rather than refused:
// a partial import of the dimensions that do map is more useful than none.
var plausibleFiles = map[string]string{
	"visitors":           "",
	"pages":              "page",
	"entry_pages":        "page",
	"sources":            "ref",
	"referrers":          "ref",
	"countries":          "country",
	"regions":            "region",
	"cities":             "city",
	"devices":            "device",
	"browsers":           "browser",
	"operating_systems":  "os",
	"utm_sources":        "utm_source",
	"utm_campaigns":      "utm_campaign",
	"utm_mediums":        "utm_medium",
	"custom_events":      "event",
	"custom_event_goals": "event",
}

// parsePlausible reads Plausible's export, either the zip or a single CSV.
func parsePlausible(data []byte) (*parsed, error) {
	p := newParsed()
	if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		found := false
		for _, f := range zr.File {
			name := strings.TrimSuffix(path.Base(f.Name), ".csv")
			dim, ok := plausibleFiles[name]
			if !ok {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%w: cannot read %s: %v", ErrInvalid, f.Name, err)
			}
			body, err := io.ReadAll(io.LimitReader(rc, maxMemberBytes))
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("%w: cannot read %s: %v", ErrInvalid, f.Name, err)
			}
			if err := readPlausibleCSV(p, dim, body); err != nil {
				p.warn("skipped %s: %v", f.Name, err)
				continue
			}
			found = true
		}
		if !found {
			return nil, fmt.Errorf("%w: the zip contains no file Glance recognises (expected visitors.csv, pages.csv, sources.csv and friends)", ErrInvalid)
		}
		return p, nil
	}
	// A bare CSV: assume it is the visitors file, which is the one with dates
	// and totals.
	if err := readPlausibleCSV(p, "", data); err != nil {
		return nil, err
	}
	return p, nil
}

// maxMemberBytes caps one file inside an uploaded archive, so a zip bomb
// cannot be expanded into memory.
const maxMemberBytes = 64 << 20

// readPlausibleCSV reads one Plausible CSV. The visitors file has a date
// column and totals; a breakdown file has a name column plus visitors and
// pageviews, and (in newer exports) a date column too.
func readPlausibleCSV(p *parsed, dim string, body []byte) error {
	recs, head, err := readCSV(body)
	if err != nil {
		return err
	}
	dateCol := firstCol(head, "date", "day", "bucket")
	if dateCol < 0 {
		return fmt.Errorf("no date column")
	}
	visitorsCol := firstCol(head, "visitors", "unique_visitors")
	viewsCol := firstCol(head, "pageviews", "views", "total_pageviews", "events")
	nameCol := -1
	if dim != "" {
		nameCol = firstCol(head, "name", "page", "source", "referrer", "country", "region", "city",
			"device", "browser", "operating_system", "utm_source", "utm_campaign", "utm_medium", "event", "goal")
		if nameCol < 0 {
			return fmt.Errorf("no name column")
		}
	}
	for _, rec := range recs {
		d, ok := normDay(at(rec, dateCol))
		if !ok {
			continue
		}
		visitors, views := atoi(at(rec, visitorsCol)), atoi(at(rec, viewsCol))
		if dim == "" {
			p.addTotal(d, views, visitors)
			continue
		}
		p.add(d, dim, normKey(dim, at(rec, nameCol)), views, visitors)
	}
	return nil
}

// ga4Dimensions maps GA4 dimension column names to Glance's.
var ga4Dimensions = map[string]string{
	"pagepath":                "page",
	"pagepathandscreenclass":  "page",
	"pagepathscreenclass":     "page",
	"pagetitleandscreenclass": "page",
	"landingpagescreenclass":  "page",
	"fullpageurl":             "page",
	"pagepathpluserystring":   "page",
	"pagepathplusquerystring": "page",
	"landingpage":             "page",
	"pagelocation":            "page",
	"sessionsource":           "ref",
	"source":                  "ref",
	"sessionsourcemedium":     "ref",
	"firstusersource":         "ref",
	"country":                 "country",
	"region":                  "region",
	"city":                    "city",
	"devicecategory":          "device",
	"browser":                 "browser",
	"operatingsystem":         "os",
	"sessioncampaignname":     "utm_campaign",
	"sessionmanualsource":     "utm_source",
	"sessionmediumnnn":        "utm_medium",
	"sessionmedium":           "utm_medium",
	"eventname":               "event",
}

// parseGA4 reads a GA4 report CSV. Google's downloads carry comment lines
// before the header and a "# " preamble, which readCSV strips.
func parseGA4(data []byte) (*parsed, error) {
	p := newParsed()
	recs, head, err := readCSV(data)
	if err != nil {
		return nil, err
	}
	dateCol := firstCol(head, "date", "day", "nthday", "yearweek", "firstsessiondate")
	if dateCol < 0 {
		return nil, fmt.Errorf("%w: no Date column; export a report with Date as a dimension", ErrInvalid)
	}
	// GA4 has no "visitors" — the nearest is totalUsers or activeUsers.
	visitorsCol := firstCol(head, "totalusers", "activeusers", "users", "newusers")
	viewsCol := firstCol(head, "screenpageviews", "pageviews", "views", "eventcount", "sessions")
	if visitorsCol < 0 && viewsCol < 0 {
		return nil, fmt.Errorf("%w: no user or pageview metric column found", ErrInvalid)
	}
	dim, dimCol := "", -1
	for i, h := range head {
		if d, ok := ga4Dimensions[h]; ok {
			dim, dimCol = d, i
			break
		}
	}
	if visitorsCol < 0 {
		p.warn("the GA4 export has no users column, so visitors are reported as zero")
	}
	for _, rec := range recs {
		d, ok := normDay(at(rec, dateCol))
		if !ok {
			continue
		}
		visitors, views := atoi(at(rec, visitorsCol)), atoi(at(rec, viewsCol))
		if dimCol < 0 {
			p.addTotal(d, views, visitors)
			continue
		}
		p.add(d, dim, normKey(dim, at(rec, dimCol)), views, visitors)
	}
	if dimCol >= 0 {
		p.warn("this export breaks down by %s only; run one export per dimension to import the rest", dim)
	}
	return p, nil
}

// parseFathom reads Fathom's CSV export.
func parseFathom(data []byte) (*parsed, error) {
	p := newParsed()
	recs, head, err := readCSV(data)
	if err != nil {
		return nil, err
	}
	dateCol := firstCol(head, "date", "day", "timestamp")
	if dateCol < 0 {
		return nil, fmt.Errorf("%w: no date column", ErrInvalid)
	}
	visitorsCol := firstCol(head, "visitors", "uniques", "unique_visitors")
	viewsCol := firstCol(head, "pageviews", "views")
	dim, dimCol := "", -1
	for _, cand := range []struct{ header, dim string }{
		{"pathname", "page"}, {"path", "page"}, {"page", "page"},
		{"referrer_hostname", "ref"}, {"referrer", "ref"},
		{"country_code", "country"}, {"country", "country"},
		{"browser", "browser"}, {"os", "os"}, {"device_type", "device"},
	} {
		if i := firstCol(head, cand.header); i >= 0 {
			dim, dimCol = cand.dim, i
			break
		}
	}
	for _, rec := range recs {
		d, ok := normDay(at(rec, dateCol))
		if !ok {
			continue
		}
		visitors, views := atoi(at(rec, visitorsCol)), atoi(at(rec, viewsCol))
		if dimCol < 0 {
			p.addTotal(d, views, visitors)
			continue
		}
		p.add(d, dim, normKey(dim, at(rec, dimCol)), views, visitors)
	}
	return p, nil
}

// parseUmami reads Umami's CSV export, which is shaped much like Fathom's.
func parseUmami(data []byte) (*parsed, error) { return parseFathom(data) }

// glanceExport is the shape of Glance's own export.
type glanceExport struct {
	DailyStats []struct {
		Day       string `json:"day"`
		Dim       string `json:"dim"`
		Key       string `json:"key"`
		Pageviews int    `json:"pageviews"`
		Visitors  int    `json:"visitors"`
	} `json:"daily_stats"`
}

// parseGlance reads Glance's own export, so history can move between
// instances. The site id in the file is ignored: the operator chose which
// site to import into.
func parseGlance(data []byte) (*parsed, error) {
	var in glanceExport
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("%w: not a Glance export: %v", ErrInvalid, err)
	}
	p := newParsed()
	for _, r := range in.DailyStats {
		d, ok := normDay(r.Day)
		if !ok {
			continue
		}
		if r.Dim == "total" {
			p.addTotal(d, r.Pageviews, r.Visitors)
			continue
		}
		p.add(d, r.Dim, r.Key, r.Pageviews, r.Visitors)
	}
	return p, nil
}

// ---- shared CSV helpers ----

// readCSV parses a CSV, returning the data rows and the normalised header.
// Header names are lower-cased with spaces and underscores removed so
// "Screen page views", "screenPageViews" and "screen_page_views" all match.
// bom is the UTF-8 byte order mark, which Excel and several export tools
// prepend and which would otherwise become part of the first header name.
const bom = "\xef\xbb\xbf"

func readCSV(body []byte) (recs [][]string, head []string, err error) {
	// Strip a UTF-8 BOM and any leading comment lines (GA4 writes several).
	text := strings.TrimPrefix(string(body), bom)
	lines := strings.Split(text, "\n")
	start := 0
	for start < len(lines) && (strings.TrimSpace(lines[start]) == "" || strings.HasPrefix(strings.TrimSpace(lines[start]), "#")) {
		start++
	}
	if start >= len(lines) {
		return nil, nil, fmt.Errorf("%w: file has no rows", ErrInvalid)
	}
	r := csv.NewReader(strings.NewReader(strings.Join(lines[start:], "\n")))
	r.FieldsPerRecord = -1 // GA4 appends shorter summary rows
	r.TrimLeadingSpace = true
	all, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(all) < 2 {
		return nil, nil, fmt.Errorf("%w: file has a header but no data rows", ErrInvalid)
	}
	head = make([]string, len(all[0]))
	for i, h := range all[0] {
		head[i] = normHeader(h)
	}
	return all[1:], head, nil
}

func normHeader(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	return strings.NewReplacer(" ", "", "_", "", "-", "", "(", "", ")", "").Replace(h)
}

// firstCol returns the index of the first header that matches any name.
func firstCol(head []string, names ...string) int {
	for _, n := range names {
		want := normHeader(n)
		for i, h := range head {
			if h == want {
				return i
			}
		}
	}
	return -1
}

func at(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

// atoi reads an integer, tolerating thousands separators and decimals that
// export tools sometimes emit for counts.
func atoi(s string) int {
	s = strings.ReplaceAll(strings.ReplaceAll(s, ",", ""), " ", "")
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 {
		return int(f + 0.5)
	}
	return 0
}

// dayLayouts are the date forms the supported tools emit.
var dayLayouts = []string{
	"2006-01-02", "20060102", "02/01/2006", "01/02/2006",
	time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04",
}

// normDay parses a date cell into a UTC YYYY-MM-DD key.
func normDay(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, l := range dayLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC().Format("2006-01-02"), true
		}
	}
	// A bare unix timestamp, which Fathom and Umami sometimes use.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1_000_000_000 && n < 4_000_000_000 {
		return time.Unix(n, 0).UTC().Format("2006-01-02"), true
	}
	return "", false
}

// normKey brings an imported key into the shape Glance stores, so imported
// history sits alongside measured history in the same bars.
func normKey(dim, key string) string {
	key = strings.TrimSpace(key)
	switch dim {
	case "page":
		// Other tools export full URLs or query strings; Glance stores a bare
		// path with no trailing slash.
		if i := strings.Index(key, "://"); i >= 0 {
			if j := strings.IndexByte(key[i+3:], '/'); j >= 0 {
				key = key[i+3+j:]
			} else {
				key = "/"
			}
		}
		if i := strings.IndexAny(key, "?#"); i >= 0 {
			key = key[:i]
		}
		if key == "" {
			key = "/"
		}
		if len(key) > 1 {
			key = strings.TrimRight(key, "/")
			if key == "" {
				key = "/"
			}
		}
		if len(key) > 200 {
			key = key[:200]
		}
		return key
	case "ref":
		// "Direct / None" and friends all mean direct, which Glance stores as
		// the empty key.
		low := strings.ToLower(key)
		if low == "" || low == "direct" || low == "(direct)" || low == "direct / none" || low == "(none)" || low == "none" {
			return ""
		}
		if i := strings.Index(key, "://"); i >= 0 {
			key = key[i+3:]
		}
		if i := strings.IndexByte(key, '/'); i >= 0 {
			key = key[:i]
		}
		// GA4 writes "google / organic"; the host is the part Glance keeps.
		if i := strings.Index(key, " / "); i >= 0 {
			key = key[:i]
		}
		return strings.TrimPrefix(strings.ToLower(key), "www.")
	case "country":
		if len(key) == 2 {
			return strings.ToUpper(key)
		}
		// A country name rather than a code: keep it, since a wrong code is
		// worse than an unmatched name.
		return key
	case "device":
		switch strings.ToLower(key) {
		case "desktop":
			return "Desktop"
		case "mobile", "smartphone", "phone":
			return "Mobile"
		case "tablet":
			return "Tablet"
		}
		return key
	default:
		if len(key) > 200 {
			return key[:200]
		}
		return key
	}
}
