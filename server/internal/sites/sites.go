// Package sites manages the tracked websites.
package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/ViralOne/glance/server/internal/database"
	"github.com/ViralOne/glance/server/internal/enrich"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/stats"
)

// ErrNotFound is returned when a site does not exist.
var ErrNotFound = errors.New("site not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid site")

// Site is a tracked website.
type Site struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	HomeCountry string `json:"home_country"`
	// Accent overrides the account-wide colour on this site's dashboard.
	// Empty follows the global setting.
	Accent string `json:"accent"`
	// DefaultRange is the range this site's dashboard opens on. Empty means
	// the server default.
	DefaultRange string `json:"default_range"`
	HasFavicon   bool   `json:"has_favicon"`
	Position     int    `json:"position"`
	// Domains are extra registrable domains this site accepts events from,
	// beyond Domain and its subdomains. Subdomains never need listing.
	Domains []string `json:"domains"`
	// ExcludePaths are glob patterns whose pageviews are not recorded, one per
	// line, e.g. "/admin/*". ExcludeIPs are addresses or CIDRs to ignore, so
	// your own visits do not register as traffic.
	ExcludePaths []string `json:"exclude_paths"`
	ExcludeIPs   []string `json:"exclude_ips"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// Input is the writable subset of a site.
type Input struct {
	Name         *string   `json:"name"`
	Domain       *string   `json:"domain"`
	HomeCountry  *string   `json:"home_country"`
	Accent       *string   `json:"accent"`
	DefaultRange *string   `json:"default_range"`
	Domains      *[]string `json:"domains"`
	ExcludePaths *[]string `json:"exclude_paths"`
	ExcludeIPs   *[]string `json:"exclude_ips"`
}

// MatchesHost reports whether host may send events for this site: its own
// domain, any subdomain of it, or any of the extra domains (and their
// subdomains). Cross-subdomain tracking therefore needs no configuration;
// only a genuinely different registrable domain does.
func (s Site) MatchesHost(host string) bool {
	if enrich.SameSite(host, s.Domain) {
		return true
	}
	for _, d := range s.Domains {
		if enrich.SameSite(host, d) {
			return true
		}
	}
	return false
}

// ExcludesPath reports whether path matches one of the site's ignore patterns.
// A trailing * matches a prefix, a leading * matches a suffix, and a bare
// pattern must match exactly.
func (s Site) ExcludesPath(path string) bool {
	for _, p := range s.ExcludePaths {
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

// matchGlob is a deliberately tiny matcher: exact, prefix*, *suffix, or
// pre*post. Analytics exclusions do not need a full glob dialect, and a
// small rule set is one people can predict.
func matchGlob(pattern, s string) bool {
	pattern, s = strings.TrimSpace(pattern), strings.TrimSpace(s)
	if pattern == "" {
		return false
	}
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return pattern == s
	}
	pre, post := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(s, pre) || !strings.HasSuffix(s, post) {
		return false
	}
	return len(s) >= len(pre)+len(post)
}

// ExcludesIP reports whether ip is one the site ignores. Entries may be a
// plain address or a CIDR block.
func (s Site) ExcludesIP(ip string) bool {
	if len(s.ExcludeIPs) == 0 {
		return false
	}
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	for _, e := range s.ExcludeIPs {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, block, err := net.ParseCIDR(e); err == nil && block.Contains(addr) {
				return true
			}
			continue
		}
		if other := net.ParseIP(e); other != nil && other.Equal(addr) {
			return true
		}
	}
	return false
}

// Store persists sites and keeps an in-memory index for the ingest path.
type Store struct {
	db *sql.DB

	mu    sync.RWMutex
	byID  map[string]Site
	ready bool
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db, byID: map[string]Site{}} }

const cols = `id, name, domain, home_country, accent, default_range, favicon IS NOT NULL, position, exclude_paths, exclude_ips, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (Site, error) {
	var s Site
	var fav int
	var paths, ips string
	err := row.Scan(&s.ID, &s.Name, &s.Domain, &s.HomeCountry, &s.Accent, &s.DefaultRange, &fav, &s.Position, &paths, &ips, &s.CreatedAt, &s.UpdatedAt)
	s.HasFavicon = fav == 1
	s.ExcludePaths, s.ExcludeIPs = splitLines(paths), splitLines(ips)
	s.Domains = []string{}
	return s, err
}

// splitLines turns the newline-separated storage form into a slice, dropping
// blanks. Always returns a non-nil slice so the JSON is [] rather than null.
func splitLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func joinLines(v []string) string { return strings.Join(v, "\n") }

var domainRe = regexp.MustCompile(`^(localhost|([a-z0-9-]+\.)+[a-z]{2,63})(:\d{1,5})?$`)

// NormaliseDomain lower-cases, strips scheme, path and a leading www.
func NormaliseDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimPrefix(d, "www.")
	if d == "" {
		return "", fmt.Errorf("%w: domain is required", ErrInvalid)
	}
	if !domainRe.MatchString(d) {
		return "", fmt.Errorf("%w: %q is not a valid domain", ErrInvalid, d)
	}
	return d, nil
}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// validAccent accepts an empty string (follow the global accent) or a hex
// colour, stored upper-case like the account-wide one.
func validAccent(a string) (string, error) {
	a = strings.TrimSpace(a)
	if a == "" {
		return "", nil
	}
	if !hexRe.MatchString(a) {
		return "", fmt.Errorf("%w: accent must be a hex colour like #7C83E8", ErrInvalid)
	}
	return strings.ToUpper(a), nil
}

// validRange accepts an empty string (use the server default) or one of the
// ranges the dashboard supports.
func validRange(r string) (string, error) {
	r = strings.TrimSpace(r)
	if r == "" {
		return "", nil
	}
	if !stats.ValidRange(r) {
		return "", fmt.Errorf("%w: default_range must be one of %s", ErrInvalid, strings.Join(stats.Ranges, ", "))
	}
	return r, nil
}

func validCountry(cc string) (string, error) {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if cc == "" {
		return "", nil
	}
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return "", fmt.Errorf("%w: home_country must be a two-letter country code", ErrInvalid)
	}
	return cc, nil
}

// Create inserts a site.
func (s *Store) Create(ctx context.Context, in Input) (Site, error) {
	if in.Domain == nil {
		return Site{}, fmt.Errorf("%w: domain is required", ErrInvalid)
	}
	d, err := NormaliseDomain(*in.Domain)
	if err != nil {
		return Site{}, err
	}
	site := Site{ID: ids.New("site"), Domain: d, Domains: []string{}, ExcludePaths: []string{}, ExcludeIPs: []string{}}
	if in.Name != nil {
		site.Name = strings.TrimSpace(*in.Name)
	}
	if site.Name == "" {
		site.Name = d
	}
	if len(site.Name) > 80 {
		return Site{}, fmt.Errorf("%w: name must be 80 characters or fewer", ErrInvalid)
	}
	if in.HomeCountry != nil {
		if site.HomeCountry, err = validCountry(*in.HomeCountry); err != nil {
			return Site{}, err
		}
	}
	if in.Accent != nil {
		if site.Accent, err = validAccent(*in.Accent); err != nil {
			return Site{}, err
		}
	}
	if in.DefaultRange != nil {
		if site.DefaultRange, err = validRange(*in.DefaultRange); err != nil {
			return Site{}, err
		}
	}
	if in.ExcludePaths != nil {
		if site.ExcludePaths, err = validPatterns(*in.ExcludePaths); err != nil {
			return Site{}, err
		}
	}
	if in.ExcludeIPs != nil {
		if site.ExcludeIPs, err = validIPs(*in.ExcludeIPs); err != nil {
			return Site{}, err
		}
	}
	if in.Domains != nil {
		if site.Domains, err = validDomains(*in.Domains, site.Domain); err != nil {
			return Site{}, err
		}
	}
	now := ids.Now()
	site.CreatedAt, site.UpdatedAt = now, now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM sites`).Scan(&site.Position); err != nil {
		return Site{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sites (id, name, domain, home_country, accent, default_range, exclude_paths, exclude_ips, position, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		site.ID, site.Name, site.Domain, site.HomeCountry, site.Accent, site.DefaultRange,
		joinLines(site.ExcludePaths), joinLines(site.ExcludeIPs), site.Position, site.CreatedAt, site.UpdatedAt)
	if database.IsUniqueViolation(err) {
		return Site{}, fmt.Errorf("%w: %s is already tracked", ErrInvalid, d)
	}
	if err != nil {
		return Site{}, err
	}
	for _, extra := range site.Domains {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_domains (site_id, domain) VALUES (?, ?)`, site.ID, extra); err != nil {
			return Site{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Site{}, err
	}
	s.invalidate()
	return site, nil
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, id string, in Input) (Site, error) {
	site, err := s.Get(ctx, id)
	if err != nil {
		return Site{}, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 80 {
			return Site{}, fmt.Errorf("%w: name must be 1-80 characters", ErrInvalid)
		}
		site.Name = n
	}
	if in.Domain != nil {
		d, err := NormaliseDomain(*in.Domain)
		if err != nil {
			return Site{}, err
		}
		site.Domain = d
	}
	if in.HomeCountry != nil {
		if site.HomeCountry, err = validCountry(*in.HomeCountry); err != nil {
			return Site{}, err
		}
	}
	if in.Accent != nil {
		if site.Accent, err = validAccent(*in.Accent); err != nil {
			return Site{}, err
		}
	}
	if in.DefaultRange != nil {
		if site.DefaultRange, err = validRange(*in.DefaultRange); err != nil {
			return Site{}, err
		}
	}
	if in.ExcludePaths != nil {
		if site.ExcludePaths, err = validPatterns(*in.ExcludePaths); err != nil {
			return Site{}, err
		}
	}
	if in.ExcludeIPs != nil {
		if site.ExcludeIPs, err = validIPs(*in.ExcludeIPs); err != nil {
			return Site{}, err
		}
	}
	if in.Domains != nil {
		if site.Domains, err = validDomains(*in.Domains, site.Domain); err != nil {
			return Site{}, err
		}
	}
	site.UpdatedAt = ids.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE sites SET name=?, domain=?, home_country=?, accent=?, default_range=?, exclude_paths=?, exclude_ips=?, updated_at=? WHERE id=?`,
		site.Name, site.Domain, site.HomeCountry, site.Accent, site.DefaultRange,
		joinLines(site.ExcludePaths), joinLines(site.ExcludeIPs), site.UpdatedAt, site.ID)
	if database.IsUniqueViolation(err) {
		return Site{}, fmt.Errorf("%w: %s is already tracked", ErrInvalid, site.Domain)
	}
	if err != nil {
		return Site{}, err
	}
	if in.Domains != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_domains WHERE site_id = ?`, site.ID); err != nil {
			return Site{}, err
		}
		for _, d := range site.Domains {
			if _, err := tx.ExecContext(ctx, `INSERT INTO site_domains (site_id, domain) VALUES (?, ?)`, site.ID, d); err != nil {
				return Site{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Site{}, err
	}
	s.invalidate()
	return site, nil
}

// maxExtraDomains keeps the ingest-path host check a short loop.
const maxExtraDomains = 20

func validDomains(in []string, own string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{own: true}
	for _, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		d, err := NormaliseDomain(raw)
		if err != nil {
			return nil, err
		}
		if seen[d] {
			continue // the site's own domain, or a duplicate
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) > maxExtraDomains {
		return nil, fmt.Errorf("%w: at most %d extra domains", ErrInvalid, maxExtraDomains)
	}
	sort.Strings(out)
	return out, nil
}

func validPatterns(in []string) ([]string, error) {
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > 200 {
			return nil, fmt.Errorf("%w: exclude patterns must be 200 characters or fewer", ErrInvalid)
		}
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
			return nil, fmt.Errorf("%w: %q must start with / or *", ErrInvalid, p)
		}
		if strings.Count(p, "*") > 1 {
			return nil, fmt.Errorf("%w: %q may contain at most one *", ErrInvalid, p)
		}
		out = append(out, p)
	}
	if len(out) > 50 {
		return nil, fmt.Errorf("%w: at most 50 exclude patterns", ErrInvalid)
	}
	return out, nil
}

func validIPs(in []string) ([]string, error) {
	out := []string{}
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, _, err := net.ParseCIDR(e); err != nil {
				return nil, fmt.Errorf("%w: %q is not a valid CIDR block", ErrInvalid, e)
			}
		} else if net.ParseIP(e) == nil {
			return nil, fmt.Errorf("%w: %q is not a valid IP address", ErrInvalid, e)
		}
		out = append(out, e)
	}
	if len(out) > 50 {
		return nil, fmt.Errorf("%w: at most 50 excluded addresses", ErrInvalid)
	}
	return out, nil
}

// List returns every site in display order.
func (s *Store) List(ctx context.Context) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM sites ORDER BY position ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	out := []Site{}
	for rows.Next() {
		st, err := scan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// One query for every site's extra domains rather than one per site.
	extra, err := s.allDomains(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if d, ok := extra[out[i].ID]; ok {
			out[i].Domains = d
		}
	}
	return out, nil
}

func (s *Store) allDomains(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT site_id, domain FROM site_domains ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, d string
		if err := rows.Scan(&id, &d); err != nil {
			return nil, err
		}
		out[id] = append(out[id], d)
	}
	return out, rows.Err()
}

// Get returns a site by id.
func (s *Store) Get(ctx context.Context, id string) (Site, error) {
	st, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM sites WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, ErrNotFound
	}
	if err != nil {
		return Site{}, err
	}
	st.Domains, err = s.domains(ctx, id)
	return st, err
}

func (s *Store) domains(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain FROM site_domains WHERE site_id = ? ORDER BY domain`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Lookup resolves a site id from the in-memory index; used on the ingest
// path so no query runs per event.
func (s *Store) Lookup(ctx context.Context, id string) (Site, bool) {
	s.mu.RLock()
	if s.ready {
		st, ok := s.byID[id]
		s.mu.RUnlock()
		return st, ok
	}
	s.mu.RUnlock()
	list, err := s.List(ctx)
	if err != nil {
		return Site{}, false
	}
	s.mu.Lock()
	s.byID = map[string]Site{}
	for _, st := range list {
		s.byID[st.ID] = st
	}
	s.ready = true
	st, ok := s.byID[id]
	s.mu.Unlock()
	return st, ok
}

func (s *Store) invalidate() {
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
}

// Reorder sets display positions to follow idList.
func (s *Store) Reorder(ctx context.Context, idList []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET position = position + ?`, len(idList)); err != nil {
		return err
	}
	for i, id := range idList {
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET position = ? WHERE id = ?`, i+1, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// Delete removes a site and all of its data.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM sites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, q := range []string{
		`DELETE FROM events WHERE site_id = ?`, `DELETE FROM hourly_stats WHERE site_id = ?`, `DELETE FROM daily_stats WHERE site_id = ?`,
		`DELETE FROM vitals WHERE site_id = ?`, `DELETE FROM daily_vitals WHERE site_id = ?`,
		`DELETE FROM google_connections WHERE site_id = ?`, `DELETE FROM search_terms WHERE site_id = ?`,
		`DELETE FROM payment_connections WHERE site_id = ?`, `DELETE FROM orders WHERE site_id = ?`,
		`DELETE FROM goals WHERE site_id = ?`, `DELETE FROM funnels WHERE site_id = ?`, `DELETE FROM notes WHERE site_id = ?`,
		`DELETE FROM shares WHERE site_id = ?`, `DELETE FROM site_domains WHERE site_id = ?`, `DELETE FROM alerts WHERE site_id = ?`} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// SetFavicon stores the site's icon bytes.
func (s *Store) SetFavicon(ctx context.Context, id string, data []byte, ctype string) error {
	var blob any
	if len(data) > 0 {
		blob = data
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sites SET favicon=?, favicon_type=?, favicon_at=? WHERE id=?`, blob, ctype, ids.Now(), id)
	s.invalidate()
	return err
}

// Favicon returns the stored icon.
func (s *Store) Favicon(ctx context.Context, id string) (data []byte, ctype string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(favicon, X''), favicon_type FROM sites WHERE id = ?`, id).Scan(&data, &ctype)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return data, ctype, err
}

// StaleFavicons returns sites whose icon is missing or older than `before`.
func (s *Store) StaleFavicons(ctx context.Context, before string) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM sites WHERE favicon_at = '' OR favicon_at < ?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		st, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
