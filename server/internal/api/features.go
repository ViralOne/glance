package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ViralOne/glance/server/internal/alerts"
	"github.com/ViralOne/glance/server/internal/funnels"
	"github.com/ViralOne/glance/server/internal/goals"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/importer"
	"github.com/ViralOne/glance/server/internal/notes"
	"github.com/ViralOne/glance/server/internal/shares"
	"github.com/ViralOne/glance/server/internal/sites"
	"github.com/ViralOne/glance/server/internal/stats"
	"github.com/ViralOne/glance/server/internal/vitals"
)

// site resolves the {id} path value, writing the error response itself.
func (s *Server) site(w http.ResponseWriter, r *http.Request) (sites.Site, bool) {
	st, err := s.Sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return sites.Site{}, false
	}
	return st, true
}

// rangeFor resolves and validates the range for a site request.
func (s *Server) rangeFor(w http.ResponseWriter, r *http.Request, st sites.Site) (string, bool) {
	rng := siteRange(r.URL.Query().Get("range"), st)
	if !stats.ValidRange(rng) {
		writeError(w, http.StatusBadRequest, "invalid", "range must be one of "+strings.Join(stats.Ranges, ", "))
		return "", false
	}
	return rng, true
}

// ---- goals ----

func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	rng, ok := s.rangeFor(w, r, st)
	if !ok {
		return
	}
	s.freshen(r.Context())
	// The conversion rate is against the same visitor count the dashboard
	// shows, so the two can never disagree.
	sum, err := s.Stats.Summary(r.Context(), st.ID, rng, s.Now(), 1)
	if err != nil {
		s.fail(w, err)
		return
	}
	results, err := s.Goals.Measure(r.Context(), st.ID, rng, s.Now(), sum.Totals.Visitors)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": rng, "visitors": sum.Totals.Visitors, "goals": results})
}

func (s *Server) createGoal(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in goals.Input
	if !readJSON(w, r, &in) {
		return
	}
	g, err := s.Goals.Create(r.Context(), st.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) updateGoal(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in goals.Input
	if !readJSON(w, r, &in) {
		return
	}
	g, err := s.Goals.Update(r.Context(), st.ID, r.PathValue("goal"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	if err := s.Goals.Delete(r.Context(), st.ID, r.PathValue("goal")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- funnels ----

func (s *Server) listFunnels(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	rng, ok := s.rangeFor(w, r, st)
	if !ok {
		return
	}
	list, err := s.Funnels.List(r.Context(), st.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]funnels.Result, 0, len(list))
	for _, f := range list {
		res, err := s.Funnels.Measure(r.Context(), f, rng, s.Now(), s.retentionDays(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": rng, "funnels": out})
}

func (s *Server) createFunnel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in funnels.Input
	if !readJSON(w, r, &in) {
		return
	}
	f, err := s.Funnels.Create(r.Context(), st.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) updateFunnel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in funnels.Input
	if !readJSON(w, r, &in) {
		return
	}
	f, err := s.Funnels.Update(r.Context(), st.ID, r.PathValue("funnel"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) deleteFunnel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	if err := s.Funnels.Delete(r.Context(), st.ID, r.PathValue("funnel")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- notes ----

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	rng, ok := s.rangeFor(w, r, st)
	if !ok {
		return
	}
	from, to, _ := stats.Window(rng, s.Now())
	list, err := s.Notes.Between(r.Context(), st.ID, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": list})
}

func (s *Server) createNote(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in notes.Input
	if !readJSON(w, r, &in) {
		return
	}
	if in.Day == nil {
		// Annotating "now" is the common case; default to today so the UI can
		// send just the text.
		today := s.Now().UTC().Format("2006-01-02")
		in.Day = &today
	}
	n, err := s.Notes.Create(r.Context(), st.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in notes.Input
	if !readJSON(w, r, &in) {
		return
	}
	n, err := s.Notes.Update(r.Context(), st.ID, r.PathValue("note"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	if err := s.Notes.Delete(r.Context(), st.ID, r.PathValue("note")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Core Web Vitals ----

func (s *Server) siteVitals(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	rng, ok := s.rangeFor(w, r, st)
	if !ok {
		return
	}
	s.freshen(r.Context())
	limit := 10
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	out, err := s.Stats.Vitals(r.Context(), st.ID, rng, s.Now(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vitals": out, "thresholds": vitals.Thresholds})
}

// ---- shared dashboards ----

// shareView adds the absolute URL, which is what the operator wants to copy.
type shareView struct {
	shares.Share
	URL string `json:"url"`
}

func (s *Server) shareView(r *http.Request, sh shares.Share) shareView {
	return shareView{Share: sh, URL: s.absoluteURL(r, "/shared/"+sh.Slug)}
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	list, err := s.Shares.List(r.Context(), st.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]shareView, 0, len(list))
	for _, sh := range list {
		out = append(out, s.shareView(r, sh))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in shares.Input
	// A share with no options is the common case, so an empty body is allowed.
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	sh, err := s.Shares.Create(r.Context(), st.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("share.created", "site", st.ID, "slug", sh.Slug)
	writeJSON(w, http.StatusCreated, s.shareView(r, sh))
}

func (s *Server) updateShare(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	var in shares.Input
	if !readJSON(w, r, &in) {
		return
	}
	sh, err := s.Shares.Update(r.Context(), st.ID, r.PathValue("slug"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.shareView(r, sh))
}

func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	if err := s.Shares.Delete(r.Context(), st.ID, r.PathValue("slug")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sharedStats serves a published dashboard without any login.
//
// This is the only unauthenticated read path in Glance, so it is deliberately
// narrow: one site's aggregates for one range, the site's name and accent for
// presentation, notes and goals because they are part of reading the chart,
// and revenue only when the share was created with it enabled. No filters are
// honoured, because filtered views read raw events and would let a viewer
// probe individual visitor behaviour.
func (s *Server) sharedStats(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	password := r.Header.Get("X-Share-Password")
	if password == "" {
		password = r.URL.Query().Get("password")
	}
	sh, err := s.Shares.Resolve(r.Context(), slug, password)
	switch {
	case errors.Is(err, shares.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such shared dashboard")
		return
	case errors.Is(err, shares.ErrPassword):
		writeError(w, http.StatusUnauthorized, "password_required", shares.ErrPassword.Error())
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	st, err := s.Sites.Get(r.Context(), sh.SiteID)
	if err != nil {
		s.fail(w, err)
		return
	}
	rng := siteRange(r.URL.Query().Get("range"), st)
	if !stats.ValidRange(rng) {
		writeError(w, http.StatusBadRequest, "invalid", "range must be one of "+strings.Join(stats.Ranges, ", "))
		return
	}
	s.freshen(r.Context())
	sum, err := s.Stats.Summary(r.Context(), st.ID, rng, s.Now(), 10)
	if err != nil {
		s.fail(w, err)
		return
	}
	live, _ := s.Stats.LiveVisitors(r.Context(), st.ID, s.Now())
	from, to, _ := stats.Window(rng, s.Now())
	noteList, err := s.Notes.Between(r.Context(), st.ID, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		s.fail(w, err)
		return
	}
	goalList, err := s.Goals.Measure(r.Context(), st.ID, rng, s.Now(), sum.Totals.Visitors)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{
		// Only the presentational fields of the site: not its domain
		// exclusions, not its integrations.
		"site":  map[string]any{"id": st.ID, "name": st.Name, "domain": st.Domain, "accent": st.Accent, "home_country": st.HomeCountry},
		"stats": sum, "live": live, "notes": noteList, "goals": goalList,
		"show_revenue": sh.ShowRevenue,
	}
	if sh.ShowRevenue {
		if conns, err := s.Revenue.ForSite(r.Context(), st.ID); err == nil && len(conns) > 0 {
			if rev, err := s.revenueView(r.Context(), st.ID, rng, 10); err == nil {
				out["revenue"] = rev
			}
		}
	}
	// Rounded to the minute so a dashboard polling every few seconds
	// does not write on every request.
	s.Shares.Touch(r.Context(), slug, ids.Format(s.Now().Truncate(time.Minute)))
	w.Header().Set("Cache-Control", "no-store")
	// A shared dashboard must never be indexed: the slug is the credential.
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	writeJSON(w, http.StatusOK, out)
}

// sharedMeta answers whether a slug exists and whether it needs a password,
// so the public page can show a password prompt instead of an error.
func (s *Server) sharedMeta(w http.ResponseWriter, r *http.Request) {
	sh, err := s.Shares.Resolve(r.Context(), r.PathValue("slug"), "")
	switch {
	case errors.Is(err, shares.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such shared dashboard")
		return
	case errors.Is(err, shares.ErrPassword):
		writeJSON(w, http.StatusOK, map[string]any{"exists": true, "password_required": true})
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": true, "password_required": false, "site": sh.SiteID})
}

// ---- alerts ----

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	list, err := s.Alerts.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts": list,
		// The UI hides the email channel when it cannot be delivered, rather
		// than letting someone save a rule that will only ever fail.
		"email_configured": s.Mail.Configured(),
		"windows":          alerts.Windows,
	})
}

func (s *Server) createAlert(w http.ResponseWriter, r *http.Request) {
	var in alerts.Input
	if !readJSON(w, r, &in) {
		return
	}
	a, err := s.Alerts.Create(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) updateAlert(w http.ResponseWriter, r *http.Request) {
	var in alerts.Input
	if !readJSON(w, r, &in) {
		return
	}
	a, err := s.Alerts.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteAlert(w http.ResponseWriter, r *http.Request) {
	if err := s.Alerts.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testAlert delivers the alert now, whatever its condition, so a webhook URL
// or email address can be proven before it is relied on.
func (s *Server) testAlert(w http.ResponseWriter, r *http.Request) {
	a, err := s.Alerts.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	list, err := s.Sites.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(list) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "add a site before testing an alert")
		return
	}
	site := list[0]
	for _, st := range list {
		if st.ID == a.SiteID {
			site = st
		}
	}
	note, err := s.AlertEngine.Digest(r.Context(), site, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	note.AlertID, note.Kind = a.ID, "test"
	note.Subject = "Glance test: " + note.Subject
	note.Text = "This is a test from Glance; the rule itself has not fired.\n\n" + note.Text
	if err := s.AlertEngine.Notifier.Deliver(r.Context(), a, note); err != nil {
		writeError(w, http.StatusBadGateway, "delivery_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "sent", "channel": a.Channel, "destination": a.Destination})
}

// ---- import ----

// maxImportBytes caps an uploaded export. Three years of daily rollups is a
// few megabytes; the limit is generous enough for a large GA4 dump and small
// enough not to be a memory hazard.
const maxImportBytes = 128 << 20

// importData loads history from another tool into a site.
func (s *Server) importData(w http.ResponseWriter, r *http.Request) {
	st, ok := s.site(w, r)
	if !ok {
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		writeError(w, http.StatusBadRequest, "invalid", "format must be one of "+strings.Join(importer.Formats, ", "))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the upload is too large")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "upload the export file as the request body")
		return
	}
	res, err := s.Importer.Import(r.Context(), st.ID, format, body)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("import.completed", "site", st.ID, "format", format, "days", res.Days, "rows", res.Rows)
	writeJSON(w, http.StatusOK, res)
}
