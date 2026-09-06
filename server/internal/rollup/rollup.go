// Package rollup rebuilds hourly and daily statistics from raw events.
//
// Today and yesterday (UTC) are rebuilt on every run because raw events for
// those days are still arriving; older days are final. Visitor hashes are
// day-scoped, so a day's visitor count is exact and multi-day totals are the
// sum of daily uniques.
//
// Crawler hits live in the same events table under kind 'bot' with an empty
// visitor. Every human-facing aggregate therefore has to exclude them
// explicitly: a blank visitor would otherwise count as one more distinct
// visitor per day.
package rollup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/enrich"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/vitals"
)

// Dimensions stored in daily_stats.
var Dims = []string{
	"page", "ref", "country", "region", "city", "device", "browser", "os",
	"event", "prop", "utm_source", "utm_campaign", "utm_medium", "bot", "aibot",
}

// DimColumn is the events column behind each dimension.
var DimColumn = map[string]string{
	"page": "path", "ref": "ref_host", "country": "country", "region": "region", "city": "city",
	"device": "device", "browser": "browser", "os": "os", "event": "name", "prop": "props",
	"utm_source": "utm_source", "utm_campaign": "utm_campaign", "utm_medium": "utm_medium",
	"bot": "name", "aibot": "name",
}

// humanOnly excludes crawler rows from a query over events.
const humanOnly = `kind IN ('pageview','event')`

// aiNameList is the SQL literal list of AI crawler display names. The names
// are compile-time constants from the user-agent table, never user input, so
// quoting them here cannot introduce injection.
var aiNameList = func() string {
	quoted := make([]string, 0, len(enrich.AICrawlerNames))
	for _, n := range enrich.AICrawlerNames {
		quoted = append(quoted, "'"+strings.ReplaceAll(n, "'", "''")+"'")
	}
	if len(quoted) == 0 {
		return "''"
	}
	return strings.Join(quoted, ",")
}()

// MaxKeysPerDim caps distinct keys kept per dimension per day; the rest are
// folded into "Other" so one noisy site cannot fill the disk.
const MaxKeysPerDim = 500

// Run rebuilds every site's rollups for today and yesterday.
func Run(ctx context.Context, db *sql.DB, log *slog.Logger, now time.Time) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM sites`)
	if err != nil {
		return err
	}
	var siteIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		siteIDs = append(siteIDs, id)
	}
	rows.Close()
	today := now.UTC().Truncate(24 * time.Hour)
	for _, id := range siteIDs {
		for _, day := range []time.Time{today.AddDate(0, 0, -1), today} {
			if err := Day(ctx, db, id, day); err != nil {
				log.Error("rollup.failed", "site", id, "day", day.Format("2006-01-02"), "error", err.Error())
			}
			if err := Vitals(ctx, db, id, day); err != nil {
				log.Error("rollup.vitals_failed", "site", id, "day", day.Format("2006-01-02"), "error", err.Error())
			}
		}
	}
	return nil
}

// kindFilter is the events predicate for one dimension.
func kindFilter(dim string) string {
	col := DimColumn[dim]
	switch dim {
	case "event":
		return `kind = 'event'`
	case "prop":
		return `kind = 'event' AND props != ''`
	case "bot":
		return `kind = 'bot'`
	case "aibot":
		// AI crawlers are a subset of bots; the ai flag is not stored on the
		// row, so the set is resolved from the crawler name at query time.
		return `kind = 'bot' AND name IN (` + aiNameList + `)`
	case "region", "city", "utm_source", "utm_campaign", "utm_medium":
		// These are frequently empty; an "unknown" bar carries no information.
		return `kind = 'pageview' AND ` + col + ` != ''`
	default:
		return `kind = 'pageview'`
	}
}

// visitorExpr counts distinct visitors, but only for dimensions where a
// visitor exists. Crawler rows carry no visitor at all.
func visitorExpr(dim string) string {
	if dim == "bot" || dim == "aibot" {
		return `0`
	}
	return `COUNT(DISTINCT visitor)`
}

// Day rebuilds one site's rollups for the UTC day starting at day.
func Day(ctx context.Context, db *sql.DB, siteID string, day time.Time) error {
	day = day.UTC().Truncate(24 * time.Hour)
	dayKey := day.Format("2006-01-02")
	from, to := ids.Format(day), ids.Format(day.AddDate(0, 0, 1))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Hourly: pageviews and distinct visitors per hour, humans only.
	if _, err := tx.ExecContext(ctx, `DELETE FROM hourly_stats WHERE site_id = ? AND hour >= ? AND hour < ?`, siteID, dayKey+"T00", dayKey+"T24"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hourly_stats (site_id, hour, pageviews, visitors)
		SELECT site_id, substr(ts, 1, 13), SUM(kind = 'pageview'), COUNT(DISTINCT visitor)
		FROM events WHERE site_id = ? AND ts >= ? AND ts < ? AND `+humanOnly+`
		GROUP BY substr(ts, 1, 13)`, siteID, from, to); err != nil {
		return err
	}

	// Daily: totals plus each dimension.
	if _, err := tx.ExecContext(ctx, `DELETE FROM daily_stats WHERE site_id = ? AND day = ?`, siteID, dayKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_stats (site_id, day, dim, key, pageviews, visitors, value)
		SELECT site_id, ?, 'total', '', SUM(kind = 'pageview'), COUNT(DISTINCT visitor), COALESCE(SUM(value), 0)
		FROM events WHERE site_id = ? AND ts >= ? AND ts < ? AND `+humanOnly+` HAVING COUNT(*) > 0`, dayKey, siteID, from, to); err != nil {
		return err
	}
	for _, dim := range Dims {
		col, filter, visitors := DimColumn[dim], kindFilter(dim), visitorExpr(dim)
		// Top keys by pageviews (or event count), capped.
		q := fmt.Sprintf(`INSERT INTO daily_stats (site_id, day, dim, key, pageviews, visitors, value)
			SELECT site_id, ?, ?, %s, COUNT(*), %s, COALESCE(SUM(value), 0)
			FROM events WHERE site_id = ? AND ts >= ? AND ts < ? AND %s
			GROUP BY %s ORDER BY COUNT(*) DESC LIMIT ?`, col, visitors, filter, col)
		if _, err := tx.ExecContext(ctx, q, dayKey, dim, siteID, from, to, MaxKeysPerDim); err != nil {
			return err
		}
		// Fold the remainder into "Other".
		q = fmt.Sprintf(`INSERT INTO daily_stats (site_id, day, dim, key, pageviews, visitors, value)
			SELECT ?, ?, ?, 'Other', COUNT(*), %s, COALESCE(SUM(value), 0) FROM events
			WHERE site_id = ? AND ts >= ? AND ts < ? AND %s
			AND %s NOT IN (SELECT key FROM daily_stats WHERE site_id = ? AND day = ? AND dim = ?)
			HAVING COUNT(*) > 0
			ON CONFLICT(site_id, day, dim, key) DO UPDATE SET pageviews = pageviews + excluded.pageviews, visitors = visitors + excluded.visitors, value = value + excluded.value`,
			visitors, filter, col)
		if _, err := tx.ExecContext(ctx, q, siteID, dayKey, dim, siteID, from, to, siteID, dayKey, dim); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Vitals rebuilds one site's Core Web Vitals percentiles for a UTC day.
//
// The percentile is computed in Go rather than SQL: SQLite has no percentile
// function, and a day of one site's samples is small enough to sort in memory
// while a window-function query over the same rows is neither shorter nor
// clearer.
func Vitals(ctx context.Context, db *sql.DB, siteID string, day time.Time) error {
	day = day.UTC().Truncate(24 * time.Hour)
	dayKey := day.Format("2006-01-02")
	from, to := ids.Format(day), ids.Format(day.AddDate(0, 0, 1))

	rows, err := db.QueryContext(ctx, `SELECT metric, device, path, value FROM vitals WHERE site_id = ? AND ts >= ? AND ts < ?`, siteID, from, to)
	if err != nil {
		return err
	}
	// bucket key: metric, scope, key
	type bk struct{ metric, scope, key string }
	buckets := map[bk][]float64{}
	for rows.Next() {
		var metric, device, path string
		var value float64
		if err := rows.Scan(&metric, &device, &path, &value); err != nil {
			rows.Close()
			return err
		}
		if !vitals.Valid(metric) {
			continue
		}
		buckets[bk{metric, "total", ""}] = append(buckets[bk{metric, "total", ""}], value)
		if device != "" {
			buckets[bk{metric, "device", device}] = append(buckets[bk{metric, "device", device}], value)
		}
		if path != "" {
			buckets[bk{metric, "page", path}] = append(buckets[bk{metric, "page", path}], value)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM daily_vitals WHERE site_id = ? AND day = ?`, siteID, dayKey); err != nil {
		return err
	}
	if len(buckets) == 0 {
		return tx.Commit()
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO daily_vitals (site_id, day, metric, scope, key, samples, p75, p50, good, poor) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// A page bucket with a handful of samples produces a percentile nobody
	// should act on, so pages are only kept once there is something to see.
	const minPageSamples = 5
	for k, vs := range buckets {
		if k.scope == "page" && len(vs) < minPageSamples {
			continue
		}
		sort.Float64s(vs)
		good, poor := 0, 0
		for _, v := range vs {
			switch vitals.Rating(k.metric, v) {
			case "good":
				good++
			case "poor":
				poor++
			}
		}
		if _, err := stmt.ExecContext(ctx, siteID, dayKey, k.metric, k.scope, k.key, len(vs),
			percentile(vs, 0.75), percentile(vs, 0.50), good, poor); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// percentile returns the p-th percentile of a sorted slice using the
// nearest-rank method, which is what Google's CrUX reporting uses for p75.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
