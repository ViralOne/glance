package stats

import (
	"context"
	"sort"
	"time"

	"github.com/ViralOne/glance/server/internal/vitals"
)

// VitalRow is one metric's percentiles over a window.
type VitalRow struct {
	Metric  string  `json:"metric"`
	Unit    string  `json:"unit"`
	Samples int     `json:"samples"`
	P75     float64 `json:"p75"`
	P50     float64 `json:"p50"`
	// Rating buckets P75 as good, needs-improvement or poor, using Google's
	// published thresholds.
	Rating string `json:"rating"`
	// GoodPct is the share of samples rated good, which is what Google's own
	// reporting leads with.
	GoodPct float64 `json:"good_pct"`
	PoorPct float64 `json:"poor_pct"`
}

// VitalScope is one cut of the vitals: overall, by device, or by page.
type VitalScope struct {
	Key  string     `json:"key"`
	Rows []VitalRow `json:"rows"`
}

// Vitals is the Core Web Vitals payload for one site and range.
type Vitals struct {
	Range string `json:"range"`
	// Overall is the site-wide reading per metric.
	Overall []VitalRow `json:"overall"`
	// Devices and Pages are the same metrics cut by device and by path.
	Devices []VitalScope `json:"devices"`
	Pages   []VitalScope `json:"pages"`
}

// Vitals reads the daily percentile rollups over a range.
//
// Percentiles do not add, so a window's p75 cannot be summed from daily p75s.
// Rather than store every sample forever, Glance reports the sample-weighted
// mean of the daily p75s: with a stable site that is within noise of the true
// figure, and it is stated as such rather than presented as an exact
// percentile. The good/poor shares, being plain counts, are exact.
func (s *Store) Vitals(ctx context.Context, siteID, rng string, now time.Time, topPages int) (Vitals, error) {
	from, to, _ := Window(rng, now)
	fromDay, toDay := from.UTC().Format("2006-01-02"), to.Add(-time.Second).UTC().Format("2006-01-02")
	out := Vitals{Range: rng, Overall: []VitalRow{}, Devices: []VitalScope{}, Pages: []VitalScope{}}

	rows, err := s.db.QueryContext(ctx, `SELECT scope, key, metric,
			SUM(samples),
			CASE WHEN SUM(samples) > 0 THEN SUM(p75 * samples) / SUM(samples) ELSE 0 END,
			CASE WHEN SUM(samples) > 0 THEN SUM(p50 * samples) / SUM(samples) ELSE 0 END,
			SUM(good), SUM(poor)
		FROM daily_vitals WHERE site_id = ? AND day >= ? AND day <= ?
		GROUP BY scope, key, metric`, siteID, fromDay, toDay)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	// scope -> key -> metric
	got := map[string]map[string]map[string]VitalRow{}
	samplesByKey := map[string]map[string]int{}
	for rows.Next() {
		var scope, key, metric string
		var samples, good, poor int
		var p75, p50 float64
		if err := rows.Scan(&scope, &key, &metric, &samples, &p75, &p50, &good, &poor); err != nil {
			return out, err
		}
		row := VitalRow{Metric: metric, Unit: vitals.Unit(metric), Samples: samples, P75: round(p75), P50: round(p50),
			Rating: vitals.Rating(metric, p75)}
		if samples > 0 {
			row.GoodPct = round(float64(good) / float64(samples) * 100)
			row.PoorPct = round(float64(poor) / float64(samples) * 100)
		}
		if got[scope] == nil {
			got[scope] = map[string]map[string]VitalRow{}
			samplesByKey[scope] = map[string]int{}
		}
		if got[scope][key] == nil {
			got[scope][key] = map[string]VitalRow{}
		}
		got[scope][key][metric] = row
		samplesByKey[scope][key] += samples
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	out.Overall = ordered(got["total"][""])
	out.Devices = scopes(got["device"], samplesByKey["device"], 0)
	out.Pages = scopes(got["page"], samplesByKey["page"], topPages)
	return out, nil
}

// ordered returns the metrics in the dashboard's order, skipping absent ones.
func ordered(byMetric map[string]VitalRow) []VitalRow {
	out := []VitalRow{}
	for _, m := range vitals.All {
		if r, ok := byMetric[m]; ok {
			out = append(out, r)
		}
	}
	return out
}

// scopes turns a scope's map into a slice ordered by sample count, so the
// busiest device or page leads. limit of 0 means no limit.
func scopes(byKey map[string]map[string]VitalRow, samples map[string]int, limit int) []VitalScope {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	// Descending by samples, then by key so the order never wobbles between
	// requests for keys with equal counts.
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if samples[a] != samples[b] {
			return samples[a] > samples[b]
		}
		return a < b
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]VitalScope, 0, len(keys))
	for _, k := range keys {
		out = append(out, VitalScope{Key: k, Rows: ordered(byKey[k])})
	}
	return out
}

func round(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
