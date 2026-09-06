// Package alerts watches traffic and revenue and notifies a webhook or an
// email address when a rule matches.
//
// The rules are deliberately few, because an alert nobody trusts is worse than
// no alert: a spike or drop against the same window a week earlier, a plain
// threshold, a goal conversion, and a scheduled digest. Every rule reads the
// same rollups the dashboard reads, so an alert can never disagree with what
// the operator sees when they follow it.
package alerts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/enrich"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/mailer"
)

// Kinds of rule.
const (
	// KindSpike fires when the metric is above the same window a week ago by
	// more than Threshold percent.
	KindSpike = "spike"
	// KindDrop fires when it is below by more than Threshold percent.
	KindDrop = "drop"
	// KindThreshold fires when the metric exceeds Threshold outright.
	KindThreshold = "threshold"
	// KindDigest is not a condition but a schedule: a summary every week.
	KindDigest = "digest"
)

// Channels.
const (
	ChannelWebhook = "webhook"
	ChannelEmail   = "email"
)

// Metrics an alert can watch.
const (
	MetricVisitors  = "visitors"
	MetricPageviews = "pageviews"
	MetricRevenue   = "revenue"
)

// Windows an alert can compare over.
var Windows = []string{"1h", "24h", "7d"}

// ErrNotFound is returned when an alert does not exist.
var ErrNotFound = errors.New("alert not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid alert")

// Alert is one rule.
type Alert struct {
	ID string `json:"id"`
	// SiteID is empty for a rule that covers every site.
	SiteID      string  `json:"site_id"`
	Kind        string  `json:"kind"`
	Metric      string  `json:"metric"`
	Window      string  `json:"window"`
	Threshold   float64 `json:"threshold"`
	Channel     string  `json:"channel"`
	Destination string  `json:"destination"`
	Enabled     bool    `json:"enabled"`
	CooldownMin int     `json:"cooldown_min"`
	LastFired   string  `json:"last_fired"`
	LastError   string  `json:"last_error"`
	CreatedAt   string  `json:"created_at"`
}

// Input is the writable subset.
type Input struct {
	SiteID      *string  `json:"site_id"`
	Kind        *string  `json:"kind"`
	Metric      *string  `json:"metric"`
	Window      *string  `json:"window"`
	Threshold   *float64 `json:"threshold"`
	Channel     *string  `json:"channel"`
	Destination *string  `json:"destination"`
	Enabled     *bool    `json:"enabled"`
	CooldownMin *int     `json:"cooldown_min"`
}

// Store persists alerts.
type Store struct{ db *sql.DB }

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `id, site_id, kind, metric, window, threshold, channel, destination, enabled, cooldown_min, last_fired, last_error, created_at`

func scan(row interface{ Scan(...any) error }) (Alert, error) {
	var a Alert
	var enabled int
	err := row.Scan(&a.ID, &a.SiteID, &a.Kind, &a.Metric, &a.Window, &a.Threshold, &a.Channel, &a.Destination,
		&enabled, &a.CooldownMin, &a.LastFired, &a.LastError, &a.CreatedAt)
	a.Enabled = enabled == 1
	return a, err
}

func validate(in Input, a *Alert) error {
	if in.SiteID != nil {
		a.SiteID = strings.TrimSpace(*in.SiteID)
	}
	if in.Kind != nil {
		k := strings.ToLower(strings.TrimSpace(*in.Kind))
		switch k {
		case KindSpike, KindDrop, KindThreshold, KindDigest:
		default:
			return fmt.Errorf("%w: kind must be spike, drop, threshold or digest", ErrInvalid)
		}
		a.Kind = k
	}
	if in.Metric != nil {
		m := strings.ToLower(strings.TrimSpace(*in.Metric))
		switch m {
		case MetricVisitors, MetricPageviews, MetricRevenue:
		default:
			return fmt.Errorf("%w: metric must be visitors, pageviews or revenue", ErrInvalid)
		}
		a.Metric = m
	}
	if in.Window != nil {
		w := strings.ToLower(strings.TrimSpace(*in.Window))
		if !contains(Windows, w) {
			return fmt.Errorf("%w: window must be one of %s", ErrInvalid, strings.Join(Windows, ", "))
		}
		a.Window = w
	}
	if in.Threshold != nil {
		if *in.Threshold < 0 {
			return fmt.Errorf("%w: threshold cannot be negative", ErrInvalid)
		}
		a.Threshold = *in.Threshold
	}
	if in.Channel != nil {
		c := strings.ToLower(strings.TrimSpace(*in.Channel))
		if c != ChannelWebhook && c != ChannelEmail {
			return fmt.Errorf("%w: channel must be webhook or email", ErrInvalid)
		}
		a.Channel = c
	}
	if in.Destination != nil {
		a.Destination = strings.TrimSpace(*in.Destination)
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if in.CooldownMin != nil {
		if *in.CooldownMin < 0 || *in.CooldownMin > 60*24*7 {
			return fmt.Errorf("%w: cooldown must be between 0 minutes and a week", ErrInvalid)
		}
		a.CooldownMin = *in.CooldownMin
	}
	// Cross-field checks, once every field has its final value.
	switch a.Channel {
	case ChannelEmail:
		if !mailer.ValidAddress(a.Destination) {
			return fmt.Errorf("%w: destination must be an email address", ErrInvalid)
		}
	case ChannelWebhook:
		u, err := url.Parse(a.Destination)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && enrich.LocalHost(u.Hostname()))) {
			return fmt.Errorf("%w: destination must be an https webhook URL", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: channel is required", ErrInvalid)
	}
	if a.Kind == KindSpike || a.Kind == KindDrop {
		if a.Threshold <= 0 {
			return fmt.Errorf("%w: a %s rule needs a percentage threshold above zero", ErrInvalid, a.Kind)
		}
	}
	if a.Kind == KindThreshold && a.Threshold <= 0 {
		return fmt.Errorf("%w: a threshold rule needs a threshold above zero", ErrInvalid)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Create inserts an alert.
func (s *Store) Create(ctx context.Context, in Input) (Alert, error) {
	a := Alert{ID: ids.New("alrt"), Kind: KindSpike, Metric: MetricVisitors, Window: "24h", Enabled: true, CooldownMin: 60, CreatedAt: ids.Now()}
	if err := validate(in, &a); err != nil {
		return Alert{}, err
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alerts (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.SiteID, a.Kind, a.Metric, a.Window, a.Threshold, a.Channel, a.Destination, enabled, a.CooldownMin, "", "", a.CreatedAt)
	return a, err
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, id string, in Input) (Alert, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Alert{}, err
	}
	if err := validate(in, &a); err != nil {
		return Alert{}, err
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(ctx, `UPDATE alerts SET site_id=?, kind=?, metric=?, window=?, threshold=?, channel=?, destination=?, enabled=?, cooldown_min=? WHERE id=?`,
		a.SiteID, a.Kind, a.Metric, a.Window, a.Threshold, a.Channel, a.Destination, enabled, a.CooldownMin, id)
	return a, err
}

// Get returns one alert.
func (s *Store) Get(ctx context.Context, id string) (Alert, error) {
	a, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM alerts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}

// List returns every alert.
func (s *Store) List(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM alerts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Delete removes an alert.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFired records a delivery attempt. An empty errMsg means success; a
// failure does not reset the cooldown, so a broken webhook is retried on the
// next evaluation rather than hammered.
func (s *Store) MarkFired(ctx context.Context, id string, at time.Time, errMsg string) error {
	if errMsg != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE alerts SET last_error = ? WHERE id = ?`, errMsg, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET last_fired = ?, last_error = '' WHERE id = ?`, ids.Format(at), id)
	return err
}

// Cooling reports whether the alert fired too recently to fire again.
func (a Alert) Cooling(now time.Time) bool {
	if a.LastFired == "" || a.CooldownMin <= 0 {
		return false
	}
	last, err := ids.Parse(a.LastFired)
	if err != nil {
		return false
	}
	return now.Sub(last) < time.Duration(a.CooldownMin)*time.Minute
}

// Notification is what a fired alert delivers.
type Notification struct {
	AlertID  string  `json:"alert_id"`
	Kind     string  `json:"kind"`
	SiteID   string  `json:"site_id"`
	SiteName string  `json:"site"`
	Metric   string  `json:"metric"`
	Window   string  `json:"window"`
	Value    float64 `json:"value"`
	Baseline float64 `json:"baseline"`
	// ChangePct is the change against the baseline, negative for a drop.
	ChangePct float64 `json:"change_pct"`
	Threshold float64 `json:"threshold"`
	Subject   string  `json:"subject"`
	Text      string  `json:"text"`
	URL       string  `json:"url,omitempty"`
	FiredAt   string  `json:"fired_at"`
}

// Notifier delivers notifications over the configured channels.
type Notifier struct {
	Mail *mailer.Mailer
	HTTP *http.Client
	Log  *slog.Logger
}

// NewNotifier returns a Notifier.
func NewNotifier(m *mailer.Mailer, log *slog.Logger) *Notifier {
	return &Notifier{Mail: m, HTTP: &http.Client{Timeout: 15 * time.Second}, Log: log}
}

// Deliver sends one notification over the alert's channel.
func (n *Notifier) Deliver(ctx context.Context, a Alert, note Notification) error {
	switch a.Channel {
	case ChannelEmail:
		if !n.Mail.Configured() {
			return mailer.ErrNotConfigured
		}
		return n.Mail.Send(mailer.Message{To: a.Destination, Subject: note.Subject, Text: note.Text})
	case ChannelWebhook:
		return n.post(ctx, a.Destination, note)
	}
	return fmt.Errorf("%w: unknown channel %q", ErrInvalid, a.Channel)
}

// post sends the notification as JSON. The payload doubles as a Slack and
// Discord message: both accept a "content"/"text" field and ignore the rest,
// so one shape works for a raw endpoint and for a chat webhook.
func (n *Notifier) post(ctx context.Context, endpoint string, note Notification) error {
	payload := map[string]any{
		"content": note.Text, // Discord
		"text":    note.Text, // Slack
		"alert":   note,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Glance")
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
