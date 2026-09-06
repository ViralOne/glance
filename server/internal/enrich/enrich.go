// Package enrich derives country, device, browser, OS and referrer from a
// request without any external database or service.
package enrich

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Country resolves a country code: a proxy/CDN header first, then the
// visitor's IANA time zone. Returns "" when unknown.
func Country(h http.Header, tz string) string { return CountryWith(h, tz, "") }

// CountryWith is Country with an optional GeoIP answer, which is preferred
// over the time zone but not over an edge header the CDN vouched for.
func CountryWith(h http.Header, tz, geo string) string {
	for _, k := range []string{"CF-IPCountry", "X-Vercel-IP-Country", "X-Country-Code", "X-Geo-Country", "CloudFront-Viewer-Country"} {
		if v := strings.ToUpper(strings.TrimSpace(h.Get(k))); len(v) == 2 && v != "XX" && v != "T1" {
			return v
		}
	}
	if geo = strings.ToUpper(strings.TrimSpace(geo)); len(geo) == 2 {
		return geo
	}
	if cc, ok := tzCountry[strings.TrimSpace(tz)]; ok {
		return cc
	}
	return ""
}

// OptedOut reports whether the request asks not to be measured, via the
// legacy Do Not Track header or the Global Privacy Control signal. Both are
// honoured: they cost nothing and they are the whole point of a
// privacy-first collector.
func OptedOut(h http.Header) bool {
	if strings.TrimSpace(h.Get("DNT")) == "1" {
		return true
	}
	return strings.TrimSpace(h.Get("Sec-GPC")) == "1"
}

// RegionWith prefers a GeoIP region name and falls back to the time zone.
func RegionWith(tz, geo string) string {
	if geo = strings.TrimSpace(geo); geo != "" {
		if len(geo) > 60 {
			geo = geo[:60]
		}
		return geo
	}
	return Region(tz)
}

// Region derives a coarse place name from an IANA time zone: the last path
// segment with underscores as spaces ("America/New_York" -> "New York").
// It is the best location signal available without a GeoIP database.
func Region(tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" || !strings.Contains(tz, "/") {
		return ""
	}
	seg := tz[strings.LastIndex(tz, "/")+1:]
	if seg == "" || strings.HasPrefix(seg, "GMT") || strings.HasPrefix(seg, "UTC") || strings.HasPrefix(seg, "Etc") {
		return ""
	}
	return strings.ReplaceAll(seg, "_", " ")
}

// UTM extracts campaign tags from a page URL. source falls back to the
// common ?ref= and ?source= conventions, and medium to ?utm_medium=.
func UTM(raw string) (source, campaign, medium string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", ""
	}
	q := u.Query()
	source = strings.TrimSpace(q.Get("utm_source"))
	if source == "" {
		source = strings.TrimSpace(q.Get("ref"))
	}
	if source == "" {
		source = strings.TrimSpace(q.Get("source"))
	}
	campaign = strings.TrimSpace(q.Get("utm_campaign"))
	medium = strings.TrimSpace(q.Get("utm_medium"))
	return strings.ToLower(cap80(source)), cap80(campaign), strings.ToLower(cap80(medium))
}

func cap80(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

// maxProps is how many properties are kept on one custom event, and how long
// each key and value may be. Properties are for labelling a goal ("plan":
// "pro"), not for carrying payloads, so the caps are deliberately tight.
const (
	maxProps    = 8
	maxPropLen  = 60
	maxPropsLen = 512
)

// Props normalises the free-form property bag on a custom event into a
// compact JSON object of string values, or "" when there is nothing worth
// keeping. Nested objects and arrays are dropped rather than flattened: a
// breakdown can only group by a scalar.
func Props(raw []byte) string {
	if len(raw) == 0 || len(raw) > 4096 {
		return ""
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil || len(in) == 0 {
		return ""
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	// Stable order so the same bag always serialises identically, which keeps
	// the breakdown key stable across requests.
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if len(out) >= maxProps {
			break
		}
		v, ok := scalar(in[k])
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		if len(key) > maxPropLen {
			key = key[:maxPropLen]
		}
		if len(v) > maxPropLen {
			v = v[:maxPropLen]
		}
		out[key] = v
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil || len(b) > maxPropsLen {
		return ""
	}
	return string(b)
}

// scalar renders a JSON value as a display string, refusing containers.
func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t), true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		// Integers are far more common than fractions in event properties,
		// and "3" reads better than "3.000000" in a breakdown.
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		return "", false
	}
}

// ReferrerAliases turns well-known hosts into the names people expect.
var ReferrerAliases = map[string]string{
	"t.co": "x.com", "twitter.com": "x.com", "l.facebook.com": "facebook.com", "lm.facebook.com": "facebook.com",
	"m.facebook.com": "facebook.com", "com.google.android.gm": "gmail.com", "android-app://com.google.android.googlequicksearchbox": "google.com",
	"www.google.com": "google.com", "www.bing.com": "bing.com", "duckduckgo.com": "duckduckgo.com", "com.linkedin.android": "linkedin.com",
	"www.linkedin.com": "linkedin.com", "old.reddit.com": "reddit.com", "www.reddit.com": "reddit.com", "out.reddit.com": "reddit.com",
	"news.ycombinator.com": "news.ycombinator.com", "www.youtube.com": "youtube.com", "m.youtube.com": "youtube.com",
}

// Referrer returns the referrer host, or "" for direct or same-site traffic.
func Referrer(ref, siteDomain string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if a, ok := ReferrerAliases[host]; ok {
		host = a
	}
	host = strings.TrimPrefix(host, "www.")
	if SameSite(host, siteDomain) {
		return ""
	}
	return host
}

// LocalHost reports whether host is a local development address, which is
// accepted for every site so the snippet can be tested before deploying.
//
// The page host arrives in the request body, so a caller can claim any value.
// This must therefore never be the only check: the collect handler also
// requires the request itself to come from a private client address. See
// PrivateClient.
func LocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "localhost" {
		return true
	}
	if strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".test") {
		return true
	}
	// A literal IP only counts when it really is a private or loopback one;
	// a *name* like "10.example.com" is not a LAN address.
	if ip := net.ParseIP(host); ip != nil {
		return PrivateClient(host)
	}
	return false
}

// PrivateClient reports whether ip is a loopback, link-local, private or
// carrier-grade NAT address, i.e. a client that cannot be on the public
// internet. Unparseable input is treated as public.
func PrivateClient(ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return false
	}
	if p.IsLoopback() || p.IsLinkLocalUnicast() || p.IsLinkLocalMulticast() || p.IsPrivate() || p.IsUnspecified() {
		return true
	}
	// 100.64.0.0/10, carrier-grade NAT, which net.IP does not classify.
	if v4 := p.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// SameSite reports whether host is the site domain or one of its subdomains.
func SameSite(host, domain string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	domain = strings.ToLower(strings.TrimPrefix(domain, "www."))
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// Path extracts a normalised path from a page URL: no query, no fragment,
// trailing slash trimmed, capped in length.
func Path(raw string) (path, host string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "/", ""
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	if len(p) > 200 {
		p = p[:200]
	}
	return p, strings.ToLower(u.Hostname())
}
