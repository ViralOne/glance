// Command glance runs the Glance analytics server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ViralOne/glance/server/internal/alerts"
	"github.com/ViralOne/glance/server/internal/api"
	"github.com/ViralOne/glance/server/internal/auth"
	"github.com/ViralOne/glance/server/internal/config"
	"github.com/ViralOne/glance/server/internal/database"
	"github.com/ViralOne/glance/server/internal/events"
	"github.com/ViralOne/glance/server/internal/favicons"
	"github.com/ViralOne/glance/server/internal/funnels"
	"github.com/ViralOne/glance/server/internal/geo"
	"github.com/ViralOne/glance/server/internal/goals"
	"github.com/ViralOne/glance/server/internal/ids"
	"github.com/ViralOne/glance/server/internal/importer"
	"github.com/ViralOne/glance/server/internal/mailer"
	"github.com/ViralOne/glance/server/internal/notes"
	"github.com/ViralOne/glance/server/internal/polar"
	"github.com/ViralOne/glance/server/internal/ratelimit"
	"github.com/ViralOne/glance/server/internal/revenue"
	"github.com/ViralOne/glance/server/internal/rollup"
	"github.com/ViralOne/glance/server/internal/searchconsole"
	"github.com/ViralOne/glance/server/internal/settings"
	"github.com/ViralOne/glance/server/internal/shares"
	"github.com/ViralOne/glance/server/internal/sites"
	"github.com/ViralOne/glance/server/internal/stats"
	"github.com/ViralOne/glance/server/internal/stripe"
	"github.com/ViralOne/glance/server/internal/tokens"
	"github.com/ViralOne/glance/server/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "glance:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	db, err := database.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DatabasePath, err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	admin := auth.NewAdmin(cfg.AdminUser, cfg.AdminPassword, auth.NewSessionStore(db))
	if cfg.MCPToken != "" {
		log.Info("mcp.enabled", "endpoint", "/mcp")
	}
	if admin.Enabled() {
		log.Info("auth.enabled", "user", cfg.AdminUser)
	} else {
		log.Warn("auth.disabled", "hint", "set GLANCE_ADMIN_USER and GLANCE_ADMIN_PASSWORD to protect the dashboard")
	}

	writer := events.NewWriter(db, log)
	writer.Start(ctx)
	siteStore := sites.New(db)
	fetcher := favicons.New(db)

	st := settings.New(db)
	google := searchconsole.NewService(searchconsole.NewStore(db), searchconsole.NewClient(cfg.GoogleClientID, cfg.GoogleClientSecret), log)
	if google.Configured() {
		log.Info("google.enabled", "callback", "/api/v1/google/callback")
	}
	// Payment providers all write into one orders table.
	domainOf := func(ctx context.Context, id string) string {
		st, err := siteStore.Get(ctx, id)
		if err != nil {
			return ""
		}
		return st.Domain
	}
	revStore := revenue.NewStore(db)
	polarSvc := polar.NewService(revStore, polar.NewClient(), log)
	polarSvc.Domain = domainOf
	stripeSvc := stripe.NewService(revStore, stripe.NewClient(), log)
	stripeSvc.Domain = domainOf

	geoDB := geo.Open(cfg.GeoIPPath, log)
	defer geoDB.Close()

	mail := mailer.New(mailer.Config{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser,
		Password: cfg.SMTPPassword, From: cfg.SMTPFrom, TLS: cfg.SMTPTLS,
	}, log)
	if mail.Configured() {
		log.Info("email.enabled", "host", cfg.SMTPHost, "from", cfg.SMTPFrom)
	}
	statsStore := stats.New(db)
	alertStore := alerts.New(db)
	engine := &alerts.Engine{
		Store: alertStore, Sites: siteStore, Stats: statsStore, Revenue: revStore,
		Notifier: alerts.NewNotifier(mail, log), BaseURL: cfg.BaseURL,
	}

	srv := &api.Server{
		DB: db, Log: log, Sites: siteStore, Settings: st, Writer: writer, Stats: statsStore,
		Favicons: fetcher, Admin: admin, Web: web.Handler(), TrustProxy: true, MCPToken: cfg.MCPToken,
		TrustedProxyHops: cfg.TrustedProxyHops, AllowLocalEvents: cfg.AllowLocalEvents,
		SnippetPath: cfg.SnippetPath, CollectPath: cfg.CollectPath,
		CollectLimiter: ratelimit.New(cfg.CollectPerSecond, cfg.CollectBurst),
		LoginLimiter:   ratelimit.New(loginPerSecond, loginBurst),
		Geo:            geoDB,
		Tokens:         tokens.New(db), RetentionDays: cfg.RetentionDays, RetentionFromEnv: cfg.RetentionDaysSet,
		StartedAt: time.Now(), DatabasePath: cfg.DatabasePath, Google: google,
		Revenue: revStore, Polar: polarSvc, Stripe: stripeSvc,
		Goals: goals.New(db), Funnels: funnels.New(db), Notes: notes.New(db), Shares: shares.New(db),
		Alerts: alertStore, AlertEngine: engine, Mail: mail, Importer: importer.New(db),
	}

	go maintenance(ctx, log, db, siteStore, st, fetcher, cfg)
	go searchConsoleSync(ctx, google)
	go paymentSync(ctx, polarSvc, stripeSvc)
	go alertLoop(ctx, engine)

	httpServer := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	log.Info("server.started", "port", cfg.Port, "database", cfg.DatabasePath, "version", api.Version)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("server.stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	stop()
	writer.Wait()
	// Final rollup so nothing written in the last minutes is lost to the UI.
	_ = rollup.Run(context.Background(), db, log, time.Now())
	return nil
}

// maintenance rolls up every minute, prunes raw events hourly and refreshes
// site favicons weekly.
func maintenance(ctx context.Context, log *slog.Logger, db *sqlDB, siteStore *sites.Store, st *settings.Store, fetcher *favicons.Fetcher, cfg config.Config) {
	roll := func() {
		if err := rollup.Run(ctx, db, log, time.Now()); err != nil && ctx.Err() == nil {
			log.Error("rollup.failed", "error", err.Error())
		}
	}
	prune := func() {
		days := cfg.RetentionDays
		if g, err := st.General(ctx, cfg.RetentionDays, cfg.RetentionDaysSet); err == nil {
			days = g.RetentionDays
		}
		n, err := events.Prune(ctx, db, days, time.Now())
		if err != nil && ctx.Err() == nil {
			log.Error("prune.failed", "error", err.Error())
		} else if n > 0 {
			log.Info("prune.completed", "deleted", n)
		}
		_ = auth.NewSessionStore(db).Prune(ctx, time.Now())
		stale, err := siteStore.StaleFavicons(ctx, ids.Format(time.Now().AddDate(0, 0, -7)))
		if err != nil {
			return
		}
		for _, st := range stale {
			fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			data, ctype, err := fetcher.ForDomain(fctx, st.Domain)
			cancel()
			if err != nil {
				_ = siteStore.SetFavicon(ctx, st.ID, nil, "")
				continue
			}
			_ = siteStore.SetFavicon(ctx, st.ID, data, ctype)
		}
	}
	roll()
	prune()
	rt := time.NewTicker(time.Minute)
	pt := time.NewTicker(time.Hour)
	defer rt.Stop()
	defer pt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.C:
			roll()
		case <-pt.C:
			prune()
		}
	}
}

type sqlDB = database.DB

// loginPerSecond and loginBurst throttle the login form. A person mistypes a
// password a handful of times; a script tries thousands, and the 400ms delay
// on a failure is not on its own enough to make that expensive.
const (
	loginPerSecond = 0.2 // one attempt every five seconds, sustained
	loginBurst     = 10
)

// alertLoop evaluates alert rules every five minutes.
//
// Five minutes is a compromise: fast enough that a spike alert is still news,
// slow enough that a rule with a one-hour window is not re-evaluated on
// essentially the same data twelve times an hour. Each rule's own cooldown
// stops it firing repeatedly while a condition persists.
func alertLoop(ctx context.Context, engine *alerts.Engine) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			engine.Run(ctx)
		}
	}
}

// paymentSync reconciles orders for every connected site once a day, so
// refunds and missed webhooks are picked up whichever processor they came
// through.
func paymentSync(ctx context.Context, polarSvc *polar.Service, stripeSvc *stripe.Service) {
	const maxAge = 20 * time.Hour
	sync := func() {
		polarSvc.SyncStale(ctx, maxAge)
		stripeSvc.SyncStale(ctx, maxAge)
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	sync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sync()
		}
	}
}

// searchConsoleSync pulls search terms for every connected site once a day.
// Google's data trails by a few days, so the cadence is checked hourly but
// each site is refreshed only when its last pull is over 20 hours old.
func searchConsoleSync(ctx context.Context, google *searchconsole.Service) {
	if !google.Configured() {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	google.SyncStale(ctx, 20*time.Hour)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			google.SyncStale(ctx, 20*time.Hour)
		}
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
