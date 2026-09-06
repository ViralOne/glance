// Package stripe links a site to a Stripe account so revenue sits next to
// traffic, using the same order shape as every other provider.
//
// Only a restricted key is needed, and only read access to charges. Glance
// reads Charges rather than Payment Intents or Invoices because a charge is
// the one object that exists for every way money can arrive — subscription,
// one-off, invoice, checkout — and it carries both the amount captured and the
// amount refunded, which is exactly what a revenue figure is.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/enrich"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/revenue"
)

// Provider is this package's key in the shared orders table.
const Provider = revenue.Stripe

// DefaultServer is Stripe's API base.
const DefaultServer = "https://api.stripe.com"

// Re-exported so callers keep talking to one package about Stripe.
var (
	ErrNotConnected = revenue.ErrNotConnected
	ErrInvalid      = revenue.ErrInvalid
)

// Shared shapes.
type (
	Connection = revenue.Connection
	Order      = revenue.Order
	Store      = revenue.Store
	Input      = revenue.Input
)

// ErrSignature is returned when a webhook fails verification.
var ErrSignature = errors.New("webhook signature invalid")

// Service runs the connect check, the pull and the webhook.
type Service struct {
	Store  *Store
	Client *Client
	Log    *slog.Logger
	Now    func() time.Time
	// Domain resolves a site id to its domain, so attribution referrers are
	// normalised the same way page views are.
	Domain func(ctx context.Context, siteID string) string
}

// NewService returns a Service.
func NewService(store *Store, client *Client, log *slog.Logger) *Service {
	return &Service{Store: store, Client: client, Log: log, Now: time.Now}
}

// Connect validates the key against Stripe and saves the connection. On an
// existing connection, omitted secrets are kept.
func (s *Service) Connect(ctx context.Context, siteID string, in Input) (Connection, error) {
	c, err := s.Store.Get(ctx, siteID, Provider)
	if errors.Is(err, ErrNotConnected) {
		c = Connection{SiteID: siteID, Provider: Provider, Server: DefaultServer}
	} else if err != nil {
		return Connection{}, err
	}
	if in.AccessToken != nil && strings.TrimSpace(*in.AccessToken) != "" {
		c.AccessToken = strings.TrimSpace(*in.AccessToken)
	}
	if c.AccessToken == "" {
		return Connection{}, fmt.Errorf("%w: a Stripe secret or restricted key is required", ErrInvalid)
	}
	if !strings.HasPrefix(c.AccessToken, "sk_") && !strings.HasPrefix(c.AccessToken, "rk_") {
		return Connection{}, fmt.Errorf("%w: a Stripe key starts with sk_ or rk_", ErrInvalid)
	}
	if in.Server != nil {
		srv := strings.TrimRight(strings.TrimSpace(*in.Server), "/")
		if srv == "" {
			srv = DefaultServer
		}
		u, err := url.Parse(srv)
		local := err == nil && u.Scheme == "http" && enrich.LocalHost(u.Hostname())
		if err != nil || u.Host == "" || (u.Scheme != "https" && !local) {
			return Connection{}, fmt.Errorf("%w: server must be an https URL", ErrInvalid)
		}
		c.Server = srv
	}
	if c.Server == "" {
		c.Server = DefaultServer
	}
	if in.ProductIDs != nil {
		var clean []string
		for _, p := range strings.Split(*in.ProductIDs, ",") {
			if p = strings.TrimSpace(p); p != "" {
				clean = append(clean, p)
			}
		}
		c.ProductIDs = strings.Join(clean, ",")
	}
	if in.WebhookSecret != nil {
		c.WebhookSecret = strings.TrimSpace(*in.WebhookSecret)
	}
	if err := s.Client.Check(ctx, c.Server, c.AccessToken); err != nil {
		return Connection{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if err := s.Store.Save(ctx, c); err != nil {
		return Connection{}, err
	}
	go s.syncDetached(siteID)
	return s.Store.Get(ctx, siteID, Provider)
}

// backfill is how far the first pull reaches.
const backfill = 2 * 365 * 24 * time.Hour

// overlap is re-pulled on every sync so refunds and status changes on recent
// charges are picked up even without the webhook.
const overlap = 30 * 24 * time.Hour

// Sync pulls charges for one site and records the outcome.
func (s *Service) Sync(ctx context.Context, siteID, domain string) error {
	c, err := s.Store.Get(ctx, siteID, Provider)
	if err != nil {
		return err
	}
	err = s.pull(ctx, c, domain)
	msg := ""
	if err != nil {
		msg = err.Error()
		s.Log.Warn("stripe.sync_failed", "site", siteID, "error", msg)
	} else {
		s.Log.Info("stripe.synced", "site", siteID)
	}
	if merr := s.Store.MarkSynced(ctx, siteID, Provider, s.Now(), msg); merr != nil {
		return merr
	}
	return err
}

func (s *Service) pull(ctx context.Context, c Connection, domain string) error {
	since := s.Now().Add(-backfill)
	if latest, _ := s.Store.LatestOrderAt(ctx, c.SiteID, Provider); !latest.IsZero() {
		since = latest.Add(-overlap)
	}
	raw, err := s.Client.Charges(ctx, c.Server, c.AccessToken, since)
	if err != nil {
		return err
	}
	products := c.Products()
	orders := make([]Order, 0, len(raw))
	for _, r := range raw {
		o, ok := ParseCharge(r, domain)
		if !ok {
			continue
		}
		if len(products) > 0 && !matchesProduct(r, products) {
			continue
		}
		orders = append(orders, o)
	}
	return s.Store.UpsertOrders(ctx, c.SiteID, orders)
}

func (s *Service) syncDetached(siteID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := s.Store.Get(ctx, siteID, Provider); err != nil {
		return
	}
	_ = s.Sync(ctx, siteID, s.domainOf(siteID))
}

func (s *Service) domainOf(siteID string) string {
	if s.Domain == nil {
		return ""
	}
	return s.Domain(context.Background(), siteID)
}

// SyncStale pulls every connected site not refreshed within maxAge.
func (s *Service) SyncStale(ctx context.Context, maxAge time.Duration) {
	list, err := s.Store.ForProvider(ctx, Provider)
	if err != nil {
		return
	}
	for _, c := range list {
		if t, err := ids.Parse(c.SyncedAt); err == nil && s.Now().Sub(t) < maxAge {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_ = s.Sync(sctx, c.SiteID, s.domainOf(c.SiteID))
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
}

// ParseCharge turns a raw Stripe charge into Glance's order shape.
//
// Attribution comes from the charge's own metadata, or from the metadata of
// the payment intent, invoice or subscription behind it when Stripe expanded
// them — a subscription renewal carries the metadata of the original
// checkout, not of the renewal charge, so both are checked.
func ParseCharge(r RawCharge, domain string) (Order, bool) {
	id := r.str("id")
	if id == "" {
		return Order{}, false
	}
	created, ok := r.num("created")
	if !ok {
		return Order{}, false
	}
	o := Order{
		Provider:  Provider,
		OrderID:   id,
		CreatedAt: time.Unix(int64(created), 0).UTC(),
		Status:    r.str("status"),
		Currency:  r.str("currency"),
	}
	// A charge is money only once it is captured and not disputed away.
	o.Paid = o.Status == "succeeded" && r.boolean("captured") && r.boolean("paid")
	// amount_captured is what actually moved; amount is what was requested.
	if n, ok := r.num("amount_captured"); ok && n > 0 {
		o.NetAmount = int(n)
	} else if n, ok := r.num("amount"); ok {
		o.NetAmount = int(n)
	}
	if n, ok := r.num("amount_refunded"); ok {
		o.RefundedAmount = int(n)
	}
	if r.boolean("refunded") && o.RefundedAmount == 0 {
		o.RefundedAmount = o.NetAmount
	}
	if o.RefundedAmount > o.NetAmount {
		o.RefundedAmount = o.NetAmount
	}
	if o.Status == "succeeded" && o.RefundedAmount > 0 {
		o.Status = "refunded"
		if o.RefundedAmount < o.NetAmount {
			o.Status = "partially_refunded"
		}
	}
	o.Country = strings.ToUpper(firstOf(
		r.str("billing_details", "address", "country"),
		r.str("payment_method_details", "card", "country"),
	))
	o.Product = firstOf(r.str("description"), r.str("statement_descriptor"))
	if len(o.Product) > 80 {
		o.Product = o.Product[:80]
	}
	ref, landing := attribution(r)
	o.Ref = enrich.Referrer(ref, domain)
	o.Source, o.Campaign, _ = enrich.UTM(landing)
	if u, err := url.Parse(landing); err == nil && landing != "" {
		o.Landing = u.Path
		if o.Landing == "" {
			o.Landing = "/"
		}
		if len(o.Landing) > 200 {
			o.Landing = o.Landing[:200]
		}
	}
	return o, true
}

// attributionPaths are the metadata bags Glance looks in, nearest first.
var attributionPaths = [][]string{
	{"metadata"},
	{"payment_intent", "metadata"},
	{"invoice", "metadata"},
	{"invoice", "subscription", "metadata"},
	{"invoice", "subscription_details", "metadata"},
}

func attribution(r RawCharge) (ref, landing string) {
	for _, base := range attributionPaths {
		gotRef := r.str(append(append([]string{}, base...), "attr_ref")...)
		gotLanding := r.str(append(append([]string{}, base...), "attr_landing")...)
		if gotRef != "" || gotLanding != "" {
			return gotRef, gotLanding
		}
	}
	return "", ""
}

// matchesProduct reports whether a charge belongs to one of the listed Stripe
// price or product ids, looked up through the invoice lines when present.
func matchesProduct(r RawCharge, want []string) bool {
	for _, id := range collectProductIDs(r) {
		for _, w := range want {
			if id == w {
				return true
			}
		}
	}
	return false
}

func collectProductIDs(r RawCharge) []string {
	var out []string
	add := func(v string) {
		if v != "" {
			out = append(out, v)
		}
	}
	add(r.str("invoice", "subscription_details", "plan", "product"))
	lines, _ := r.path("invoice", "lines", "data").([]any)
	for _, l := range lines {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		line := RawCharge(m)
		add(line.str("price", "product"))
		add(line.str("price", "id"))
		add(line.str("plan", "product"))
		add(line.str("plan", "id"))
	}
	return out
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- webhook ----

// timestampTolerance is generous because Stripe resends failed deliveries
// with the original timestamp.
const timestampTolerance = 24 * time.Hour

// VerifyWebhook checks a Stripe-Signature header: HMAC-SHA256, hex encoded,
// over "timestamp.body", keyed with the endpoint's whsec_ secret.
func VerifyWebhook(secret, sigHeader string, body []byte, now time.Time) error {
	if secret == "" {
		return fmt.Errorf("%w: no webhook secret saved for this site", ErrSignature)
	}
	if sigHeader == "" {
		return fmt.Errorf("%w: missing Stripe-Signature header", ErrSignature)
	}
	var timestamp string
	var sigs []string
	for _, part := range strings.Split(sigHeader, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			timestamp = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if timestamp == "" || len(sigs) == 0 {
		return fmt.Errorf("%w: signature header has no timestamp or v1 signature", ErrSignature)
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || now.Sub(time.Unix(ts, 0)).Abs() > timestampTolerance {
		return fmt.Errorf("%w: timestamp out of tolerance", ErrSignature)
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range sigs {
		if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) == 1 {
			return nil
		}
	}
	return ErrSignature
}

// chargeEvents are the event types that change what a charge is worth.
var chargeEvents = map[string]bool{
	"charge.succeeded":         true,
	"charge.updated":           true,
	"charge.captured":          true,
	"charge.refunded":          true,
	"charge.refund.updated":    true,
	"charge.dispute.created":   true,
	"charge.dispute.closed":    true,
	"payment_intent.succeeded": true,
	"invoice.paid":             true,
}

// HandleWebhook applies one verified event.
//
// The event payload is not trusted to be complete: Stripe sends whatever the
// object looked like at the time, without the expansions Glance needs for
// attribution and product filtering, so the charge is re-fetched by id.
func (s *Service) HandleWebhook(ctx context.Context, c Connection, domain string, body []byte) (string, error) {
	var ev struct {
		Type string `json:"type"`
		Data struct {
			Object RawCharge `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", fmt.Errorf("%w: body is not JSON", ErrInvalid)
	}
	if !chargeEvents[ev.Type] {
		return ev.Type, nil
	}
	id := chargeID(ev.Data.Object)
	if id == "" {
		return ev.Type, nil
	}
	raw, err := s.Client.Charge(ctx, c.Server, c.AccessToken, id)
	if err != nil {
		return ev.Type, err
	}
	o, ok := ParseCharge(raw, domain)
	if !ok {
		return ev.Type, fmt.Errorf("%w: charge payload is missing id or created", ErrInvalid)
	}
	if products := c.Products(); len(products) > 0 && !matchesProduct(raw, products) {
		return ev.Type, nil
	}
	return ev.Type, s.Store.UpsertOrders(ctx, c.SiteID, []Order{o})
}

// chargeID finds the charge this event is about, whether the payload is the
// charge itself or a payment intent or invoice that points at one.
func chargeID(o RawCharge) string {
	if strings.HasPrefix(o.str("id"), "ch_") {
		return o.str("id")
	}
	if id := o.str("latest_charge"); id != "" {
		return id
	}
	if id := o.str("charge"); id != "" {
		return id
	}
	if data, ok := o.path("charges", "data").([]any); ok && len(data) > 0 {
		if m, ok := data[0].(map[string]any); ok {
			return RawCharge(m).str("id")
		}
	}
	return ""
}

// Client talks to the Stripe API.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a Client.
func NewClient() *Client { return &Client{HTTP: &http.Client{Timeout: 60 * time.Second}} }

// RawCharge is a charge as Stripe sends it, kept loose so field drift in the
// API never breaks parsing.
type RawCharge map[string]any

// path walks nested objects, returning nil when any step is missing.
func (r RawCharge) path(keys ...string) any {
	var cur any = map[string]any(r)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[k]
		if !ok {
			return nil
		}
	}
	return cur
}

func (r RawCharge) str(keys ...string) string {
	s, _ := r.path(keys...).(string)
	return strings.TrimSpace(s)
}

func (r RawCharge) num(keys ...string) (float64, bool) {
	switch v := r.path(keys...).(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	return 0, false
}

func (r RawCharge) boolean(keys ...string) bool {
	b, _ := r.path(keys...).(bool)
	return b
}

// pageSize is Stripe's list maximum.
const pageSize = 100

// expansions fetch the objects attribution and product filtering need in the
// same round trip. Stripe caps expansion depth at four levels.
var expansions = []string{
	"data.payment_intent",
	"data.invoice",
	"data.invoice.subscription",
}

// Check verifies the key works and has the access Glance needs.
func (c *Client) Check(ctx context.Context, server, key string) error {
	var body struct {
		Data []RawCharge `json:"data"`
	}
	return c.get(ctx, key, strings.TrimRight(server, "/")+"/v1/charges?limit=1", &body)
}

// Charges lists charges created at or after `since`, paging until done.
func (c *Client) Charges(ctx context.Context, server, key string, since time.Time) ([]RawCharge, error) {
	var out []RawCharge
	startingAfter := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}}
		if !since.IsZero() {
			q.Set("created[gte]", strconv.FormatInt(since.Unix(), 10))
		}
		for _, e := range expansions {
			q.Add("expand[]", e)
		}
		if startingAfter != "" {
			q.Set("starting_after", startingAfter)
		}
		var body struct {
			Data    []RawCharge `json:"data"`
			HasMore bool        `json:"has_more"`
		}
		if err := c.get(ctx, key, strings.TrimRight(server, "/")+"/v1/charges?"+q.Encode(), &body); err != nil {
			return nil, err
		}
		out = append(out, body.Data...)
		if !body.HasMore || len(body.Data) == 0 {
			return out, nil
		}
		startingAfter = body.Data[len(body.Data)-1].str("id")
		if startingAfter == "" {
			return out, nil
		}
	}
}

// Charge fetches one charge by id, with the same expansions as a list pull.
func (c *Client) Charge(ctx context.Context, server, key, id string) (RawCharge, error) {
	q := url.Values{}
	for _, e := range expansions {
		// A single retrieve expands without the "data." list prefix.
		q.Add("expand[]", strings.TrimPrefix(e, "data."))
	}
	var out RawCharge
	err := c.get(ctx, key, strings.TrimRight(server, "/")+"/v1/charges/"+url.PathEscape(id)+"?"+q.Encode(), &out)
	return out, err
}

func (c *Client) get(ctx context.Context, key, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	// Pinning the version keeps a Stripe upgrade from silently changing the
	// shape of a field Glance reads.
	req.Header.Set("Stripe-Version", "2024-06-20")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("stripe returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
