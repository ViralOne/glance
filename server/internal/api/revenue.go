package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ViralOne/glance/server/internal/polar"
	"github.com/ViralOne/glance/server/internal/revenue"
	"github.com/ViralOne/glance/server/internal/stats"
	"github.com/ViralOne/glance/server/internal/stripe"
)

// absoluteURL rebuilds a public URL for this server from the request, so the
// webhook and OAuth URLs shown in settings match whatever host the proxy
// serves rather than the port the process bound.
func (s *Server) absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if s.TrustProxy {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = strings.ToLower(strings.TrimSpace(strings.Split(p, ",")[0]))
		}
	}
	return scheme + "://" + r.Host + path
}

func (s *Server) webhookURL(r *http.Request, provider, siteID string) string {
	return s.absoluteURL(r, "/api/v1/payments/"+provider+"/webhook/"+siteID)
}

// paymentStatus is one provider's connection state for a site.
type paymentStatus struct {
	Provider   string              `json:"provider"`
	Connected  bool                `json:"connected"`
	Connection *revenue.Connection `json:"connection,omitempty"`
	WebhookURL string              `json:"webhook_url"`
}

// paymentsView is every provider's state plus the shared order count.
type paymentsView struct {
	Providers []paymentStatus `json:"providers"`
	Orders    int             `json:"orders"`
}

func (s *Server) writePaymentsStatus(w http.ResponseWriter, r *http.Request, siteID string) {
	out := paymentsView{Providers: []paymentStatus{}}
	conns, err := s.Revenue.ForSite(r.Context(), siteID)
	if err != nil {
		s.fail(w, err)
		return
	}
	byProvider := map[string]revenue.Connection{}
	for _, c := range conns {
		byProvider[c.Provider] = c
	}
	for _, p := range revenue.Providers {
		st := paymentStatus{Provider: p, WebhookURL: s.webhookURL(r, p, siteID)}
		if c, ok := byProvider[p]; ok {
			st.Connected = true
			st.Connection = &c
		}
		out.Providers = append(out.Providers, st)
	}
	if out.Orders, err = s.Revenue.Count(r.Context(), siteID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) paymentsGet(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writePaymentsStatus(w, r, st.ID)
}

// provider resolves the {provider} path segment.
func provider(r *http.Request) (string, bool) {
	p := strings.ToLower(r.PathValue("provider"))
	return p, revenue.ValidProvider(p)
}

// paymentsConnect saves a provider's token and settings.
func (s *Server) paymentsConnect(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	p, ok := provider(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown payment provider")
		return
	}
	var in revenue.Input
	if !readJSON(w, r, &in) {
		return
	}
	switch p {
	case revenue.Polar:
		_, err = s.Polar.Connect(r.Context(), st.ID, in)
	case revenue.Stripe:
		_, err = s.Stripe.Connect(r.Context(), st.ID, in)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writePaymentsStatus(w, r, st.ID)
}

func (s *Server) paymentsDisconnect(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	p, ok := provider(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown payment provider")
		return
	}
	if err := s.Revenue.Delete(r.Context(), st.ID, p); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) paymentsSync(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	p, ok := provider(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown payment provider")
		return
	}
	switch p {
	case revenue.Polar:
		err = s.Polar.Sync(r.Context(), st.ID, st.Domain)
	case revenue.Stripe:
		err = s.Stripe.Sync(r.Context(), st.ID, st.Domain)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writePaymentsStatus(w, r, st.ID)
}

// paymentsWebhook receives a provider's events. Public by necessity; the
// signature against the site's saved secret is the authentication.
func (s *Server) paymentsWebhook(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	p, ok := provider(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown payment provider")
		return
	}
	c, err := s.Revenue.Get(r.Context(), st.ID, p)
	if err != nil {
		s.fail(w, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body must be 1 MB or smaller")
		return
	}
	var kind string
	switch p {
	case revenue.Polar:
		if err = polar.VerifyWebhook(c.WebhookSecret, r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"),
			r.Header.Get("webhook-signature"), body, s.Now()); err == nil {
			kind, err = s.Polar.HandleWebhook(r.Context(), c, st.Domain, body)
		}
	case revenue.Stripe:
		if err = stripe.VerifyWebhook(c.WebhookSecret, r.Header.Get("Stripe-Signature"), body, s.Now()); err == nil {
			kind, err = s.Stripe.HandleWebhook(r.Context(), c, st.Domain, body)
		}
	}
	if errors.Is(err, polar.ErrSignature) || errors.Is(err, stripe.ErrSignature) {
		s.Log.Warn("payments.webhook_rejected", "site", st.ID, "provider", p, "error", err.Error())
		writeError(w, http.StatusUnauthorized, "signature", err.Error())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("payments.webhook", "site", st.ID, "provider", p, "type", kind)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "type": kind})
}

// Revenue is the revenue view for a range.
type Revenue struct {
	Range      string                   `json:"range"`
	Currency   string                   `json:"currency"`
	Totals     revenue.Totals           `json:"totals"`
	Previous   revenue.Totals           `json:"previous"`
	Series     []revenue.Point          `json:"series"`
	Breakdowns map[string][]revenue.Row `json:"breakdowns"`
}

func (s *Server) siteRevenue(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	q := r.URL.Query()
	rng := siteRange(q.Get("range"), st)
	if !stats.ValidRange(rng) {
		writeError(w, http.StatusBadRequest, "invalid", "range must be one of "+strings.Join(stats.Ranges, ", "))
		return
	}
	limit := 10
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	conns, err := s.Revenue.ForSite(r.Context(), st.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(conns) == 0 {
		s.fail(w, revenue.ErrNotConnected)
		return
	}
	rev, err := s.revenueView(r.Context(), st.ID, rng, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (s *Server) revenueView(ctx context.Context, siteID, rng string, limit int) (Revenue, error) {
	from, to, bucket := stats.Window(rng, s.Now())
	out := Revenue{Range: rng, Breakdowns: map[string][]revenue.Row{}}
	var err error
	if out.Currency, err = s.Revenue.Currency(ctx, siteID); err != nil {
		return out, err
	}
	if out.Series, err = s.Revenue.Series(ctx, siteID, from, to, bucket); err != nil {
		return out, err
	}
	if out.Totals, err = s.Revenue.Totals(ctx, siteID, from, to); err != nil {
		return out, err
	}
	if out.Previous, err = s.Revenue.Totals(ctx, siteID, from.Add(-to.Sub(from)), from); err != nil {
		return out, err
	}
	for _, dim := range revenue.Dims {
		if out.Breakdowns[dim], err = s.Revenue.Breakdown(ctx, siteID, dim, from, to, limit); err != nil {
			return out, err
		}
	}
	return out, nil
}
