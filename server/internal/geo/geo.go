// Package geo resolves a city and region from an IP address using an
// optional MaxMind-format database.
//
// This is the one place Glance will read a third-party data file, and it stays
// optional for a reason: without it, location comes from the visitor's time
// zone, which is honest but country-shaped ("Europe/London" for all of the
// UK). Point GLANCE_GEOIP_PATH at a GeoLite2-City.mmdb or DB-IP City Lite file
// and the same request answers with a real city. Nothing is fetched at
// runtime; if the file is missing or unreadable, Glance says so once and falls
// back to the time zone.
package geo

import (
	"log/slog"
	"net/netip"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Place is what a lookup yields. Every field may be empty.
type Place struct {
	Country string // ISO 3166-1 alpha-2
	Region  string // first subdivision, e.g. "England"
	City    string
}

// Reader looks up addresses. A nil Reader answers with an empty Place, so
// callers never need to check whether GeoIP is configured.
type Reader struct {
	db *maxminddb.Reader

	mu    sync.Mutex
	cache map[netip.Addr]Place
}

// cacheMax bounds the lookup cache. Visitors cluster, so a small cache
// absorbs most of the work; when it fills it is dropped wholesale rather than
// evicted one entry at a time, which costs one extra lookup per visitor and
// saves keeping an LRU.
const cacheMax = 8192

// Open reads the database at path. An empty path returns a nil Reader.
func Open(path string, log *slog.Logger) *Reader {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		log.Warn("geoip.unavailable", "path", path, "error", err.Error(),
			"hint", "location will fall back to the visitor's time zone")
		return nil
	}
	log.Info("geoip.enabled", "path", path, "build", db.Metadata.BuildEpoch, "type", db.Metadata.DatabaseType)
	return &Reader{db: db, cache: map[netip.Addr]Place{}}
}

// Close releases the database.
func (r *Reader) Close() {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Close()
}

// record is the subset of the City schema Glance reads. Both GeoLite2-City and
// DB-IP City Lite use these paths.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

// Lookup resolves ip. Unparseable, private and unknown addresses give an
// empty Place.
func (r *Reader) Lookup(ip string) Place {
	if r == nil || r.db == nil {
		return Place{}
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return Place{}
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() {
		return Place{}
	}
	r.mu.Lock()
	if p, ok := r.cache[addr]; ok {
		r.mu.Unlock()
		return p
	}
	r.mu.Unlock()

	var rec record
	if err := r.db.Lookup(addr).Decode(&rec); err != nil {
		return Place{}
	}
	p := Place{Country: strings.ToUpper(rec.Country.ISOCode), City: name(rec.City.Names)}
	if len(rec.Subdivisions) > 0 {
		p.Region = name(rec.Subdivisions[0].Names)
	}

	r.mu.Lock()
	if len(r.cache) >= cacheMax {
		r.cache = map[netip.Addr]Place{}
	}
	r.cache[addr] = p
	r.mu.Unlock()
	return p
}

// name prefers the English label, falling back to whichever locale sorts
// first so a non-English database still yields something readable — and
// yields the *same* something on every lookup, which map iteration would not.
func name(names map[string]string) string {
	if n, ok := names["en"]; ok {
		return n
	}
	best := ""
	for locale, n := range names {
		if best == "" || locale < best {
			best = locale
		}
		_ = n
	}
	return names[best]
}
