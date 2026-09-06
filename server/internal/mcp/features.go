package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ViralOne/glance/server/internal/funnels"
	"github.com/ViralOne/glance/server/internal/goals"
	"github.com/ViralOne/glance/server/internal/notes"
	"github.com/ViralOne/glance/server/internal/stats"
)

// ---- goals ----

// GoalsIn asks for a site's conversions.
type GoalsIn struct {
	Site  string `json:"site" jsonschema:"site id, name or domain"`
	Range string `json:"range,omitempty" jsonschema:"24h, 48h, 7d, 30d, 90d or 180d; defaults to 7d"`
}

// GoalsOut is the measured goals.
type GoalsOut struct {
	Site     string         `json:"site"`
	Range    string         `json:"range"`
	Visitors int            `json:"visitors" jsonschema:"visitors over the window, the denominator of every rate"`
	Goals    []goals.Result `json:"goals" jsonschema:"conversions are summed daily converting visitors; rate is conversions over visitors as a percentage; value is in minor currency units"`
}

func (t *tools) goals(ctx context.Context, _ *sdk.CallToolRequest, in GoalsIn) (*sdk.CallToolResult, GoalsOut, error) {
	s, err := t.resolve(ctx, in.Site)
	if err != nil {
		return nil, GoalsOut{}, err
	}
	rng, err := normRange(in.Range)
	if err != nil {
		return nil, GoalsOut{}, err
	}
	out := GoalsOut{Site: s.Name, Range: rng, Goals: []goals.Result{}}
	if t.st.Goals == nil {
		return nil, out, nil
	}
	sum, err := t.st.Stats.Summary(ctx, s.ID, rng, t.st.Now(), 1)
	if err != nil {
		return nil, out, err
	}
	out.Visitors = sum.Totals.Visitors
	if out.Goals, err = t.st.Goals.Measure(ctx, s.ID, rng, t.st.Now(), sum.Totals.Visitors); err != nil {
		return nil, out, err
	}
	return nil, out, nil
}

// ---- funnels ----

// FunnelsIn asks for a site's funnels.
type FunnelsIn struct {
	Site  string `json:"site" jsonschema:"site id, name or domain"`
	Range string `json:"range,omitempty" jsonschema:"24h, 48h, 7d, 30d, 90d or 180d; defaults to 7d"`
}

// FunnelsOut is the measured funnels.
type FunnelsOut struct {
	Site    string           `json:"site"`
	Range   string           `json:"range"`
	Funnels []funnels.Result `json:"funnels" jsonschema:"each step's visitors count only those who completed every earlier step first; truncated means the window was cut to the retention period because funnels read raw events"`
}

func (t *tools) funnels(ctx context.Context, _ *sdk.CallToolRequest, in FunnelsIn) (*sdk.CallToolResult, FunnelsOut, error) {
	s, err := t.resolve(ctx, in.Site)
	if err != nil {
		return nil, FunnelsOut{}, err
	}
	rng, err := normRange(in.Range)
	if err != nil {
		return nil, FunnelsOut{}, err
	}
	out := FunnelsOut{Site: s.Name, Range: rng, Funnels: []funnels.Result{}}
	if t.st.Funnels == nil {
		return nil, out, nil
	}
	list, err := t.st.Funnels.List(ctx, s.ID)
	if err != nil {
		return nil, out, err
	}
	for _, f := range list {
		res, err := t.st.Funnels.Measure(ctx, f, rng, t.st.Now(), t.retention(ctx))
		if err != nil {
			return nil, out, err
		}
		out.Funnels = append(out.Funnels, res)
	}
	return nil, out, nil
}

// ---- Core Web Vitals ----

// VitalsIn asks for a site's Core Web Vitals.
type VitalsIn struct {
	Site  string `json:"site" jsonschema:"site id, name or domain"`
	Range string `json:"range,omitempty" jsonschema:"24h, 48h, 7d, 30d, 90d or 180d; defaults to 7d"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many pages to include, default 10"`
}

// VitalsOut is the measured vitals.
type VitalsOut struct {
	Site   string       `json:"site"`
	Vitals stats.Vitals `json:"vitals" jsonschema:"LCP, INP, TTFB and FCP are milliseconds; CLS is a unitless score multiplied by 1000. rating applies Google's thresholds to p75. A window's p75 is the sample-weighted mean of daily p75s, so treat it as close rather than exact; good_pct and poor_pct are exact counts"`
}

func (t *tools) vitals(ctx context.Context, _ *sdk.CallToolRequest, in VitalsIn) (*sdk.CallToolResult, VitalsOut, error) {
	s, err := t.resolve(ctx, in.Site)
	if err != nil {
		return nil, VitalsOut{}, err
	}
	rng, err := normRange(in.Range)
	if err != nil {
		return nil, VitalsOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 200 {
		limit = 200
	}
	v, err := t.st.Stats.Vitals(ctx, s.ID, rng, t.st.Now(), limit)
	return nil, VitalsOut{Site: s.Name, Vitals: v}, err
}

// ---- notes ----

// NotesIn asks for a site's annotations.
type NotesIn struct {
	Site  string `json:"site" jsonschema:"site id, name or domain"`
	Range string `json:"range,omitempty" jsonschema:"24h, 48h, 7d, 30d, 90d or 180d; defaults to 30d"`
}

// NotesOut is the annotations in the window.
type NotesOut struct {
	Site  string       `json:"site"`
	Range string       `json:"range"`
	Notes []notes.Note `json:"notes" jsonschema:"dated annotations: what the owner changed on a given day, which usually explains a spike"`
}

func (t *tools) notes(ctx context.Context, _ *sdk.CallToolRequest, in NotesIn) (*sdk.CallToolResult, NotesOut, error) {
	s, err := t.resolve(ctx, in.Site)
	if err != nil {
		return nil, NotesOut{}, err
	}
	if in.Range == "" {
		in.Range = "30d"
	}
	rng, err := normRange(in.Range)
	if err != nil {
		return nil, NotesOut{}, err
	}
	out := NotesOut{Site: s.Name, Range: rng, Notes: []notes.Note{}}
	if t.st.Notes == nil {
		return nil, out, nil
	}
	fromDay, toDay := stats.DayRange(rng, t.st.Now())
	if out.Notes, err = t.st.Notes.Between(ctx, s.ID, fromDay, toDay); err != nil {
		return nil, out, err
	}
	return nil, out, nil
}

// AddNoteIn records an annotation.
type AddNoteIn struct {
	Site string `json:"site" jsonschema:"site id, name or domain"`
	Text string `json:"text" jsonschema:"what happened, 200 characters or fewer"`
	Day  string `json:"day,omitempty" jsonschema:"the UTC day as YYYY-MM-DD; defaults to today"`
}

// AddNoteOut is the created annotation.
type AddNoteOut struct {
	Note notes.Note `json:"note"`
}

func (t *tools) addNote(ctx context.Context, _ *sdk.CallToolRequest, in AddNoteIn) (*sdk.CallToolResult, AddNoteOut, error) {
	if !canWrite(ctx) {
		return nil, AddNoteOut{}, fmt.Errorf("this credential is read-only; mint a token with the write scope in Settings to record annotations")
	}
	s, err := t.resolve(ctx, in.Site)
	if err != nil {
		return nil, AddNoteOut{}, err
	}
	if t.st.Notes == nil {
		return nil, AddNoteOut{}, fmt.Errorf("notes are not available on this server")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, AddNoteOut{}, fmt.Errorf("text is required")
	}
	day := strings.TrimSpace(in.Day)
	if day == "" {
		day = t.st.Now().UTC().Format("2006-01-02")
	}
	n, err := t.st.Notes.Create(ctx, s.ID, notes.Input{Day: &day, Text: &text})
	return nil, AddNoteOut{Note: n}, err
}

// ---- crawlers ----

// CrawlersIn asks which crawlers read a site.
type CrawlersIn struct {
	Site   string `json:"site" jsonschema:"site id, name or domain"`
	Range  string `json:"range,omitempty" jsonschema:"24h, 48h, 7d, 30d, 90d or 180d; defaults to 30d"`
	AIOnly bool   `json:"ai_only,omitempty" jsonschema:"only crawlers that feed a language model"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many rows, default 50"`
}

// CrawlersOut is the crawler breakdown.
type CrawlersOut struct {
	Site  string      `json:"site"`
	Range string      `json:"range"`
	Rows  []stats.Row `json:"rows" jsonschema:"key is the crawler's name, pageviews is how many requests it made. Crawlers are never counted as visitors, so visitors is always zero here and these numbers are not part of any traffic total"`
}

// crawlers answers "is ChatGPT reading my docs", which is a question people
// now actually have and which most analytics tools cannot answer because they
// drop bot traffic on the floor.
func (t *tools) crawlers(ctx context.Context, _ *sdk.CallToolRequest, in CrawlersIn) (*sdk.CallToolResult, CrawlersOut, error) {
	s, err := t.resolve(ctx, in.Site)
	if err != nil {
		return nil, CrawlersOut{}, err
	}
	if in.Range == "" {
		in.Range = "30d"
	}
	rng, err := normRange(in.Range)
	if err != nil {
		return nil, CrawlersOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	dim := "bot"
	if in.AIOnly {
		dim = "aibot"
	}
	rows, err := t.st.Stats.Breakdown(ctx, s.ID, dim, rng, t.st.Now(), limit)
	if err != nil {
		return nil, CrawlersOut{}, err
	}
	if rows == nil {
		rows = []stats.Row{}
	}
	return nil, CrawlersOut{Site: s.Name, Range: rng, Rows: rows}, nil
}
