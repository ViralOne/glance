package alerts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/revenue"
	"github.com/ViralOne/glance/server/internal/sites"
	"github.com/ViralOne/glance/server/internal/stats"
)

// Engine evaluates rules and delivers what fires.
type Engine struct {
	Store    *Store
	Sites    *sites.Store
	Stats    *stats.Store
	Revenue  *revenue.Store
	Notifier *Notifier
	// BaseURL is Glance's public address, linked from a notification. Empty
	// omits the link rather than guessing wrongly.
	BaseURL string
	Now     func() time.Time
}

// windowDuration is how far back a rule's window reaches.
func windowDuration(w string) time.Duration {
	switch w {
	case "1h":
		return time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// Run evaluates every enabled alert once.
//
// Failures are recorded on the alert and never returned: one unreachable
// webhook must not stop the other rules from being checked.
func (e *Engine) Run(ctx context.Context) {
	now := e.now()
	list, err := e.Store.List(ctx)
	if err != nil {
		e.Notifier.Log.Warn("alerts.list_failed", "error", err.Error())
		return
	}
	allSites, err := e.Sites.List(ctx)
	if err != nil {
		e.Notifier.Log.Warn("alerts.sites_failed", "error", err.Error())
		return
	}
	byID := map[string]sites.Site{}
	for _, s := range allSites {
		byID[s.ID] = s
	}
	for _, a := range list {
		if !a.Enabled || a.Cooling(now) {
			continue
		}
		targets := allSites
		if a.SiteID != "" {
			s, ok := byID[a.SiteID]
			if !ok {
				continue // the site was deleted; the rule is inert
			}
			targets = []sites.Site{s}
		}
		for _, site := range targets {
			note, fired, err := e.evaluate(ctx, a, site, now)
			if err != nil {
				e.Notifier.Log.Warn("alerts.evaluate_failed", "alert", a.ID, "site", site.ID, "error", err.Error())
				continue
			}
			if !fired {
				continue
			}
			if err := e.Notifier.Deliver(ctx, a, note); err != nil {
				e.Notifier.Log.Warn("alerts.deliver_failed", "alert", a.ID, "channel", a.Channel, "error", err.Error())
				_ = e.Store.MarkFired(ctx, a.ID, now, err.Error())
				continue
			}
			e.Notifier.Log.Info("alerts.fired", "alert", a.ID, "site", site.ID, "kind", a.Kind)
			_ = e.Store.MarkFired(ctx, a.ID, now, "")
			// One notification per evaluation, even for an all-sites rule: a
			// rule that matches five sites at once should not send five
			// messages and then be on cooldown anyway.
			break
		}
	}
}

func (e *Engine) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

// evaluate measures one rule against one site.
func (e *Engine) evaluate(ctx context.Context, a Alert, site sites.Site, now time.Time) (Notification, bool, error) {
	if a.Kind == KindDigest {
		return e.digest(ctx, a, site, now)
	}
	// Every measurement is over whole hours, because that is the finest
	// rollup there is. Comparing a part-finished hour with a complete one
	// would fire a "drop" every time the clock passed the hour, so the window
	// ends at the last hour boundary instead of at this instant. The cost is
	// that a one-hour rule notices a spike up to an hour after it starts; the
	// live view is the right tool for anything faster.
	end := now.UTC().Truncate(time.Hour)
	span := windowDuration(a.Window)
	value, err := e.metric(ctx, a.Metric, site.ID, end.Add(-span), end)
	if err != nil {
		return Notification{}, false, err
	}
	note := Notification{
		AlertID: a.ID, Kind: a.Kind, SiteID: site.ID, SiteName: site.Name, Metric: a.Metric,
		Window: a.Window, Value: value, Threshold: a.Threshold, FiredAt: ids.Format(now),
		URL: e.siteURL(site.ID),
	}
	if a.Kind == KindThreshold {
		if value <= a.Threshold {
			return note, false, nil
		}
		note.Subject = fmt.Sprintf("%s: %s %s over the last %s", site.Name, format(a.Metric, value), a.Metric, a.Window)
		note.Text = fmt.Sprintf("%s had %s %s in the last %s, above the threshold of %s.",
			site.Name, format(a.Metric, value), a.Metric, a.Window, format(a.Metric, a.Threshold))
		return note, true, nil
	}

	// Spike and drop compare with the same window one week earlier, not with
	// the window immediately before: traffic has a weekly shape, and comparing
	// Monday morning with Sunday night fires on the shape rather than on news.
	weekAgo := end.AddDate(0, 0, -7)
	baseline, err := e.metric(ctx, a.Metric, site.ID, weekAgo.Add(-span), weekAgo)
	if err != nil {
		return note, false, err
	}
	note.Baseline = baseline
	if baseline == 0 {
		// With no baseline any traffic is an infinite rise, which is not news.
		// A threshold rule is the right tool for "tell me about any traffic".
		return note, false, nil
	}
	note.ChangePct = (value - baseline) / baseline * 100
	switch a.Kind {
	case KindSpike:
		if note.ChangePct < a.Threshold {
			return note, false, nil
		}
		note.Subject = fmt.Sprintf("%s: %s up %.0f%%", site.Name, a.Metric, note.ChangePct)
		note.Text = fmt.Sprintf("%s had %s %s in the last %s, up %.0f%% on the same window last week (%s).",
			site.Name, format(a.Metric, value), a.Metric, a.Window, note.ChangePct, format(a.Metric, baseline))
	case KindDrop:
		if -note.ChangePct < a.Threshold {
			return note, false, nil
		}
		note.Subject = fmt.Sprintf("%s: %s down %.0f%%", site.Name, a.Metric, -note.ChangePct)
		note.Text = fmt.Sprintf("%s had %s %s in the last %s, down %.0f%% on the same window last week (%s).",
			site.Name, format(a.Metric, value), a.Metric, a.Window, -note.ChangePct, format(a.Metric, baseline))
	}
	if note.URL != "" {
		note.Text += "\n" + note.URL
	}
	return note, true, nil
}

func (e *Engine) metric(ctx context.Context, metric, siteID string, from, to time.Time) (float64, error) {
	if metric == MetricRevenue {
		if e.Revenue == nil {
			return 0, nil
		}
		t, err := e.Revenue.Totals(ctx, siteID, from, to)
		return float64(t.Revenue), err
	}
	t, err := e.Stats.TotalsBetween(ctx, siteID, from, to)
	if err != nil {
		return 0, err
	}
	if metric == MetricPageviews {
		return float64(t.Pageviews), nil
	}
	return float64(t.Visitors), nil
}

func format(metric string, v float64) string {
	if metric == MetricRevenue {
		return fmt.Sprintf("%.2f", v/100)
	}
	return fmt.Sprintf("%.0f", v)
}

func (e *Engine) siteURL(siteID string) string {
	if e.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(e.BaseURL, "/") + "/sites/" + siteID
}

// digestDue reports whether a weekly digest should go out now: Monday, in the
// hour the rule's threshold names (default 08:00 UTC), and not already sent
// this week.
func digestDue(a Alert, now time.Time) bool {
	now = now.UTC()
	if now.Weekday() != time.Monday {
		return false
	}
	hour := int(a.Threshold)
	if hour < 0 || hour > 23 {
		hour = 8
	}
	if now.Hour() != hour {
		return false
	}
	if a.LastFired == "" {
		return true
	}
	last, err := ids.Parse(a.LastFired)
	if err != nil {
		return true
	}
	return now.Sub(last) > 24*time.Hour
}

// digest builds the weekly summary: last seven days against the seven before,
// with the top pages and sources.
func (e *Engine) digest(ctx context.Context, a Alert, site sites.Site, now time.Time) (Notification, bool, error) {
	if !digestDue(a, now) {
		return Notification{}, false, nil
	}
	note, err := e.Digest(ctx, site, now)
	if err != nil {
		return Notification{}, false, err
	}
	note.AlertID, note.Kind = a.ID, KindDigest
	return note, true, nil
}

// Digest builds a site's weekly summary. Exported so the settings page can
// send a test one without waiting for Monday.
func (e *Engine) Digest(ctx context.Context, site sites.Site, now time.Time) (Notification, error) {
	to := now.UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -7)
	cur, err := e.Stats.TotalsBetween(ctx, site.ID, from, to)
	if err != nil {
		return Notification{}, err
	}
	prev, err := e.Stats.TotalsBetween(ctx, site.ID, from.AddDate(0, 0, -7), from)
	if err != nil {
		return Notification{}, err
	}
	change := ""
	if prev.Visitors > 0 {
		pct := float64(cur.Visitors-prev.Visitors) / float64(prev.Visitors) * 100
		change = fmt.Sprintf(" (%+.0f%% on the week before)", pct)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s, week to %s\n\n", site.Name, to.Format("2 January 2006"))
	fmt.Fprintf(&b, "Visitors:  %d%s\n", cur.Visitors, change)
	fmt.Fprintf(&b, "Pageviews: %d\n", cur.Pageviews)

	pages, err := e.Stats.Breakdown(ctx, site.ID, "page", "7d", now, 5)
	if err != nil {
		return Notification{}, err
	}
	if len(pages) > 0 {
		b.WriteString("\nTop pages\n")
		for _, r := range pages {
			fmt.Fprintf(&b, "  %-40s %d\n", truncate(r.Key, 40), r.Visitors)
		}
	}
	refs, err := e.Stats.Breakdown(ctx, site.ID, "ref", "7d", now, 5)
	if err != nil {
		return Notification{}, err
	}
	if len(refs) > 0 {
		b.WriteString("\nTop sources\n")
		for _, r := range refs {
			key := r.Key
			if key == "" {
				key = "Direct"
			}
			fmt.Fprintf(&b, "  %-40s %d\n", truncate(key, 40), r.Visitors)
		}
	}
	if e.Revenue != nil {
		if t, err := e.Revenue.Totals(ctx, site.ID, from, to); err == nil && t.Orders > 0 {
			cur, _ := e.Revenue.Currency(ctx, site.ID)
			fmt.Fprintf(&b, "\nRevenue: %.2f %s from %d orders\n", float64(t.Revenue)/100, strings.ToUpper(cur), t.Orders)
		}
	}
	if url := e.siteURL(site.ID); url != "" {
		fmt.Fprintf(&b, "\n%s\n", url)
	}
	return Notification{
		SiteID: site.ID, SiteName: site.Name, Metric: MetricVisitors, Window: "7d",
		Value: float64(cur.Visitors), Baseline: float64(prev.Visitors), FiredAt: ids.Format(now),
		Subject: fmt.Sprintf("%s: %d visitors last week", site.Name, cur.Visitors),
		Text:    b.String(), URL: e.siteURL(site.ID),
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
