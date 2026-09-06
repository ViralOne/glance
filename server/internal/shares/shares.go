// Package shares publishes a site's dashboard read-only at an unguessable
// URL, so numbers can be shown to someone without giving them an account.
//
// A share is capability-based: the slug is the credential, 80 bits of entropy,
// and it grants exactly one site's aggregates and nothing else — no settings,
// no tokens, no other site, and no write of any kind. Revenue is opt-in per
// share, because "how much money this makes" is usually not what you meant to
// hand over with "look at my traffic".
package shares

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ViralOne/glance/server/internal/auth"
	"github.com/ViralOne/glance/server/internal/ids"
)

// ErrNotFound is returned when no share has that slug.
var ErrNotFound = errors.New("shared dashboard not found")

// ErrPassword is returned when a share needs a password and none matched.
var ErrPassword = errors.New("this shared dashboard needs a password")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid share")

// Share is one published dashboard.
type Share struct {
	Slug   string `json:"slug"`
	SiteID string `json:"site_id"`
	// HasPassword reports whether a password is set; the hash never leaves.
	HasPassword bool   `json:"has_password"`
	ShowRevenue bool   `json:"show_revenue"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`

	passwordHash string
}

// Input is the writable subset.
type Input struct {
	// Password sets or clears the password: a pointer to "" clears it.
	Password    *string `json:"password"`
	ShowRevenue *bool   `json:"show_revenue"`
}

// Store persists shares.
type Store struct{ db *sql.DB }

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `slug, site_id, password_hash, show_revenue, created_at, last_seen_at`

func scan(row interface{ Scan(...any) error }) (Share, error) {
	var s Share
	var revenue int
	err := row.Scan(&s.Slug, &s.SiteID, &s.passwordHash, &revenue, &s.CreatedAt, &s.LastSeenAt)
	s.ShowRevenue = revenue == 1
	s.HasPassword = s.passwordHash != ""
	return s, err
}

// MinPassword is the shortest password a share may carry. A share is already
// protected by an unguessable slug, so the password is a second factor rather
// than the only one, but a two-character one is theatre.
const MinPassword = 6

// Create publishes a site.
func (s *Store) Create(ctx context.Context, siteID string, in Input) (Share, error) {
	// 16 base32 characters, 80 bits: not enumerable.
	sh := Share{Slug: ids.Random(10), SiteID: siteID, CreatedAt: ids.Now()}
	if err := apply(in, &sh); err != nil {
		return Share{}, err
	}
	revenue := 0
	if sh.ShowRevenue {
		revenue = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO shares (`+cols+`) VALUES (?,?,?,?,?,?)`,
		sh.Slug, sh.SiteID, sh.passwordHash, revenue, sh.CreatedAt, "")
	return sh, err
}

func apply(in Input, sh *Share) error {
	if in.Password != nil {
		p := strings.TrimSpace(*in.Password)
		switch {
		case p == "":
			sh.passwordHash = ""
		case len(p) < MinPassword:
			return fmt.Errorf("%w: a password must be at least %d characters", ErrInvalid, MinPassword)
		default:
			sh.passwordHash = auth.Hash(p)
		}
		sh.HasPassword = sh.passwordHash != ""
	}
	if in.ShowRevenue != nil {
		sh.ShowRevenue = *in.ShowRevenue
	}
	return nil
}

// Update changes a share's password or revenue visibility.
func (s *Store) Update(ctx context.Context, siteID, slug string, in Input) (Share, error) {
	sh, err := s.forSite(ctx, siteID, slug)
	if err != nil {
		return Share{}, err
	}
	if err := apply(in, &sh); err != nil {
		return Share{}, err
	}
	revenue := 0
	if sh.ShowRevenue {
		revenue = 1
	}
	_, err = s.db.ExecContext(ctx, `UPDATE shares SET password_hash = ?, show_revenue = ? WHERE slug = ?`, sh.passwordHash, revenue, slug)
	return sh, err
}

func (s *Store) forSite(ctx context.Context, siteID, slug string) (Share, error) {
	sh, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM shares WHERE slug = ? AND site_id = ?`, slug, siteID))
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	return sh, err
}

// List returns a site's shares.
func (s *Store) List(ctx context.Context, siteID string) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM shares WHERE site_id = ? ORDER BY created_at`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		sh, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// Delete unpublishes a share.
func (s *Store) Delete(ctx context.Context, siteID, slug string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE slug = ? AND site_id = ?`, slug, siteID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve looks up a share by slug and checks the password when one is set.
//
// The comparison is constant-time and, importantly, an unknown slug and a
// wrong password are distinguished only after the slug is known to exist —
// a caller cannot use the error to enumerate slugs, because guessing one is
// already infeasible.
func (s *Store) Resolve(ctx context.Context, slug, password string) (Share, error) {
	sh, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM shares WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	if sh.passwordHash != "" && !auth.Equal(sh.passwordHash, auth.Hash(password)) {
		return Share{}, ErrPassword
	}
	return sh, nil
}

// Touch records that a share was viewed, at most once a minute so a reloading
// dashboard does not write on every poll.
func (s *Store) Touch(ctx context.Context, slug, at string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE shares SET last_seen_at = ? WHERE slug = ? AND (last_seen_at = '' OR last_seen_at < ?)`,
		at, slug, at)
}
