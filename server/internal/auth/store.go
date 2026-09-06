package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/ViralOne/glance/server/internal/ids"
)

// Credential is the stored admin login.
type Credential struct {
	Username     string
	PasswordHash string
	// Source is "generated" when Glance invented the password on first boot,
	// or "set" once someone chose one. The dashboard nags about the former.
	Source    string
	UpdatedAt string
}

// Sources.
const (
	SourceGenerated = "generated"
	SourceSet       = "set"
	// SourceEnv is not stored; it is reported when the environment supplies
	// the credential, which cannot be changed from the dashboard.
	SourceEnv = "env"
)

// ErrNoCredential is returned when nothing has been stored yet.
var ErrNoCredential = errors.New("no admin credential stored")

// Store persists the single admin credential.
type Store struct{ db *sql.DB }

// NewStore returns a Store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Get returns the stored credential or ErrNoCredential.
func (s *Store) Get(ctx context.Context) (Credential, error) {
	var c Credential
	err := s.db.QueryRowContext(ctx,
		`SELECT username, password_hash, source, updated_at FROM admin_credential WHERE id = 1`).
		Scan(&c.Username, &c.PasswordHash, &c.Source, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNoCredential
	}
	return c, err
}

// Save writes the credential, replacing whatever was there.
func (s *Store) Save(ctx context.Context, username, passwordHash, source string) error {
	username = strings.TrimSpace(username)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_credential (id, username, password_hash, source, updated_at) VALUES (1,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET username=excluded.username, password_hash=excluded.password_hash,
		 source=excluded.source, updated_at=excluded.updated_at`,
		username, passwordHash, source, ids.Now())
	return err
}
