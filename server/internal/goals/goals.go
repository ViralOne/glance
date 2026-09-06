// Package goals turns a custom event or a page into a conversion with a rate.
//
// A goal needs no session and no visitor identity beyond the day-scoped hash
// Glance already keeps: conversions are the visitors who fired the event (or
// saw the page) that day, and the rate is those visitors over all visitors
// that day. That is why goals fit here and funnels are harder — a rate is a
// ratio of two daily counts, while a funnel is an ordering.
package goals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/database"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/stats"
)

// Kinds of goal.
const (
	KindEvent = "event" // a custom event name
	KindPath  = "path"  // a page path, with an optional trailing *
)

// ErrNotFound is returned when a goal does not exist.
var ErrNotFound = errors.New("goal not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid goal")

// Goal is one conversion definition.
type Goal struct {
	ID       string `json:"id"`
	SiteID   string `json:"site_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	// Value is what one conversion is assumed to be worth in minor units,
	// used when the event itself carries no value.
	Value     int    `json:"value"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
}

// Input is the writable subset.
type Input struct {
	Name   *string `json:"name"`
	Kind   *string `json:"kind"`
	Target *string `json:"target"`
	Value  *int    `json:"value"`
}

// Store persists goals.
type Store struct{ db *sql.DB }

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `id, site_id, name, kind, target, value, position, created_at`

func scan(row interface{ Scan(...any) error }) (Goal, error) {
	var g Goal
	err := row.Scan(&g.ID, &g.SiteID, &g.Name, &g.Kind, &g.Target, &g.Value, &g.Position, &g.CreatedAt)
	return g, err
}

// List returns a site's goals in display order.
func (s *Store) List(ctx context.Context, siteID string) ([]Goal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM goals WHERE site_id = ? ORDER BY position, created_at`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Goal{}
	for rows.Next() {
		g, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get returns one goal.
func (s *Store) Get(ctx context.Context, siteID, id string) (Goal, error) {
	g, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM goals WHERE site_id = ? AND id = ?`, siteID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Goal{}, ErrNotFound
	}
	return g, err
}

func validate(in Input, g *Goal) error {
	if in.Kind != nil {
		k := strings.ToLower(strings.TrimSpace(*in.Kind))
		if k != KindEvent && k != KindPath {
			return fmt.Errorf("%w: kind must be %q or %q", ErrInvalid, KindEvent, KindPath)
		}
		g.Kind = k
	}
	if in.Target != nil {
		t := strings.TrimSpace(*in.Target)
		if t == "" {
			return fmt.Errorf("%w: target is required", ErrInvalid)
		}
		if len(t) > 200 {
			return fmt.Errorf("%w: target must be 200 characters or fewer", ErrInvalid)
		}
		if g.Kind == KindPath && !strings.HasPrefix(t, "/") {
			return fmt.Errorf("%w: a path target must start with /", ErrInvalid)
		}
		if strings.Count(t, "*") > 1 || (strings.Contains(t, "*") && !strings.HasSuffix(t, "*")) {
			return fmt.Errorf("%w: a target may end with a single * to match a prefix", ErrInvalid)
		}
		g.Target = t
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if len(n) > 80 {
			return fmt.Errorf("%w: name must be 80 characters or fewer", ErrInvalid)
		}
		g.Name = n
	}
	if g.Name == "" {
		g.Name = g.Target
	}
	if in.Value != nil {
		if *in.Value < 0 {
			return fmt.Errorf("%w: value cannot be negative", ErrInvalid)
		}
		g.Value = *in.Value
	}
	return nil
}

// Create inserts a goal.
func (s *Store) Create(ctx context.Context, siteID string, in Input) (Goal, error) {
	g := Goal{ID: ids.New("goal"), SiteID: siteID, Kind: KindEvent}
	if in.Kind == nil && in.Target != nil && strings.HasPrefix(strings.TrimSpace(*in.Target), "/") {
		// A target that looks like a path almost certainly is one.
		g.Kind = KindPath
	}
	if err := validate(in, &g); err != nil {
		return Goal{}, err
	}
	g.CreatedAt = ids.Now()
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM goals WHERE site_id = ?`, siteID).Scan(&g.Position); err != nil {
		return Goal{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO goals (`+cols+`) VALUES (?,?,?,?,?,?,?,?)`,
		g.ID, g.SiteID, g.Name, g.Kind, g.Target, g.Value, g.Position, g.CreatedAt)
	if database.IsUniqueViolation(err) {
		return Goal{}, fmt.Errorf("%w: a goal for %s %q already exists", ErrInvalid, g.Kind, g.Target)
	}
	if err != nil {
		return Goal{}, err
	}
	return g, nil
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, siteID, id string, in Input) (Goal, error) {
	g, err := s.Get(ctx, siteID, id)
	if err != nil {
		return Goal{}, err
	}
	if err := validate(in, &g); err != nil {
		return Goal{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE goals SET name=?, kind=?, target=?, value=? WHERE site_id=? AND id=?`,
		g.Name, g.Kind, g.Target, g.Value, siteID, id)
	if database.IsUniqueViolation(err) {
		return Goal{}, fmt.Errorf("%w: a goal for %s %q already exists", ErrInvalid, g.Kind, g.Target)
	}
	return g, err
}

// Delete removes a goal.
func (s *Store) Delete(ctx context.Context, siteID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM goals WHERE site_id = ? AND id = ?`, siteID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Result is a goal measured over a window.
type Result struct {
	Goal
	// Conversions is the sum of daily converting visitors, matching the
	// convention used for visitors everywhere else in Glance.
	Conversions int `json:"conversions"`
	// Completions counts every firing, so a visitor who converts twice
	// contributes two.
	Completions int `json:"completions"`
	// Rate is conversions over the window's visitors, as a percentage.
	Rate float64 `json:"rate"`
	// Value is the summed worth: the event's own value where it carried one,
	// otherwise the goal's assumed value per conversion.
	Value int `json:"value"`
}

// Measure evaluates every goal for a site over a range, reading rollups so it
// works however far back the range goes.
func (s *Store) Measure(ctx context.Context, siteID, rng string, now time.Time, visitors int) ([]Result, error) {
	list, err := s.List(ctx, siteID)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(list))
	from, to, _ := stats.Window(rng, now)
	fromDay, toDay := from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")
	for _, g := range list {
		r := Result{Goal: g}
		dim := "event"
		if g.Kind == KindPath {
			dim = "page"
		}
		// A trailing * matches a prefix; SQLite's LIKE needs % for that, and
		// the target is escaped so a literal % or _ in a path cannot widen
		// the match.
		match, args := `key = ?`, []any{g.Target}
		if strings.HasSuffix(g.Target, "*") {
			match = `key LIKE ? ESCAPE '\'`
			args = []any{likePrefix(strings.TrimSuffix(g.Target, "*"))}
		}
		q := `SELECT COALESCE(SUM(visitors), 0), COALESCE(SUM(pageviews), 0), COALESCE(SUM(value), 0)
			FROM daily_stats WHERE site_id = ? AND dim = ? AND day >= ? AND day < ? AND ` + match
		qargs := append([]any{siteID, dim, fromDay, toDay}, args...)
		if err := s.db.QueryRowContext(ctx, q, qargs...).Scan(&r.Conversions, &r.Completions, &r.Value); err != nil {
			return nil, err
		}
		if r.Value == 0 && g.Value > 0 {
			r.Value = g.Value * r.Conversions
		}
		if visitors > 0 {
			r.Rate = float64(r.Conversions) / float64(visitors) * 100
		}
		out = append(out, r)
	}
	return out, nil
}

// likePrefix escapes LIKE metacharacters and appends the wildcard, so a goal
// targeting "/blog/100%_off*" matches that prefix literally.
func likePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}
