package database

import (
	"context"
	"database/sql"
	"io/fs"
	"sort"
	"testing"

	"github.com/ViralOne/glance/server/migrations"
)

// TestMigrateCreatesSchema applies every migration to a fresh file and checks
// the tables the rest of the server expects are there. Migrations run as one
// transaction per file, so a syntax error anywhere fails the whole start-up;
// this is the cheapest guard against shipping one.
func TestMigrateCreatesSchema(t *testing.T) {
	db, err := Open(t.TempDir() + "/glance.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	have := map[string]bool{}
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		have[n] = true
	}
	rows.Close()

	for _, want := range []string{
		"sites", "events", "vitals", "hourly_stats", "daily_stats", "daily_vitals",
		"goals", "funnels", "notes", "shares", "site_domains", "alerts",
		"orders", "payment_connections", "search_terms", "google_connections",
		"favicons", "settings", "sessions", "api_tokens", "schema_migrations",
	} {
		if !have[want] {
			t.Errorf("missing table %q", want)
		}
	}
	// The Polar-specific tables were folded into the provider-agnostic ones.
	for _, gone := range []string{"polar_orders", "polar_connections"} {
		if have[gone] {
			t.Errorf("table %q should have been migrated away", gone)
		}
	}
}

// TestMigrateIsIdempotent reopens the same file: already-applied migrations
// must be skipped rather than replayed.
func TestMigrateIsIdempotent(t *testing.T) {
	path := t.TempDir() + "/glance.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()
	if err := Migrate(context.Background(), db2); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
}

// TestMigrateCarriesPolarOrdersOver checks the data move, not just the schema:
// anyone upgrading has revenue history in polar_orders that must survive.
func TestMigrateCarriesPolarOrdersOver(t *testing.T) {
	ctx := context.Background()
	db := openOldSchema(t, "0007_features.sql")
	defer db.Close()

	for _, q := range []string{
		`INSERT INTO polar_orders (site_id, order_id, created_at, status, paid, net_amount, refunded_amount, currency, ref)
			VALUES ('site_1', 'ord_1', '2026-01-01T00:00:00Z', 'paid', 1, 1900, 0, 'USD', 'news.ycombinator.com')`,
		`INSERT INTO polar_connections (site_id, access_token, connected_at) VALUES ('site_1', 'polar_pat_x', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	var provider, ref string
	var amount int
	if err := db.QueryRowContext(ctx, `SELECT provider, ref, net_amount FROM orders WHERE order_id = 'ord_1'`).Scan(&provider, &ref, &amount); err != nil {
		t.Fatalf("order not carried over: %v", err)
	}
	if provider != "polar" || ref != "news.ycombinator.com" || amount != 1900 {
		t.Errorf("got provider=%q ref=%q amount=%d", provider, ref, amount)
	}
	var token string
	if err := db.QueryRowContext(ctx, `SELECT access_token FROM payment_connections WHERE site_id = 'site_1' AND provider = 'polar'`).Scan(&token); err != nil {
		t.Fatalf("connection not carried over: %v", err)
	}
	if token != "polar_pat_x" {
		t.Errorf("got token %q", token)
	}
}

// openOldSchema builds a database with every migration *before* stopAt
// applied, so a test can exercise an upgrade the way a real deployment
// experiences it rather than by unpicking the current schema.
func openOldSchema(t *testing.T, stopAt string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/old.db?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name >= stopAt {
			break
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, name); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
