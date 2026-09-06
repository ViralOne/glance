package config

import (
	"strings"
	"testing"
)

// set applies environment variables for one test and restores them after.
func set(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Zero trusted hops is the deliberate default: the shipped compose file
	// publishes the port directly, and trusting a hop that is not there means
	// believing whatever X-Forwarded-For the caller sent.
	if cfg.TrustedProxyHops != 0 {
		t.Errorf("TrustedProxyHops should default to 0, got %d", cfg.TrustedProxyHops)
	}
	// Thirty days, not seven: filters and funnels read raw events, and a week
	// is short enough that a 30d filtered view is mostly empty.
	if cfg.RetentionDays != 30 {
		t.Errorf("RetentionDays should default to 30, got %d", cfg.RetentionDays)
	}
	if cfg.AllowLocalEvents {
		t.Error("AllowLocalEvents must default to off; it bypasses the site-domain check")
	}
	if cfg.EmailConfigured() {
		t.Error("email should not be configured by default")
	}
}

func TestSnippetAndCollectPaths(t *testing.T) {
	for _, bad := range []string{
		"relative.js",                  // not a path
		"/api/v1/thing",                // shadows the admin API
		"/mcp",                         // shadows the MCP endpoint
		"/health",                      // shadows the health check
		"/shared/x",                    // shadows a public share
		"/a?b=1",                       // query
		"/a#b",                         // fragment
		"/a b",                         // space
		"/a{id}.js",                    // ServeMux reads braces as a wildcard and panics
		"/" + strings.Repeat("x", 300), // absurd
	} {
		t.Run(bad, func(t *testing.T) {
			set(t, map[string]string{"GLANCE_SNIPPET_PATH": bad})
			if _, err := Load(); err == nil {
				t.Errorf("%q should be refused at load, not at route registration", bad)
			}
		})
	}
	for _, ok := range []string{"/js/app.js", "/i", "/static/a/b/c.js"} {
		t.Run(ok, func(t *testing.T) {
			set(t, map[string]string{"GLANCE_SNIPPET_PATH": ok, "GLANCE_COLLECT_PATH": ok + "x"})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("%q should be accepted: %v", ok, err)
			}
			if cfg.SnippetPath != ok {
				t.Errorf("got %q", cfg.SnippetPath)
			}
		})
	}
}

func TestProxyHopsBounds(t *testing.T) {
	for _, bad := range []string{"-1", "9", "many", ""} {
		if bad == "" {
			continue
		}
		set(t, map[string]string{"GLANCE_TRUSTED_PROXY_HOPS": bad})
		if _, err := Load(); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	set(t, map[string]string{"GLANCE_TRUSTED_PROXY_HOPS": "2"})
	cfg, err := Load()
	if err != nil || cfg.TrustedProxyHops != 2 {
		t.Fatalf("got %d, %v", cfg.TrustedProxyHops, err)
	}
}

func TestSMTPValidation(t *testing.T) {
	set(t, map[string]string{"GLANCE_SMTP_HOST": "smtp.example.com"})
	if _, err := Load(); err == nil {
		t.Error("a host with no from address should be refused, since every send would fail")
	}
	set(t, map[string]string{"GLANCE_SMTP_HOST": "smtp.example.com", "GLANCE_SMTP_FROM": "glance@example.com"})
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EmailConfigured() || cfg.SMTPPort != 587 || cfg.SMTPTLS != "starttls" {
		t.Errorf("got %+v", cfg)
	}
	set(t, map[string]string{"GLANCE_SMTP_HOST": "smtp.example.com", "GLANCE_SMTP_FROM": "a@b.c", "GLANCE_SMTP_TLS": "sslv3"})
	if _, err := Load(); err == nil {
		t.Error("an unknown TLS mode should be refused")
	}
}

func TestBaseURLValidation(t *testing.T) {
	for _, bad := range []string{"example.com", "ftp://example.com", "/relative"} {
		set(t, map[string]string{"GLANCE_BASE_URL": bad})
		if _, err := Load(); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	set(t, map[string]string{"GLANCE_BASE_URL": "https://glance.example.com/"})
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://glance.example.com" {
		t.Errorf("the trailing slash should be trimmed: %q", cfg.BaseURL)
	}
}
