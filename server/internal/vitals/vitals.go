// Package vitals defines the Core Web Vitals Glance records and the
// thresholds Google publishes for them.
//
// Vitals fit Glance's model better than they fit most analytics tools: each
// metric belongs to one page load, so there is nothing to stitch into a
// session and nothing to remember about a visitor. A pageview either carried a
// measurement or it did not.
package vitals

// Metric names, matching the web-vitals vocabulary.
const (
	LCP  = "LCP"  // Largest Contentful Paint, milliseconds
	INP  = "INP"  // Interaction to Next Paint, milliseconds
	CLS  = "CLS"  // Cumulative Layout Shift, unitless, stored x1000
	TTFB = "TTFB" // Time to First Byte, milliseconds
	FCP  = "FCP"  // First Contentful Paint, milliseconds
)

// All is every metric, in the order the dashboard shows them.
var All = []string{LCP, INP, CLS, TTFB, FCP}

// Unit is how a metric's value should be rendered.
func Unit(metric string) string {
	if metric == CLS {
		return "" // unitless score
	}
	return "ms"
}

// Thresholds are Google's "good" and "needs improvement" upper bounds. A
// value at or below Good is good; above Poor's lower edge it is poor. CLS is
// stored multiplied by 1000 so every metric can share one integer column.
var Thresholds = map[string][2]float64{
	LCP:  {2500, 4000},
	INP:  {200, 500},
	CLS:  {100, 250}, // 0.1 and 0.25, x1000
	TTFB: {800, 1800},
	FCP:  {1800, 3000},
}

// Rating buckets a value as good, needs-improvement or poor.
func Rating(metric string, value float64) string {
	t, ok := Thresholds[metric]
	if !ok {
		return ""
	}
	switch {
	case value <= t[0]:
		return "good"
	case value <= t[1]:
		return "needs-improvement"
	default:
		return "poor"
	}
}

// Valid reports whether metric is one Glance records.
func Valid(metric string) bool {
	_, ok := Thresholds[metric]
	return ok
}

// ceilings reject impossible readings. Browsers occasionally report a vital
// from a tab that was backgrounded for hours, and one such sample would drag
// a p75 into nonsense, so anything past these bounds is discarded rather than
// clamped.
var ceilings = map[string]float64{
	LCP:  120000,
	INP:  60000,
	CLS:  10000, // a CLS of 10 is already absurd
	TTFB: 120000,
	FCP:  120000,
}

// Plausible reports whether value is a believable reading for metric.
func Plausible(metric string, value float64) bool {
	max, ok := ceilings[metric]
	if !ok {
		return false
	}
	return value >= 0 && value <= max
}
