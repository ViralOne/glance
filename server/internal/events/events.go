// Package events buffers incoming events and writes them to SQLite in
// batches, so the request path never touches the database.
package events

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ViralOne/glance/server/internal/ids"
)

// Kinds.
const (
	KindPageview = "pageview"
	KindEvent    = "event"
	// KindBot is a crawler hit. Bots are never counted as visitors, but
	// recording them lets the dashboard show which search engines and which
	// AI crawlers read the site.
	KindBot = "bot"
)

// Event is one enriched, anonymous hit.
type Event struct {
	SiteID    string
	At        time.Time
	Kind      string
	Name      string
	Path      string
	RefHost   string
	Country   string
	Device    string
	Browser   string
	OS        string
	Region    string
	City      string
	UTMSrc    string
	UTMCamp   string
	UTMMedium string
	Visitor   string
	// Props is a small JSON object of custom event properties, "" when none.
	Props string
	// Value is what a custom event was worth, in minor units (cents).
	Value int64
}

// Vital is one Core Web Vitals reading from one page load.
type Vital struct {
	SiteID string
	At     time.Time
	Path   string
	Device string
	Metric string
	Value  float64
}

// VisitorHash derives the day-scoped anonymous visitor id. The IP and user
// agent go in, only a truncated hash comes out, and the salt changes daily.
func VisitorHash(salt, siteID, ip, ua string) string {
	sum := sha256.Sum256([]byte(salt + "|" + siteID + "|" + ip + "|" + ua))
	return hex.EncodeToString(sum[:8])
}

const (
	bufferSize    = 4096
	flushEvery    = time.Second
	flushAtCount  = 200
	dropLogPeriod = time.Minute
)

// Writer batches events into the database.
type Writer struct {
	db  *sql.DB
	log *slog.Logger
	ch  chan Event
	vch chan Vital
	wg  sync.WaitGroup

	dropped  atomic.Int64
	lastDrop atomic.Int64 // unix seconds
	Written  atomic.Int64
	flushReq chan chan error
	running  atomic.Bool
}

// NewWriter returns a Writer; call Start to begin flushing.
func NewWriter(db *sql.DB, log *slog.Logger) *Writer {
	return &Writer{db: db, log: log, ch: make(chan Event, bufferSize), vch: make(chan Vital, bufferSize), flushReq: make(chan chan error)}
}

// Enqueue adds an event without blocking. When the buffer is full the event
// is dropped and counted.
func (w *Writer) Enqueue(e Event) {
	select {
	case w.ch <- e:
	default:
		w.drop()
	}
}

// EnqueueVital adds a Core Web Vitals reading. Vitals are the first thing to
// go under load: losing a sample only widens a percentile's error bar, while
// losing a pageview changes a number someone is looking at.
func (w *Writer) EnqueueVital(v Vital) {
	select {
	case w.vch <- v:
	default:
		w.drop()
	}
}

func (w *Writer) drop() {
	n := w.dropped.Add(1)
	now := time.Now().Unix()
	if last := w.lastDrop.Load(); now-last >= int64(dropLogPeriod.Seconds()) && w.lastDrop.CompareAndSwap(last, now) {
		w.log.Warn("events.dropped", "total", n)
	}
}

// Start runs the flush loop until ctx is cancelled, then drains what is left.
func (w *Writer) Start(ctx context.Context) {
	w.wg.Add(1)
	w.running.Store(true)
	go func() {
		defer w.wg.Done()
		defer w.running.Store(false)
		b := &batch{events: make([]Event, 0, flushAtCount)}
		t := time.NewTicker(flushEvery)
		defer t.Stop()
		flush := func() error {
			err := w.writeBatch(b)
			if err != nil {
				w.log.Error("events.write_failed", "count", b.len(), "error", err.Error())
			}
			b.reset()
			return err
		}
		drain := func() {
			for {
				select {
				case e := <-w.ch:
					b.events = append(b.events, e)
				case v := <-w.vch:
					b.vitals = append(b.vitals, v)
				default:
					return
				}
			}
		}
		for {
			select {
			case reply := <-w.flushReq:
				// Take everything queued into the batch, then write it all.
				drain()
				reply <- flush()
			case <-ctx.Done():
				drain()
				flush()
				return
			case e := <-w.ch:
				b.events = append(b.events, e)
				if b.len() >= flushAtCount {
					flush()
				}
			case v := <-w.vch:
				b.vitals = append(b.vitals, v)
				if b.len() >= flushAtCount {
					flush()
				}
			case <-t.C:
				flush()
			}
		}
	}()
}

// batch is one transaction's worth of pending writes.
type batch struct {
	events []Event
	vitals []Vital
}

func (b *batch) len() int { return len(b.events) + len(b.vitals) }

func (b *batch) reset() {
	b.events = b.events[:0]
	b.vitals = b.vitals[:0]
}

// Wait blocks until the flush loop has exited.
func (w *Writer) Wait() { w.wg.Wait() }

// Flush writes everything queued right now, including events the loop has
// already taken into its pending batch. Works with or without Start.
func (w *Writer) Flush() error {
	if w.running.Load() {
		reply := make(chan error, 1)
		select {
		case w.flushReq <- reply:
			return <-reply
		case <-time.After(5 * time.Second):
			return context.DeadlineExceeded
		}
	}
	b := &batch{}
	for {
		select {
		case e := <-w.ch:
			b.events = append(b.events, e)
		case v := <-w.vch:
			b.vitals = append(b.vitals, v)
		default:
			return w.writeBatch(b)
		}
	}
}

func (w *Writer) writeBatch(b *batch) error {
	if b.len() == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(b.events) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO events (site_id, ts, kind, name, path, ref_host, country, device, browser, os, region, city, utm_source, utm_campaign, utm_medium, visitor, props, value)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		for _, e := range b.events {
			if _, err := stmt.ExecContext(ctx, e.SiteID, ids.Format(e.At), e.Kind, e.Name, e.Path, e.RefHost, e.Country,
				e.Device, e.Browser, e.OS, e.Region, e.City, e.UTMSrc, e.UTMCamp, e.UTMMedium, e.Visitor, e.Props, e.Value); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()
	}
	if len(b.vitals) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO vitals (site_id, ts, path, device, metric, value) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		for _, v := range b.vitals {
			if _, err := stmt.ExecContext(ctx, v.SiteID, ids.Format(v.At), v.Path, v.Device, v.Metric, v.Value); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.Written.Add(int64(b.len()))
	return nil
}

// Dropped returns how many events were discarded because the buffer was full.
func (w *Writer) Dropped() int64 { return w.dropped.Load() }

// Prune deletes raw events and vitals older than days. Rollups are kept
// forever, so this only shortens how far filtered and funnel views reach.
func Prune(ctx context.Context, db *sql.DB, days int, now time.Time) (int64, error) {
	cutoff := ids.Format(now.AddDate(0, 0, -days))
	res, err := db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	vres, err := db.ExecContext(ctx, `DELETE FROM vitals WHERE ts < ?`, cutoff)
	if err != nil {
		return n, err
	}
	vn, _ := vres.RowsAffected()
	return n + vn, nil
}
