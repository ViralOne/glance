// Package funnels measures how far visitors get through an ordered list of
// steps.
//
// A caveat worth stating plainly, because it shapes the whole design: funnels
// are the one feature here that cannot be answered from rollups. A rollup
// knows how many visitors saw /pricing on a day; it cannot know whether the
// same visitor later saw /checkout, because that is an ordering over
// individual events. So funnels read raw events, which are pruned after the
// retention period — a funnel over 90 days with 30 days of retention honestly
// reports the last 30 and says so. Raising retention is the only fix, and the
// API returns Truncated so the UI can say it rather than quietly showing a
// short window as a full one.
//
// Visitor hashes are day-scoped by design, so a funnel is measured within a
// day: a visitor who lands on Monday and buys on Tuesday is two different
// hashes and cannot be joined. That is the deliberate cost of having no
// cross-day identifier, and it means a funnel here answers "of the people who
// started today, how many finished today".
package funnels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/goals"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/stats"
)

// ErrNotFound is returned when a funnel does not exist.
var ErrNotFound = errors.New("funnel not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid funnel")

// Step is one stage: an event name or a page path.
type Step struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`   // event | path
	Target string `json:"target"` // event name, or path with an optional trailing *
}

// Funnel is an ordered list of steps.
type Funnel struct {
	ID        string `json:"id"`
	SiteID    string `json:"site_id"`
	Name      string `json:"name"`
	Steps     []Step `json:"steps"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
}

// Input is the writable subset.
type Input struct {
	Name  *string `json:"name"`
	Steps *[]Step `json:"steps"`
}

// Limits keep a funnel cheap to evaluate: each step is one pass over the
// window's events for the visitors still in the funnel.
const (
	MinSteps = 2
	MaxSteps = 8
)

// Store persists funnels.
type Store struct{ db *sql.DB }

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

func scan(row interface{ Scan(...any) error }) (Funnel, error) {
	var f Funnel
	var steps string
	if err := row.Scan(&f.ID, &f.SiteID, &f.Name, &steps, &f.Position, &f.CreatedAt); err != nil {
		return Funnel{}, err
	}
	f.Steps = []Step{}
	if err := json.Unmarshal([]byte(steps), &f.Steps); err != nil {
		return Funnel{}, fmt.Errorf("funnel %s has unreadable steps: %w", f.ID, err)
	}
	return f, nil
}

const cols = `id, site_id, name, steps, position, created_at`

// List returns a site's funnels in display order.
func (s *Store) List(ctx context.Context, siteID string) ([]Funnel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM funnels WHERE site_id = ? ORDER BY position, created_at`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Funnel{}
	for rows.Next() {
		f, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Get returns one funnel.
func (s *Store) Get(ctx context.Context, siteID, id string) (Funnel, error) {
	f, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM funnels WHERE site_id = ? AND id = ?`, siteID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Funnel{}, ErrNotFound
	}
	return f, err
}

func validSteps(in []Step) ([]Step, error) {
	if len(in) < MinSteps || len(in) > MaxSteps {
		return nil, fmt.Errorf("%w: a funnel needs between %d and %d steps", ErrInvalid, MinSteps, MaxSteps)
	}
	out := make([]Step, 0, len(in))
	for i, st := range in {
		kind := strings.ToLower(strings.TrimSpace(st.Kind))
		target := strings.TrimSpace(st.Target)
		if kind == "" {
			kind = goals.KindEvent
			if strings.HasPrefix(target, "/") {
				kind = goals.KindPath
			}
		}
		if kind != goals.KindEvent && kind != goals.KindPath {
			return nil, fmt.Errorf("%w: step %d kind must be event or path", ErrInvalid, i+1)
		}
		if target == "" {
			return nil, fmt.Errorf("%w: step %d needs a target", ErrInvalid, i+1)
		}
		if len(target) > 200 {
			return nil, fmt.Errorf("%w: step %d target must be 200 characters or fewer", ErrInvalid, i+1)
		}
		if kind == goals.KindPath && !strings.HasPrefix(target, "/") {
			return nil, fmt.Errorf("%w: step %d path must start with /", ErrInvalid, i+1)
		}
		if strings.Count(target, "*") > 1 || (strings.Contains(target, "*") && !strings.HasSuffix(target, "*")) {
			return nil, fmt.Errorf("%w: step %d may end with a single * to match a prefix", ErrInvalid, i+1)
		}
		name := strings.TrimSpace(st.Name)
		if name == "" {
			name = target
		}
		if len(name) > 80 {
			return nil, fmt.Errorf("%w: step %d name must be 80 characters or fewer", ErrInvalid, i+1)
		}
		out = append(out, Step{Name: name, Kind: kind, Target: target})
	}
	return out, nil
}

// Create inserts a funnel.
func (s *Store) Create(ctx context.Context, siteID string, in Input) (Funnel, error) {
	if in.Steps == nil {
		return Funnel{}, fmt.Errorf("%w: steps are required", ErrInvalid)
	}
	steps, err := validSteps(*in.Steps)
	if err != nil {
		return Funnel{}, err
	}
	f := Funnel{ID: ids.New("fnl"), SiteID: siteID, Steps: steps, CreatedAt: ids.Now()}
	if in.Name != nil {
		f.Name = strings.TrimSpace(*in.Name)
	}
	if f.Name == "" {
		f.Name = "Funnel"
	}
	if len(f.Name) > 80 {
		return Funnel{}, fmt.Errorf("%w: name must be 80 characters or fewer", ErrInvalid)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM funnels WHERE site_id = ?`, siteID).Scan(&f.Position); err != nil {
		return Funnel{}, err
	}
	blob, err := json.Marshal(f.Steps)
	if err != nil {
		return Funnel{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO funnels (`+cols+`) VALUES (?,?,?,?,?,?)`,
		f.ID, f.SiteID, f.Name, string(blob), f.Position, f.CreatedAt)
	return f, err
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, siteID, id string, in Input) (Funnel, error) {
	f, err := s.Get(ctx, siteID, id)
	if err != nil {
		return Funnel{}, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 80 {
			return Funnel{}, fmt.Errorf("%w: name must be 1-80 characters", ErrInvalid)
		}
		f.Name = n
	}
	if in.Steps != nil {
		if f.Steps, err = validSteps(*in.Steps); err != nil {
			return Funnel{}, err
		}
	}
	blob, err := json.Marshal(f.Steps)
	if err != nil {
		return Funnel{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE funnels SET name=?, steps=? WHERE site_id=? AND id=?`, f.Name, string(blob), siteID, id)
	return f, err
}

// Delete removes a funnel.
func (s *Store) Delete(ctx context.Context, siteID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM funnels WHERE site_id = ? AND id = ?`, siteID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StepResult is one step's outcome.
type StepResult struct {
	Step
	// Visitors reached this step having reached every earlier one.
	Visitors int `json:"visitors"`
	// Rate is Visitors as a percentage of the first step's visitors.
	Rate float64 `json:"rate"`
	// DropOff is how many were lost between the previous step and this one.
	DropOff int `json:"drop_off"`
	// DropOffRate is DropOff as a percentage of the previous step.
	DropOffRate float64 `json:"drop_off_rate"`
}

// Result is a funnel measured over a window.
type Result struct {
	Funnel
	Steps []StepResult `json:"steps"`
	// Conversion is the last step's visitors over the first step's.
	Conversion float64 `json:"conversion"`
	// Truncated says the window was cut to the retention period because
	// funnels read raw events; From is where the answer actually starts.
	Truncated     bool   `json:"truncated"`
	RetentionDays int    `json:"retention_days,omitempty"`
	From          string `json:"from"`
	To            string `json:"to"`
}

// Measure evaluates one funnel over a range.
//
// Each step narrows a set of (day, visitor) pairs: the visitors who completed
// step n on a day, restricted to those who completed step n-1 earlier that
// same day. Steps must be in order in time, not merely all present, which is
// what makes it a funnel rather than an intersection.
func (s *Store) Measure(ctx context.Context, f Funnel, rng string, now time.Time, retentionDays int) (Result, error) {
	from, to, _ := stats.Window(rng, now)
	out := Result{Funnel: f, Steps: []StepResult{}}
	if retentionDays > 0 {
		if earliest := now.UTC().AddDate(0, 0, -retentionDays); from.Before(earliest) {
			from, out.Truncated, out.RetentionDays = earliest, true, retentionDays
		}
	}
	out.From, out.To = from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)

	fromTS, toTS := ids.Format(from), ids.Format(to)
	// reached maps a visitor's day to the earliest timestamp at which they had
	// completed every step so far. Keyed by day+visitor because a visitor hash
	// only means one person within one day.
	type key struct{ day, visitor string }
	var reached map[key]string

	for i, st := range f.Steps {
		where, args := stepPredicate(st)
		// Every matching timestamp is read, not just the earliest per visitor.
		// Taking MIN(ts) and then testing it against the previous step drops
		// anyone who did this step once before the previous step and again
		// after it: land on /pricing, go to /, come back to /pricing, and the
		// funnel would say you never reached step two. What matters is the
		// earliest occurrence *at or after* the previous step.
		q := `SELECT substr(ts, 1, 10), visitor, ts FROM events
			WHERE site_id = ? AND ts >= ? AND ts < ? AND visitor != '' AND ` + where + `
			ORDER BY ts`
		qargs := append([]any{f.SiteID, fromTS, toTS}, args...)
		rows, err := s.db.QueryContext(ctx, q, qargs...)
		if err != nil {
			return out, err
		}
		next := map[key]string{}
		for rows.Next() {
			var day, visitor, at string
			if err := rows.Scan(&day, &visitor, &at); err != nil {
				rows.Close()
				return out, err
			}
			k := key{day, visitor}
			// Rows arrive in time order, so the first one accepted for a
			// visitor-day is already the earliest qualifying occurrence.
			if _, done := next[k]; done {
				continue
			}
			if i == 0 {
				next[k] = at
				continue
			}
			if prev, ok := reached[k]; ok && prev <= at {
				next[k] = at
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
		reached = next

		sr := StepResult{Step: st, Visitors: len(reached)}
		if i > 0 {
			prev := out.Steps[i-1].Visitors
			sr.DropOff = prev - sr.Visitors
			if prev > 0 {
				sr.DropOffRate = float64(sr.DropOff) / float64(prev) * 100
			}
		}
		out.Steps = append(out.Steps, sr)
	}
	if len(out.Steps) > 0 && out.Steps[0].Visitors > 0 {
		first := float64(out.Steps[0].Visitors)
		for i := range out.Steps {
			out.Steps[i].Rate = float64(out.Steps[i].Visitors) / first * 100
		}
		out.Conversion = out.Steps[len(out.Steps)-1].Rate
	}
	return out, nil
}

// stepPredicate builds the events filter for one step.
func stepPredicate(st Step) (string, []any) {
	col, kind := "name", "event"
	if st.Kind == goals.KindPath {
		col, kind = "path", "pageview"
	}
	if strings.HasSuffix(st.Target, "*") {
		return `kind = ? AND ` + col + ` LIKE ? ESCAPE '\'`, []any{kind, likePrefix(strings.TrimSuffix(st.Target, "*"))}
	}
	return `kind = ? AND ` + col + ` = ?`, []any{kind, st.Target}
}

func likePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}
