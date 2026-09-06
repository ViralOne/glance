-- New event columns.
--
-- city comes from an optional GeoIP database; region already held the time
-- zone's city name, which is a country-sized approximation, so the two are
-- kept apart rather than one overwriting the other.
ALTER TABLE events ADD COLUMN city TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN utm_medium TEXT NOT NULL DEFAULT '';
-- props is a small JSON object of custom event properties, '' when none.
ALTER TABLE events ADD COLUMN props TEXT NOT NULL DEFAULT '';
-- value is what a custom event was worth, in minor units (cents).
ALTER TABLE events ADD COLUMN value INTEGER NOT NULL DEFAULT 0;

-- Funnels and journeys read raw events by visitor, which the (site_id, ts)
-- index cannot serve.
CREATE INDEX events_site_visitor_ts ON events (site_id, visitor, ts);

-- Summed event value in minor units, so a goal's worth survives the pruning
-- of the raw events it came from.
ALTER TABLE daily_stats ADD COLUMN value INTEGER NOT NULL DEFAULT 0;

-- Core Web Vitals. One row per metric per page load; percentiles are rolled
-- up daily and the raw rows are pruned with the events.
CREATE TABLE vitals (
    id      INTEGER PRIMARY KEY,
    site_id TEXT NOT NULL,
    ts      TEXT NOT NULL,
    path    TEXT NOT NULL DEFAULT '',
    device  TEXT NOT NULL DEFAULT '',
    metric  TEXT NOT NULL,              -- LCP | INP | CLS | TTFB | FCP
    value   REAL NOT NULL               -- ms, or CLS x1000
);
CREATE INDEX vitals_site_ts ON vitals (site_id, ts);

-- Daily p75 per metric, per device, and per path. key is '' for the
-- site-wide row, the device name for a device row, or the path for a page row,
-- distinguished by scope.
CREATE TABLE daily_vitals (
    site_id TEXT NOT NULL,
    day     TEXT NOT NULL,              -- YYYY-MM-DD (UTC)
    metric  TEXT NOT NULL,
    scope   TEXT NOT NULL,              -- total | device | page
    key     TEXT NOT NULL,
    samples INTEGER NOT NULL DEFAULT 0,
    p75     REAL NOT NULL DEFAULT 0,
    p50     REAL NOT NULL DEFAULT 0,
    good    INTEGER NOT NULL DEFAULT 0, -- samples rated good
    poor    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (site_id, day, metric, scope, key)
);

-- Goals: a custom event (or a page path) that counts as a conversion, with a
-- conversion rate against visitors.
CREATE TABLE goals (
    id         TEXT PRIMARY KEY,
    site_id    TEXT NOT NULL,
    name       TEXT NOT NULL,              -- display name
    kind       TEXT NOT NULL,              -- event | path
    target     TEXT NOT NULL,              -- event name, or path (prefix with * to match a prefix)
    value      INTEGER NOT NULL DEFAULT 0, -- assumed worth in cents when the event carries none
    position   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE (site_id, kind, target)
);

-- Funnels: an ordered list of steps, each an event name or a path, stored as
-- JSON because the list is short, read whole, and never queried by step.
CREATE TABLE funnels (
    id         TEXT PRIMARY KEY,
    site_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    steps      TEXT NOT NULL,              -- JSON array of {name, kind, target}
    position   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

-- Chart annotations: "we launched on Product Hunt here".
CREATE TABLE notes (
    id         TEXT PRIMARY KEY,
    site_id    TEXT NOT NULL,
    day        TEXT NOT NULL,              -- YYYY-MM-DD (UTC)
    text       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX notes_site_day ON notes (site_id, day);

-- Public read-only dashboards. A share is addressed by an unguessable slug;
-- an optional password hash gates it further.
CREATE TABLE shares (
    slug          TEXT PRIMARY KEY,
    site_id       TEXT NOT NULL,
    password_hash TEXT NOT NULL DEFAULT '',  -- '' = no password
    show_revenue  INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX shares_site ON shares (site_id);

-- Extra domains a site accepts events from, one per row, for apps split
-- across more than one registrable domain.
CREATE TABLE site_domains (
    site_id TEXT NOT NULL,
    domain  TEXT NOT NULL,
    PRIMARY KEY (site_id, domain)
);

-- Per-site collection exclusions: paths to ignore and IPs (or CIDRs) to
-- ignore, so your own visits do not show up as traffic.
ALTER TABLE sites ADD COLUMN exclude_paths TEXT NOT NULL DEFAULT '';  -- newline separated, * wildcards
ALTER TABLE sites ADD COLUMN exclude_ips TEXT NOT NULL DEFAULT '';    -- newline separated, IP or CIDR

-- Alerts: a rule evaluated on a schedule that fires to a webhook or an email.
CREATE TABLE alerts (
    id           TEXT PRIMARY KEY,
    site_id      TEXT NOT NULL,             -- '' = every site
    kind         TEXT NOT NULL,             -- spike | drop | threshold | goal | digest
    metric       TEXT NOT NULL DEFAULT 'visitors',
    window       TEXT NOT NULL DEFAULT '1h',
    threshold    REAL NOT NULL DEFAULT 0,   -- percent for spike/drop, count for threshold
    channel      TEXT NOT NULL,             -- webhook | email
    destination  TEXT NOT NULL,             -- URL or address
    enabled      INTEGER NOT NULL DEFAULT 1,
    cooldown_min INTEGER NOT NULL DEFAULT 60,
    last_fired   TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);

-- Revenue orders, provider agnostic. Replaces polar_orders, which is kept
-- until the data has been copied across below.
CREATE TABLE orders (
    site_id         TEXT NOT NULL,
    provider        TEXT NOT NULL,             -- polar | stripe
    order_id        TEXT NOT NULL,
    created_at      TEXT NOT NULL,             -- RFC 3339 UTC
    status          TEXT NOT NULL,
    paid            INTEGER NOT NULL,
    net_amount      INTEGER NOT NULL,          -- cents, after discounts, before tax
    refunded_amount INTEGER NOT NULL,
    currency        TEXT NOT NULL,
    country         TEXT NOT NULL DEFAULT '',
    product         TEXT NOT NULL DEFAULT '',
    ref             TEXT NOT NULL DEFAULT '',
    source          TEXT NOT NULL DEFAULT '',
    campaign        TEXT NOT NULL DEFAULT '',
    landing         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (site_id, provider, order_id)
);
CREATE INDEX orders_site_created ON orders (site_id, created_at);

INSERT INTO orders (site_id, provider, order_id, created_at, status, paid, net_amount, refunded_amount, currency, country, product, ref, source, campaign, landing)
SELECT site_id, 'polar', order_id, created_at, status, paid, net_amount, refunded_amount, currency, country, product, ref, source, campaign, landing
FROM polar_orders;

DROP TABLE polar_orders;

-- Payment provider connections, provider agnostic. polar_connections is
-- migrated in and dropped.
CREATE TABLE payment_connections (
    site_id        TEXT NOT NULL,
    provider       TEXT NOT NULL,              -- polar | stripe
    access_token   TEXT NOT NULL,
    server         TEXT NOT NULL DEFAULT '',
    product_ids    TEXT NOT NULL DEFAULT '',   -- comma separated; empty = every product
    webhook_secret TEXT NOT NULL DEFAULT '',
    connected_at   TEXT NOT NULL,
    synced_at      TEXT NOT NULL DEFAULT '',
    sync_error     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (site_id, provider)
);

INSERT INTO payment_connections (site_id, provider, access_token, server, product_ids, webhook_secret, connected_at, synced_at, sync_error)
SELECT site_id, 'polar', access_token, server, product_ids, webhook_secret, connected_at, synced_at, sync_error
FROM polar_connections;

DROP TABLE polar_connections;
