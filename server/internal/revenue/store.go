// Package revenue stores orders and payment-provider connections so revenue
// sits next to traffic, independently of which processor the money came
// through.
//
// Providers differ only in how orders are fetched and how a webhook is
// verified; once an order is normalised it is the same row whether it came
// from Polar or Stripe, and a site may connect more than one. Every amount is
// in minor units (cents) so nothing is ever a float, and attribution comes
// from the checkout metadata the site's snippet supplied on first touch.
package revenue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/ids"
)

// Providers.
const (
	Polar  = "polar"
	Stripe = "stripe"
)

// Providers is every supported processor.
var Providers = []string{Polar, Stripe}

// ValidProvider reports whether p is supported.
func ValidProvider(p string) bool {
	for _, v := range Providers {
		if v == p {
			return true
		}
	}
	return false
}

// ErrNotConnected is returned when a site has no connection for a provider.
var ErrNotConnected = errors.New("site is not connected to a payment provider")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid payment settings")

// Connection is a site's link to one provider. Secrets never serialise.
type Connection struct {
	SiteID           string `json:"site_id"`
	Provider         string `json:"provider"`
	AccessToken      string `json:"-"`
	Server           string `json:"server"`
	ProductIDs       string `json:"product_ids"`
	WebhookSecret    string `json:"-"`
	HasWebhookSecret bool   `json:"has_webhook_secret"`
	ConnectedAt      string `json:"connected_at"`
	SyncedAt         string `json:"synced_at"`
	SyncError        string `json:"sync_error"`
}

// Products splits the comma-separated filter.
func (c Connection) Products() []string {
	var out []string
	for _, p := range strings.Split(c.ProductIDs, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Input is what a provider's settings panel sends. Omitted secrets are kept,
// so the UI never has to echo a token back to be able to save a change to
// something else.
type Input struct {
	AccessToken   *string `json:"access_token"`
	Server        *string `json:"server"`
	ProductIDs    *string `json:"product_ids"`
	WebhookSecret *string `json:"webhook_secret"`
}

// Order is one order as Glance keeps it.
type Order struct {
	Provider       string
	OrderID        string
	CreatedAt      time.Time
	Status         string
	Paid           bool
	NetAmount      int
	RefundedAmount int
	Currency       string
	Country        string
	Product        string
	Ref            string
	Source         string
	Campaign       string
	Landing        string
}

// Store persists connections and orders.
type Store struct{ db *sql.DB }

// NewStore returns a Store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const connCols = `site_id, provider, access_token, server, product_ids, webhook_secret, connected_at, synced_at, sync_error`

func scanConn(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	err := row.Scan(&c.SiteID, &c.Provider, &c.AccessToken, &c.Server, &c.ProductIDs, &c.WebhookSecret, &c.ConnectedAt, &c.SyncedAt, &c.SyncError)
	c.HasWebhookSecret = c.WebhookSecret != ""
	return c, err
}

// Get returns one connection or ErrNotConnected.
func (s *Store) Get(ctx context.Context, siteID, provider string) (Connection, error) {
	c, err := scanConn(s.db.QueryRowContext(ctx, `SELECT `+connCols+` FROM payment_connections WHERE site_id = ? AND provider = ?`, siteID, provider))
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotConnected
	}
	return c, err
}

// ForSite returns every provider a site is connected to.
func (s *Store) ForSite(ctx context.Context, siteID string) ([]Connection, error) {
	return s.query(ctx, `SELECT `+connCols+` FROM payment_connections WHERE site_id = ? ORDER BY provider`, siteID)
}

// List returns every connection across every site.
func (s *Store) List(ctx context.Context) ([]Connection, error) {
	return s.query(ctx, `SELECT `+connCols+` FROM payment_connections ORDER BY connected_at`)
}

// ForProvider returns every site connected to one provider, so each
// provider's background sync only walks its own connections.
func (s *Store) ForProvider(ctx context.Context, provider string) ([]Connection, error) {
	return s.query(ctx, `SELECT `+connCols+` FROM payment_connections WHERE provider = ? ORDER BY connected_at`, provider)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Save inserts or replaces a connection. Orders are cleared when the server or
// product filter changes, since they may no longer belong to this site.
func (s *Store) Save(ctx context.Context, c Connection) error {
	if !ValidProvider(c.Provider) {
		return fmt.Errorf("%w: unknown provider %q", ErrInvalid, c.Provider)
	}
	if c.ConnectedAt == "" {
		c.ConnectedAt = ids.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev, err := scanConn(tx.QueryRowContext(ctx, `SELECT `+connCols+` FROM payment_connections WHERE site_id = ? AND provider = ?`, c.SiteID, c.Provider))
	if err == nil && (prev.Server != c.Server || prev.ProductIDs != c.ProductIDs) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM orders WHERE site_id = ? AND provider = ?`, c.SiteID, c.Provider); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_connections (`+connCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(site_id, provider) DO UPDATE SET access_token=excluded.access_token, server=excluded.server,
		product_ids=excluded.product_ids, webhook_secret=excluded.webhook_secret, synced_at='', sync_error=''`,
		c.SiteID, c.Provider, c.AccessToken, c.Server, c.ProductIDs, c.WebhookSecret, c.ConnectedAt, "", "")
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes one connection and its orders.
func (s *Store) Delete(ctx context.Context, siteID, provider string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM payment_connections WHERE site_id = ? AND provider = ?`, siteID, provider)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotConnected
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM orders WHERE site_id = ? AND provider = ?`, siteID, provider); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSynced records a sync outcome. An empty errMsg means success.
func (s *Store) MarkSynced(ctx context.Context, siteID, provider string, at time.Time, errMsg string) error {
	syncedAt := ""
	if errMsg == "" {
		syncedAt = ids.Format(at)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE payment_connections SET synced_at = CASE WHEN ? = '' THEN synced_at ELSE ? END, sync_error = ?
		WHERE site_id = ? AND provider = ?`, syncedAt, syncedAt, errMsg, siteID, provider)
	return err
}

// UpsertOrders writes a batch of orders in one transaction.
func (s *Store) UpsertOrders(ctx context.Context, siteID string, orders []Order) error {
	if len(orders) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO orders
		(site_id, provider, order_id, created_at, status, paid, net_amount, refunded_amount, currency, country, product, ref, source, campaign, landing)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(site_id, provider, order_id) DO UPDATE SET status=excluded.status, paid=excluded.paid, net_amount=excluded.net_amount,
		refunded_amount=excluded.refunded_amount, currency=excluded.currency, country=excluded.country, product=excluded.product,
		ref=excluded.ref, source=excluded.source, campaign=excluded.campaign, landing=excluded.landing`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, o := range orders {
		paid := 0
		if o.Paid {
			paid = 1
		}
		if _, err := stmt.ExecContext(ctx, siteID, o.Provider, o.OrderID, o.CreatedAt.UTC().Format(time.RFC3339), o.Status, paid,
			o.NetAmount, o.RefundedAmount, strings.ToLower(o.Currency), o.Country, o.Product, o.Ref, o.Source, o.Campaign, o.Landing); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LatestOrderAt returns the newest order's creation time for one provider, or
// zero when there is none.
func (s *Store) LatestOrderAt(ctx context.Context, siteID, provider string) (time.Time, error) {
	var at sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(created_at) FROM orders WHERE site_id = ? AND provider = ?`, siteID, provider).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if !at.Valid || at.String == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, at.String)
}

// Count returns how many orders a site has.
func (s *Store) Count(ctx context.Context, siteID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE site_id = ?`, siteID).Scan(&n)
	return n, err
}

// revenueExpr is what an order is worth: paid, net of discounts and tax, less
// anything refunded. Refunds land on the order's day, not the refund's, so a
// past month's figure changes when a refund arrives — which is the honest
// answer to "what did that month earn".
const revenueExpr = `CASE WHEN paid = 1 THEN net_amount - refunded_amount ELSE 0 END`

// Point is one bucket of revenue.
type Point struct {
	T       string `json:"t"`
	Revenue int    `json:"revenue"` // cents
	Orders  int    `json:"orders"`
}

// Totals for a window.
type Totals struct {
	Revenue int `json:"revenue"` // cents
	Orders  int `json:"orders"`
}

// Row is one attribution breakdown entry.
type Row struct {
	Key     string `json:"key"`
	Revenue int    `json:"revenue"` // cents
	Orders  int    `json:"orders"`
}

// Series buckets revenue over [from, to) by hour or day, zero-filled.
func (s *Store) Series(ctx context.Context, siteID string, from, to time.Time, bucket string) ([]Point, error) {
	step, layout, trunc := 24*time.Hour, "2006-01-02", "%Y-%m-%d"
	if bucket == "hour" {
		step, layout, trunc = time.Hour, "2006-01-02T15", "%Y-%m-%dT%H"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT strftime('`+trunc+`', created_at), SUM(`+revenueExpr+`), SUM(paid)
		FROM orders WHERE site_id = ? AND created_at >= ? AND created_at < ? GROUP BY 1`,
		siteID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]Point{}
	for rows.Next() {
		var k string
		var p Point
		if err := rows.Scan(&k, &p.Revenue, &p.Orders); err != nil {
			return nil, err
		}
		got[k] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Point{}
	for t := from; t.Before(to); t = t.Add(step) {
		p := got[t.UTC().Format(layout)]
		p.T = t.Format(time.RFC3339)
		out = append(out, p)
	}
	return out, nil
}

// Totals sums revenue and paid orders over [from, to).
func (s *Store) Totals(ctx context.Context, siteID string, from, to time.Time) (Totals, error) {
	var t Totals
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+revenueExpr+`), 0), COALESCE(SUM(paid), 0)
		FROM orders WHERE site_id = ? AND created_at >= ? AND created_at < ?`,
		siteID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)).Scan(&t.Revenue, &t.Orders)
	return t, err
}

// Dims are the attribution breakdowns.
var Dims = []string{"ref", "source", "campaign", "landing", "country", "product", "provider"}

// ValidDim reports whether dim is a breakdown dimension.
func ValidDim(dim string) bool {
	for _, d := range Dims {
		if d == dim {
			return true
		}
	}
	return false
}

// Breakdown sums revenue per key of one dimension over [from, to).
func (s *Store) Breakdown(ctx context.Context, siteID, dim string, from, to time.Time, limit int) ([]Row, error) {
	if !ValidDim(dim) {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+dim+`, SUM(`+revenueExpr+`), SUM(paid) FROM orders
		WHERE site_id = ? AND created_at >= ? AND created_at < ? AND paid = 1
		GROUP BY `+dim+` ORDER BY 2 DESC, 3 DESC, 1 ASC LIMIT ?`,
		siteID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Key, &r.Revenue, &r.Orders); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Currency returns the currency most orders were charged in.
func (s *Store) Currency(ctx context.Context, siteID string) (string, error) {
	var cur sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT currency FROM orders WHERE site_id = ? GROUP BY currency ORDER BY COUNT(*) DESC LIMIT 1`, siteID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cur.String, err
}
