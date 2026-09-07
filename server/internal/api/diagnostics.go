package api

import "sync/atomic"

// collectDropReason is deliberately closed: diagnostics must never create
// attacker-controlled keys or retain request data.
type collectDropReason uint8

const (
	dropRateLimited collectDropReason = iota
	dropPrivacySignal
	dropInvalidBody
	dropUnknownSite
	dropHostMismatch
	dropLocalExclusion
	dropPathExclusion
	dropIPExclusion
	dropProcessingError
	collectDropReasonCount
)

var collectDropReasonNames = [...]string{
	"rate_limited",
	"privacy_signal",
	"invalid_body",
	"unknown_site",
	"host_mismatch",
	"local_exclusion",
	"path_exclusion",
	"ip_exclusion",
	"processing_error",
}

// CollectorDiagnostics holds process-lifetime collector decisions. These are
// operational counters, not analytics: they contain no site, URL, IP, or user
// data and reset on restart.
type CollectorDiagnostics struct {
	accepted atomic.Uint64
	dropped  atomic.Uint64
	reasons  [collectDropReasonCount]atomic.Uint64
}

// CollectorSnapshot is the admin API representation of collector decisions.
type CollectorSnapshot struct {
	Accepted uint64            `json:"accepted"`
	Dropped  uint64            `json:"dropped"`
	Reasons  map[string]uint64 `json:"reasons"`
}

func (d *CollectorDiagnostics) accept() {
	if d != nil {
		d.accepted.Add(1)
	}
}

func (d *CollectorDiagnostics) drop(reason collectDropReason) {
	if d == nil || reason >= collectDropReasonCount {
		return
	}
	d.dropped.Add(1)
	d.reasons[reason].Add(1)
}

func (d *CollectorDiagnostics) snapshot() CollectorSnapshot {
	out := CollectorSnapshot{Reasons: make(map[string]uint64, collectDropReasonCount)}
	if d == nil {
		for _, name := range collectDropReasonNames {
			out.Reasons[name] = 0
		}
		return out
	}
	out.Accepted = d.accepted.Load()
	out.Dropped = d.dropped.Load()
	for reason, name := range collectDropReasonNames {
		out.Reasons[name] = d.reasons[reason].Load()
	}
	return out
}
