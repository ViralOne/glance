// Package notes stores dated annotations shown as markers on the chart, so a
// spike has a reason next to it a month later.
package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/ids"
)

// ErrNotFound is returned when a note does not exist.
var ErrNotFound = errors.New("note not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid note")

// Note is one annotation on one UTC day.
type Note struct {
	ID        string `json:"id"`
	SiteID    string `json:"site_id"`
	Day       string `json:"day"` // YYYY-MM-DD
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// Input is the writable subset.
type Input struct {
	Day  *string `json:"day"`
	Text *string `json:"text"`
}

// Store persists notes.
type Store struct{ db *sql.DB }

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `id, site_id, day, text, created_at`

func scan(row interface{ Scan(...any) error }) (Note, error) {
	var n Note
	err := row.Scan(&n.ID, &n.SiteID, &n.Day, &n.Text, &n.CreatedAt)
	return n, err
}

// MaxText keeps a marker's tooltip readable.
const MaxText = 200

func validate(in Input, n *Note) error {
	if in.Day != nil {
		d := strings.TrimSpace(*in.Day)
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return fmt.Errorf("%w: day must be YYYY-MM-DD", ErrInvalid)
		}
		n.Day = d
	}
	if in.Text != nil {
		t := strings.TrimSpace(*in.Text)
		if t == "" {
			return fmt.Errorf("%w: text is required", ErrInvalid)
		}
		if len(t) > MaxText {
			return fmt.Errorf("%w: text must be %d characters or fewer", ErrInvalid, MaxText)
		}
		n.Text = t
	}
	if n.Day == "" || n.Text == "" {
		return fmt.Errorf("%w: day and text are required", ErrInvalid)
	}
	return nil
}

// Create inserts a note.
func (s *Store) Create(ctx context.Context, siteID string, in Input) (Note, error) {
	n := Note{ID: ids.New("note"), SiteID: siteID, CreatedAt: ids.Now()}
	if err := validate(in, &n); err != nil {
		return Note{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notes (`+cols+`) VALUES (?,?,?,?,?)`, n.ID, n.SiteID, n.Day, n.Text, n.CreatedAt)
	return n, err
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, siteID, id string, in Input) (Note, error) {
	n, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM notes WHERE site_id = ? AND id = ?`, siteID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, ErrNotFound
	}
	if err != nil {
		return Note{}, err
	}
	if err := validate(in, &n); err != nil {
		return Note{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE notes SET day=?, text=? WHERE site_id=? AND id=?`, n.Day, n.Text, siteID, id)
	return n, err
}

// Delete removes a note.
func (s *Store) Delete(ctx context.Context, siteID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notes WHERE site_id = ? AND id = ?`, siteID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Between returns a site's notes for the UTC days in [fromDay, toDay).
func (s *Store) Between(ctx context.Context, siteID, fromDay, toDay string) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM notes WHERE site_id = ? AND day >= ? AND day < ? ORDER BY day, created_at`,
		siteID, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
