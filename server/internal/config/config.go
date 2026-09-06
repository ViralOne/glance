// Package config reads Glance's configuration from environment variables.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is the full server configuration.
type Config struct {
	Port         int
	DatabasePath string
	LogLevel     string

	// Days of raw events to keep. Rollups are kept forever; raw rows only
	// feed today's and yesterday's rebuild, so two days is the floor.
	RetentionDays int
	// RetentionDaysSet is true when GLANCE_RETENTION_DAYS was given; the
	// value then overrides whatever was saved from the settings page.
	RetentionDaysSet bool

	// TrustedProxyHops is how many reverse proxies sit in front of Glance.
	// Each appends the address it saw to X-Forwarded-For, so the real client
	// is the nth entry counting from the right. Anything further left was
	// supplied by the client and must not be trusted: taking the leftmost
	// entry lets a caller forge a new IP per request and inflate uniques.
	// Zero, the default, means read the peer address and ignore the header
	// entirely. Zero is the default deliberately: the shipped compose file
	// publishes the port directly, and trusting one hop when nothing is in
	// front means the header is whatever the caller sent — which is the hole
	// this field exists to close. Set it to 1 when Traefik, Caddy, nginx or
	// Cloudflare terminates in front of Glance.
	TrustedProxyHops int

	// AllowLocalEvents accepts events whose page host looks like a
	// development address (localhost, *.test, a LAN IP) even when the request
	// arrives from a public address. The page host is client-supplied, so
	// this bypasses the site-domain check for every site: only turn it on
	// when the collector is not reachable from the internet.
	AllowLocalEvents bool

	// CollectBurst and CollectPerSecond cap events accepted from one client
	// address. A zero in either disables the limit; a burst of zero with a
	// non-zero rate would otherwise reject everything.
	CollectBurst     int
	CollectPerSecond float64

	// Admin login for the dashboard and admin API.
	//
	// When both are set they pin the credential and it cannot be changed from
	// the dashboard, because the environment would override the change on the
	// next restart. When they are not, the credential lives in the database:
	// generated on first boot and printed once to the log, then changeable from
	// Settings.
	AdminUser     string
	AdminPassword string

	// DisableAuth runs with no login at all. This used to be what happened
	// when the admin variables were merely forgotten, which meant an instance
	// could be public by accident; now it has to be asked for.
	DisableAuth bool

	// MCPToken, when set, is a bearer token that grants read-only access to
	// the MCP endpoint (/mcp). The admin login works there too.
	MCPToken string

	// SnippetPath and CollectPath are where the tracking script and the
	// ingest endpoint are served, in addition to the defaults.
	//
	// This exists because of ad blockers. Filter lists match on URLs, and both
	// "glance.js" and a path ending in "/collect" are the shape they look for;
	// EasyPrivacy blocks generic analytics paths regardless of the host. The
	// single most effective answer is to serve Glance from the same domain as
	// the site it measures, so the request is first-party — a blocker will not
	// break your own domain. Renaming the paths to something that is not
	// obviously analytics closes most of the remaining gap.
	SnippetPath string
	CollectPath string

	// GeoIPPath points at an optional MaxMind-format city database
	// (GeoLite2-City.mmdb or DB-IP City Lite). Without it, location comes
	// from the visitor's time zone and there are no cities.
	GeoIPPath string

	// SMTP delivers digests and email alerts. Without a host, only webhook
	// alerts are available.
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	// SMTPTLS is "starttls" (default), "tls" for implicit TLS, or "none".
	SMTPTLS string
	// BaseURL is Glance's own public URL, used in emails where there is no
	// request to rebuild it from.
	BaseURL string

	// GoogleClientID and GoogleClientSecret are an OAuth client from Google
	// Cloud. Both must be set to offer "Connect Google Search Console".
	GoogleClientID     string
	GoogleClientSecret string
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	c := Config{
		Port:          8080,
		DatabasePath:  env("GLANCE_DATABASE_PATH", "/data/glance.db"),
		LogLevel:      env("GLANCE_LOG_LEVEL", "info"),
		RetentionDays: 30,
		AdminUser:     env("GLANCE_ADMIN_USER", ""),
		AdminPassword: env("GLANCE_ADMIN_PASSWORD", ""),
		MCPToken:      strings.TrimSpace(env("GLANCE_MCP_TOKEN", "")),

		TrustedProxyHops: 0,
		CollectBurst:     120,
		CollectPerSecond: 4,

		SnippetPath: strings.TrimSpace(env("GLANCE_SNIPPET_PATH", "")),
		CollectPath: strings.TrimSpace(env("GLANCE_COLLECT_PATH", "")),

		GeoIPPath: strings.TrimSpace(env("GLANCE_GEOIP_PATH", "")),

		SMTPHost:     strings.TrimSpace(env("GLANCE_SMTP_HOST", "")),
		SMTPPort:     587,
		SMTPUser:     strings.TrimSpace(env("GLANCE_SMTP_USER", "")),
		SMTPPassword: env("GLANCE_SMTP_PASSWORD", ""),
		SMTPFrom:     strings.TrimSpace(env("GLANCE_SMTP_FROM", "")),
		SMTPTLS:      strings.ToLower(strings.TrimSpace(env("GLANCE_SMTP_TLS", "starttls"))),
		BaseURL:      strings.TrimRight(strings.TrimSpace(env("GLANCE_BASE_URL", "")), "/"),

		GoogleClientID:     strings.TrimSpace(env("GLANCE_GOOGLE_CLIENT_ID", "")),
		GoogleClientSecret: strings.TrimSpace(env("GLANCE_GOOGLE_CLIENT_SECRET", "")),
	}
	if v := os.Getenv("GLANCE_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, fmt.Errorf("GLANCE_PORT must be a port number, got %q", v)
		}
		c.Port = p
	}
	if v := os.Getenv("GLANCE_RETENTION_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 2 {
			return c, fmt.Errorf("GLANCE_RETENTION_DAYS must be an integer of at least 2, got %q", v)
		}
		c.RetentionDays = d
		c.RetentionDaysSet = true
	}
	if v := os.Getenv("GLANCE_TRUSTED_PROXY_HOPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 8 {
			return c, fmt.Errorf("GLANCE_TRUSTED_PROXY_HOPS must be an integer between 0 and 8, got %q", v)
		}
		c.TrustedProxyHops = n
	}
	c.AllowLocalEvents = boolEnv("GLANCE_ALLOW_LOCAL_EVENTS")
	c.DisableAuth = boolEnv("GLANCE_DISABLE_AUTH")
	if v := os.Getenv("GLANCE_COLLECT_BURST"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("GLANCE_COLLECT_BURST must be a non-negative integer, got %q", v)
		}
		c.CollectBurst = n
	}
	if v := os.Getenv("GLANCE_COLLECT_PER_SECOND"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			return c, fmt.Errorf("GLANCE_COLLECT_PER_SECOND must be a non-negative number, got %q", v)
		}
		c.CollectPerSecond = f
	}
	if (c.AdminUser == "") != (c.AdminPassword == "") {
		return c, fmt.Errorf("GLANCE_ADMIN_USER and GLANCE_ADMIN_PASSWORD must be set together")
	}
	if c.AdminPassword != "" && len(c.AdminPassword) < 8 {
		return c, fmt.Errorf("GLANCE_ADMIN_PASSWORD must be at least 8 characters")
	}
	if c.DisableAuth && c.AdminUser != "" {
		return c, fmt.Errorf("GLANCE_DISABLE_AUTH cannot be combined with GLANCE_ADMIN_USER; pick one")
	}
	if c.MCPToken != "" && len(c.MCPToken) < 16 {
		return c, fmt.Errorf("GLANCE_MCP_TOKEN must be at least 16 characters")
	}
	if (c.GoogleClientID == "") != (c.GoogleClientSecret == "") {
		return c, fmt.Errorf("GLANCE_GOOGLE_CLIENT_ID and GLANCE_GOOGLE_CLIENT_SECRET must be set together")
	}
	if v := os.Getenv("GLANCE_SMTP_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, fmt.Errorf("GLANCE_SMTP_PORT must be a port number, got %q", v)
		}
		c.SMTPPort = p
	}
	switch c.SMTPTLS {
	case "starttls", "tls", "none":
	default:
		return c, fmt.Errorf("GLANCE_SMTP_TLS must be starttls, tls or none, got %q", c.SMTPTLS)
	}
	if c.SMTPHost != "" && c.SMTPFrom == "" {
		return c, fmt.Errorf("GLANCE_SMTP_FROM must be set when GLANCE_SMTP_HOST is")
	}
	for name, path := range map[string]*string{"GLANCE_SNIPPET_PATH": &c.SnippetPath, "GLANCE_COLLECT_PATH": &c.CollectPath} {
		if *path == "" {
			continue
		}
		// Braces would be read by ServeMux as a wildcard segment and panic at
		// registration, turning a config typo into a failed start-up instead
		// of the clean error every other bad value gets.
		if !strings.HasPrefix(*path, "/") || strings.ContainsAny(*path, "?# {}") || len(*path) > 200 {
			return c, fmt.Errorf("%s must be an absolute path with no query or fragment, got %q", name, *path)
		}
		// Reserving these would let a custom path shadow the dashboard or the
		// admin API, which fails in a way that is hard to diagnose.
		for _, reserved := range []string{"/api/", "/mcp", "/health", "/shared/", "/settings", "/sites"} {
			if *path == strings.TrimSuffix(reserved, "/") || strings.HasPrefix(*path, reserved) {
				return c, fmt.Errorf("%s must not start with %s, which Glance already serves", name, reserved)
			}
		}
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("GLANCE_BASE_URL must be an absolute http or https URL, got %q", c.BaseURL)
		}
	}
	return c, nil
}

// EmailConfigured reports whether digests and email alerts can be delivered.
func (c Config) EmailConfigured() bool { return c.SMTPHost != "" && c.SMTPFrom != "" }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// boolEnv reads a permissive boolean: 1, true, yes and on all mean true.
func boolEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
