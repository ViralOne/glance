<p align="center">
  <img src="docs/glance.svg" width="160" alt="Glance logo" />

  <h1 align="center">Glance</h1>

<p align="center">
  <img src="https://img.shields.io/github/go-mod/go-version/ViralOne/glance?filename=server%2Fgo.mod" alt="Go version" />
  <img src="https://img.shields.io/github/license/ViralOne/glance" alt="License" />
  <img src="https://github.com/ViralOne/glance/actions/workflows/ci.yml/badge.svg" alt="CI" />
</p>

A tiny, self-hosted web analytics service. The useful stuff at a glance: visitors, page views, top pages, referrers, countries, devices, custom events with properties and values, goals, funnels, Core Web Vitals, revenue, and which AI crawlers are reading you.

One Go binary, one SQLite file, one Docker container. Sites add a cookieless snippet of 2.3 KB (1.2 KB gzipped). No cohorts, heatmaps or session replay. No third-party services at runtime: favicons are fetched by Glance itself, the world map ships its own outlines, and brand icons are bundled.

</p>

```html
<script defer src="https://glance.example.com/glance.js" data-site="site_…"></script>
```

## Architecture

The snippet posts one small JSON body per page view (site id, URL, referrer, screen width, time zone) with `sendBeacon`, and again on `pushState` and `popstate` for single-page apps. The Go server validates the host against the site's domain, drops bots, derives browser, OS, device, country, region and campaign tags, and pushes the event onto an in-memory queue. The request never touches the database. A writer goroutine commits the queue every second or every 200 events in one transaction. Every minute (and on demand when the dashboard is opened) today's and yesterday's rollups are rebuilt from raw events; the dashboard reads only rollups, so it stays fast however much traffic you keep. The embedded Svelte UI lists your sites and, per site, shows a chart, tabbed breakdown cards, a live 3D globe and a range map.

## What is in the box

| Part | Where |
| --- | --- |
| Go server (collect endpoint, batched writer, rollups, favicons, MCP, embedded web UI) | `server/` |
| Web UI (Svelte 5, LayerChart, MapLibre, built into the binary) | `server/web/` |
| Tracking snippet, readable source | `server/internal/api/glance.src.js` |
| Tracking snippet, minified and served at `/glance.js` (`make snippet` rebuilds it) | `server/internal/api/glance.js` |

## Quick start (Docker)

```bash
git clone https://github.com/ViralOne/glance && cd glance
cp .env.example .env          # set GLANCE_ADMIN_USER and GLANCE_ADMIN_PASSWORD
mkdir -p data && chown 1000:1000 data   # Linux hosts only; the container runs as uid 1000
docker compose up -d --build   # or drop --build to pull ghcr.io/ViralOne/glance
open http://localhost:8082
```

Prebuilt images are published to `ghcr.io/ViralOne/glance` on every release: `latest`, `1`, `1.0`, `1.0.0` and so on for linux/amd64 and linux/arm64.

Add a website, click **Tracking code**, and paste the snippet before `</head>`. Data appears as soon as you open the dashboard.

Data lives in `./data/glance.db`. Back up by copying that file (use `sqlite3 data/glance.db ".backup backup.db"` for a consistent copy while running).

## Quick start (binary)

```bash
make web                       # builds the Svelte UI into the Go embed directory
make build                     # bin/glance with the UI embedded
GLANCE_DATABASE_PATH=./glance.db ./bin/glance     # listens on :8080
```

Configuration is the same set of environment variables as Docker (see [Configuration](#configuration)). `GLANCE_DATABASE_PATH` defaults to `/data/glance.db`, so set it to somewhere writable.

## Track a site

Paste the snippet, then visit the site. That is the whole integration.

**Custom events** from your page:

```js
glance('signup')
glance('download', { plan: 'pro' })          // properties become their own breakdown
glance('purchase', { plan: 'pro' }, 1900)    // and a value, in cents
```

Properties are kept as a small object of scalars: at most eight keys, 60
characters each, nested objects dropped rather than flattened, because a
breakdown can only group by a scalar. Values are summed, so a goal can report
what it was worth.

**Single-page apps** are handled: the snippet re-sends on `pushState` and `popstate`.

**Debugging the snippet.** Add the boolean `data-debug` attribute while troubleshooting:

```html
<script defer src="https://glance.example.com/glance.js" data-site="site_…" data-debug></script>
```

Glance then writes fixed, payload-free delivery messages to the browser console: startup or privacy opt-out, fetch response status, and beacon fallback. It makes no extra network requests and never logs the site id, collector URL, page URL, referrer, or event body. A `202` only confirms transport because the public collector deliberately gives accepted and dropped payloads the same response. Remove the attribute after troubleshooting.

**Testing locally.** Pages served from `localhost`, `127.0.0.1`, `*.localhost`, `*.local`, `*.test` or a private LAN address are accepted for every site, so you can try the snippet on a dev server before deploying. Enable **Ignore local development traffic** in that site's settings when you want future development visits excluded from realtime views, rollups, alerts, exports, and MCP responses. The switch cannot remove past local visits because Glance intentionally does not retain the page hostname or client IP. Anything else must match the site's domain or a subdomain of it. Run with `GLANCE_LOG_LEVEL=debug` to see why an event was dropped.

**What is dropped.** Browsers with `navigator.webdriver` set, visitors sending
Do Not Track or Global Privacy Control, events whose page host does not match
the site, paths and addresses a site excludes, anything past the per-address
rate limit, and bodies over 8 KB. The endpoint always answers 202 so it cannot
be used to probe which site ids exist.

**Crawlers are not dropped.** They are recorded under their own kind with a
display name and never counted as a visitor or a pageview, so the dashboard can
show which search engines and which AI crawlers (GPTBot, ClaudeBot, Perplexity,
Bytespider and about thirty others) read the site. Knowing that is worth a row;
mixing it into human traffic is not.

## Dashboard

The index lists every site with its favicon, a 14-day sparkline and this week's visitors against last week's. Drag the grip to reorder.

Per site:

- **Visitors, page views, views per visitor** with the change versus the previous equal window, over 24h, 48h, 7d, 30d, 90d or 180d.
- **Chart**: hourly for 24h, 48h and 7d, daily for 30d, 90d and 180d, with the dark tooltip on hover.
- **People in the last 30 minutes** with a per-minute strip.
- **Tabbed cards**: Pages; Sources as Referrer, Source (`utm_source`, falling back to `?ref=` and `?source=`), Campaign or Medium; Locations as Countries, Regions or Cities; Devices as Browsers, OS or Devices; Events and their Properties; Goals; Crawlers, all or just the AI ones. A tab only appears once its dimension has something in it. The expand icon beside a title opens the full list with a filter box.
- **Funnels**: each step's visitors as a share of the first, with the loss between steps called out.
- **Speed**: Core Web Vitals (LCP, INP, CLS, TTFB, FCP) as p75 with Google's good/needs-improvement/poor rating and the share of page loads rated good, overall and split by device or page.
- **Notes** under the chart: dated annotations, so a spike still has a reason next to it a month later.
- **Live**: a 3D globe of visitors from the last five minutes, dots sized by count, arcs flowing from each country to your home country, refreshed every five seconds. Switch to the range map for the whole window.
- **Settings** per site: name, domain, extra domains, path and IP exclusions, default date range, accent colour, home country, refetch favicon, goals, funnels, notes, shared links, payment providers, Search Console, and history import.

## How it works

**Visitors.** There are no cookies and no stored IPs. A visitor is `sha256(daily salt + site + IP + user agent)`, truncated to 16 hex characters. The salt rotates every UTC day and is persisted, so a restart does not split a day and nobody can be followed from one day to the next. A day's visitor count is exact; multi-day totals are the sum of daily uniques, the same convention Plausible uses.

Totals are counted per day even on the ranges that chart hourly. Summing an hourly series would count a visitor once per hour they were active, so someone reading two articles either side of 10:00 would be two visitors — and the index card, which sums days, would then disagree with the site page about the same seven days. The 24h and 48h windows do not start and end on day boundaries, so their edge days are read from the hourly rows and inherit that over-count within one day; there is no way around that without keeping raw events forever, and it is bounded by a day rather than spread over the window.

**Country and region.** A proxy country header (`CF-IPCountry`, `X-Vercel-IP-Country`, `X-Country-Code`, `X-Geo-Country`, `CloudFront-Viewer-Country`) is used when present. Otherwise the visitor's browser time zone is mapped to a country with a table generated from the IANA zone file. "Regions" are the city part of that time zone (`Europe/London` → London), the only sub-country signal available without a GeoIP database. Point `GLANCE_GEOIP_PATH` at a GeoLite2-City or DB-IP City Lite file and real cities and regions appear instead, with the time zone as the fallback for addresses the database does not know. Nothing is fetched at runtime.

**Browser, OS, device.** A small ordered user-agent matcher (Edge before Chrome, Chrome before Safari, `CriOS` and `FxiOS` on iOS) plus the screen width the snippet sends. No external database.

**Referrers.** Host only, same-site traffic counts as direct, and common hosts are aliased (`t.co` → `x.com`, `www.google.com` → `google.com`, and so on).

**Rollups.** `hourly_stats` holds page views and distinct visitors per hour. `daily_stats` holds, per day, a total plus every dimension (page, referrer, country, region, city, device, browser, OS, event, event properties, `utm_source`, `utm_campaign`, `utm_medium`, crawler, AI crawler) with a summed event value, capped at 500 keys per dimension per day with the rest folded into "Other". `daily_vitals` holds each metric's p75, p50 and good/poor counts. Raw events are pruned after the retention period; two days is the floor because today and yesterday are rebuilt from them. Crawler rows are excluded from every human aggregate explicitly, since their blank visitor would otherwise count as one more distinct visitor a day.

**Favicons.** Glance fetches your site's icon from the site itself (declared `<link rel="icon">`, then `/favicon.ico`) and caches referrer icons the same way, so the dashboard never asks Google or a CDN. Every fetch goes through a guard that refuses loopback, private, link-local and carrier-grade NAT addresses, including after redirects, and dials the checked IP rather than the name.

**Map.** Country outlines come from Natural Earth (public domain) bundled into the UI, rendered by MapLibre with no tiles and no attribution requirement. Antarctica is left out and rings that cross the antimeridian are unwrapped so nothing fills the canvas. MapLibre loads in its own chunk after the dashboard's first paint.

**Icons.** Browser and OS marks come from [SVGL](https://svgl.app), downloaded once and bundled. Devices are small stroke glyphs. Flags are emoji; on systems that do not render them the country name still shows.

## Goals

A goal is a custom event or a page that counts as a conversion. It reports
conversions (visitors who converted, summed per day, the same convention
visitors use everywhere else), completions (every firing), a rate against the
window's visitors, and a summed value. A trailing `*` on a path matches a
prefix, so `/thanks*` covers `/thanks/pro`.

Goals read the daily rollups, so they work over any range and keep working after
raw events are pruned. The rate's denominator is the same visitor count the
dashboard shows, so the two can never disagree.

## Funnels

Two to eight ordered steps, each an event name or a page path. Each step counts
only visitors who completed every earlier step *first*, so it measures ordering
rather than co-occurrence.

Two limits, both consequences of Glance's design rather than oversights, and
both reported in the API response rather than hidden:

- Funnels read raw events, because a rollup cannot know whether the same visitor
  who saw `/pricing` later saw `/checkout`. So a funnel reaches back only as far
  as raw events are kept, and says so when the range was cut.
- A funnel is measured within a UTC day. Visitor hashes rotate daily by design,
  so someone who lands on Monday and converts on Tuesday is two different
  hashes and cannot be joined. A funnel answers "of those who started today,
  how many finished today".

## Core Web Vitals

The snippet samples LCP, INP, CLS, TTFB and FCP with `PerformanceObserver`, the
same source Chrome's own field data uses, and sends them once when the page goes
away. Daily p75, p50 and the good/poor counts are rolled up per metric, per
device and per page; raw samples are pruned with the events.

Two honest caveats. A window's p75 is the sample-weighted mean of the daily
p75s, because percentiles do not add — close, not exact; the good and poor
shares are plain counts and are exact. And INP is the worst interaction on the
page rather than a high percentile of interactions, which can only overstate,
never flatter. Turn the whole thing off with `data-vitals="false"`.

## Shared dashboards

A share publishes one site read-only at an unguessable URL, so numbers can be
shown to someone without giving them an account. The slug is the credential — 80
bits of entropy — and it grants exactly one site's aggregates: no settings, no
tokens, no other site, no filters (those read raw events and would let a viewer
probe individual behaviour), and no writes. Revenue is opt-in per share, and a
password can be added. Shared pages are served `X-Robots-Tag: noindex`.

## Alerts and the weekly digest

A rule fires to a webhook or an email address:

| Kind | Fires when |
| --- | --- |
| `spike` | The metric is above the same window one week earlier by more than N percent |
| `drop` | It is below by more than N percent |
| `threshold` | It exceeds N outright |
| `digest` | Not a condition but a schedule: a weekly summary, Mondays at the hour you choose |

Spike and drop compare with a week earlier rather than with the window
immediately before, because traffic has a weekly shape and comparing Monday
morning with Sunday night fires on the shape rather than on news. Every rule
reads the same rollups the dashboard reads, so an alert can never disagree with
what you see when you follow it. Measurements are over whole hours, the finest
rollup there is, so a one-hour rule notices a spike up to an hour after it
starts; the live view is the right tool for anything faster.

The webhook payload carries `content` and `text` alongside the structured
`alert`, so one shape works for a raw endpoint, Slack and Discord alike. Email
needs `GLANCE_SMTP_HOST` and `GLANCE_SMTP_FROM`; without them the email channel
is hidden rather than offered and then failing. Each rule has a **Test** button
that delivers immediately, whatever its condition.

## Importing history from another tool

Site settings, **Import history**: Plausible (the CSV export, zipped or a single
file), Google Analytics 4 (a report CSV with Date as a dimension), Fathom,
Umami, or another Glance instance's export.

Imported data is written straight into the daily rollups, not into raw events.
That is deliberate: raw events exist to rebuild the last couple of days and are
pruned, so importing three years of history as events would be discarded within
the month, while a rollup row is kept forever and is exactly what the dashboard
reads. Re-running an import replaces the days it covers rather than adding to
them, so a corrected export overwrites a wrong one.

The trade is that imported days have no hourly detail and cannot be filtered,
because the export has no individual events in it. A range covering imported
days is charted by day and says so, rather than spreading a day's total across
24 hours to invent a shape nobody measured.

## Ad blockers

Filter lists match on URL shape, and Glance's defaults are the shape they look
for. Checked against the real EasyPrivacy and EasyList rules:

- **Serve Glance from the same registrable domain as the site it measures.**
  This is the one that matters. `stats.example.com` measuring `example.com` is a
  first-party request, and a blocker will not break someone's own domain — the
  default paths pass cleanly. A collector on a different domain is third-party
  and EasyPrivacy blocks it.
- **The snippet posts with `fetch`, not `sendBeacon`.** That looks backwards and
  is not: EasyPrivacy carries a blanket `*$ping,third-party` rule, so every
  third-party `sendBeacon` is blocked whatever the URL is. Renaming paths does
  not help, and neither does a fallback, because a blocked `sendBeacon` still
  returns true. `keepalive` is what makes `fetch` safe here: it exists precisely
  so a request survives the page being torn down.
- **Do not name the subdomain `analytics.*` and keep the default collect path.**
  `analytics.<anything>/collect` is a real rule and matches even first-party.
  Renaming either fixes it.
- `GLANCE_SNIPPET_PATH` and `GLANCE_COLLECT_PATH` serve the script and the
  collector at a path of your choosing, in addition to the defaults. The served
  script has the collect path written into it, so changing it needs no edit to
  any page.

For comparison, the same check says Plausible, Fathom, DataFast and Google
Analytics are all blocked in their hosted form. Self-hosting on your own
subdomain is not a marginal gain here.

## Settings

The gear in the header opens `/settings`:

- **Overview**: sites, raw events, rollup rows, database size, uptime.
- **Appearance**: accent colour (four design swatches or any hex; it recolours the wordmark, links, chart, bars, arcs and map in both themes) and the title. Both are public so the login screen matches.
- **MCP**: the endpoint URL, an on/off switch, and API tokens with a read or write scope. Minting shows the secret once with a ready-to-paste config block; the list shows name, prefix, scope, created and last used, with revoke.
- **Alerts**: spike, drop, threshold and weekly digest rules, each to a webhook or an email address, with a Test button.
- **Retention**: 2 to 90 days for raw events, unless `GLANCE_RETENTION_DAYS` pins it. Raw events are what filtered views and funnels read, so this is how far back those reach.
- **Data**: export download, events written since start, and the dropped count if the queue ever overflowed.

## MCP (for AI agents)

Glance speaks the [Model Context Protocol](https://modelcontextprotocol.io) at `/mcp` (Streamable HTTP), read-only. Point the agent you already use at it and ask things like *"how are my sites doing this week?"*, *"did anything spike on example.com in the last month?"* or *"where is example.com's traffic coming from?"*. There is no LLM inside Glance; it serves structured numbers plus small computed signals so the agent does not have to do arithmetic on long series.

| Tool | Returns |
| --- | --- |
| `list_sites` | Every site with visitors this week and last, and visitors online now |
| `overview` | Every site over a range with the change versus the previous window, a rising/falling/flat trend, spike buckets, the peak, and the top page, referrer, country and events. The right first call |
| `site_stats` | Full detail for one site: totals, series, top 10 of every breakdown, and the same signals. Takes `filters` to narrow to matching visitors |
| `breakdown` | The complete list for any dimension, up to 500 rows. Takes the same `filters` |
| `search_terms` | Google search queries from Search Console with clicks, impressions and position, for connected sites |
| `revenue` | Revenue from every connected processor: totals, series, revenue per visitor, attributed versus unattributed orders, and revenue by first-touch referrer, source, campaign, landing page, country and product |
| `goals` | Conversions and conversion rates for the site's goals |
| `funnels` | Step-by-step drop-off, with the retention and single-day limits reported |
| `vitals` | Core Web Vitals as p75 and p50, with Google's ratings, by device and page |
| `crawlers` | Which search and AI crawlers read the site, by name. Never part of any traffic total |
| `notes` | The owner's dated annotations, which usually explain a spike |
| `add_note` | Records an annotation. The only tool that writes, and it cannot change a measurement; needs a token with the `write` scope |

Ranges are `24h`, `48h`, `7d`, `30d`, `90d`, `180d`; words like `week` and `month` are accepted. Sites resolve by id, name, domain or a fuzzy match. `filters` is a map of dimension to key, such as `{"ref": "google.com", "country": "GB"}`; filtered answers come from raw events and say so when the range was cut to the retention window. The server instructions explain how revenue attribution is collected and why the unattributed bucket exists, so the agent does not mistake pre-attribution orders for direct sales.

Mint a token in **Settings → MCP** (or set `GLANCE_MCP_TOKEN`) and use it as a bearer token. The admin login works there too. Turning the endpoint off in Settings returns `404 mcp_disabled` to everyone.

```bash
claude mcp add --transport http glance https://glance.example.com/mcp --header "Authorization: Bearer glance_tok_…"
```

Any client that supports Streamable HTTP with a custom header can connect the same way.

Things to ask once it is connected:

- *"Give me an overview of all my sites for the last 30 days."*
- *"Which referrer sent the most visitors to example.com this week, and is it growing?"*
- *"Show me example.com's traffic from Germany over the last month."* (uses `filters`)
- *"What did people search on Google to find example.com?"* (needs [Search Console](#google-search-terms))
- *"How much revenue came from Product Hunt visitors?"* (needs [Polar](#revenue-from-polar))

## API

All endpoints are under `/api/v1`. Errors are JSON: `{"error": "code", "message": "..."}`.

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET | `/health` | none | `{"status":"ok"}` |
| GET | `/glance.js` | none | The tracking snippet, cached for a day |
| POST | `/api/v1/collect` | none | Ingest one event `{s, n, u, r, w, tz}`; always 202 |
| GET | `/api/v1/theme` | none | `{accent, title}` for the UI |
| GET/POST | `/api/v1/sites` | admin | List (with 7-day card and live count) / create `{name?, domain}` |
| GET/PATCH/DELETE | `/api/v1/sites/:id` | admin | Manage `{name?, domain?, home_country?}` |
| POST | `/api/v1/sites/reorder` | admin | `{ids}` in display order |
| GET | `/api/v1/sites/:id/stats?range=` | admin | Totals, previous window, series and top-10 breakdowns |
| GET | `/api/v1/sites/:id/breakdown?dim=&range=&limit=` | admin | Full list for one dimension, up to 500 rows |
| GET | `/api/v1/sites/:id/live` | admin | Last 5 minutes by country, per-minute visitors for the last 30 minutes |
| GET | `/api/v1/sites/:id/favicon` | admin | The stored site icon |
| POST | `/api/v1/sites/:id/refresh-favicon` | admin | Refetch it now |
| GET | `/api/v1/favicon?host=` | admin | A cached referrer icon |
| POST | `/api/v1/rollup` | admin | Flush the queue and rebuild today's rollups now |
| GET | `/api/v1/status` | admin | Counts, database size, uptime, ingest stats |
| GET/PATCH | `/api/v1/settings` | admin | `accent`, `title`, `mcp_enabled`, `retention_days` |
| GET/POST | `/api/v1/tokens` | admin | List / mint `{name}` (returns `secret` once) |
| DELETE | `/api/v1/tokens/:id` | admin | Revoke |
| GET | `/api/v1/export` | admin | Every site and daily rollup as one JSON file |
| GET/POST | `/api/v1/sites/:id/goals` | admin | List measured goals / create one |
| PATCH/DELETE | `/api/v1/sites/:id/goals/:goal` | admin | Manage |
| GET/POST | `/api/v1/sites/:id/funnels` | admin | List measured funnels / create one |
| PATCH/DELETE | `/api/v1/sites/:id/funnels/:funnel` | admin | Manage |
| GET/POST | `/api/v1/sites/:id/notes` | admin | Annotations in a range / create one |
| PATCH/DELETE | `/api/v1/sites/:id/notes/:note` | admin | Manage |
| GET | `/api/v1/sites/:id/vitals?range=` | admin | Core Web Vitals |
| GET/POST | `/api/v1/sites/:id/shares` | admin | Shared links / publish one |
| PATCH/DELETE | `/api/v1/sites/:id/shares/:slug` | admin | Set a password or revenue visibility / revoke |
| POST | `/api/v1/sites/:id/import?format=` | admin | Import history; the body is the export file |
| GET | `/api/v1/sites/:id/payments` | admin | Every payment provider's connection state |
| PUT/DELETE | `/api/v1/sites/:id/payments/:provider` | admin | Connect or disconnect `polar` or `stripe` |
| POST | `/api/v1/sites/:id/payments/:provider/sync` | admin | Pull orders now |
| POST | `/api/v1/payments/:provider/webhook/:id` | signature | Provider webhook |
| GET/POST | `/api/v1/alerts` | admin | Rules / create one |
| PATCH/DELETE | `/api/v1/alerts/:id` | admin | Manage |
| POST | `/api/v1/alerts/:id/test` | admin | Deliver now, whatever the condition |
| GET | `/api/v1/shared/:slug` | slug | A published dashboard, read-only, no login |
| GET | `/api/v1/shared/:slug/meta` | none | Whether a slug exists and needs a password |
| POST | `/mcp` | token or admin | [MCP](#mcp-for-ai-agents) endpoint; read-only unless the token has the write scope |

**Admin auth.** Set `GLANCE_ADMIN_USER` and `GLANCE_ADMIN_PASSWORD` and the UI shows a sign-in screen; admin endpoints then need the session cookie it sets (`POST /api/v1/auth/login`) or HTTP Basic credentials (`curl -u user:pass …`). Sessions last 30 days, survive restarts, and are invalidated when the password changes. Leave both unset and everything is open. Only do that behind your own proxy, Tailscale or VPN.

**API tokens** (`glance_tok_…`) are minted in Settings, stored as hashes, and accepted for `/mcp` and every `GET` admin endpoint. They get `403 read_only` on anything that writes.

Tokens carry a scope. A `read` token can only read. A `write` token may
additionally record chart annotations through MCP's `add_note`, which is the one
thing an agent can change; nothing any token can do alters a measurement,
deletes data or reveals another token. Everything minted before scopes existed
stays `read`, which is what it was promised to be.

## Configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `GLANCE_PORT` | `8080` | |
| `GLANCE_DATABASE_PATH` | `/data/glance.db` | WAL mode, migrations applied on start |
| `GLANCE_RETENTION_DAYS` | `30` | Days of raw events to keep, minimum 2. When set it pins the value; leave unset to manage it from Settings. Rollups are kept forever |
| `GLANCE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. JSON logs on stdout |
| `GLANCE_ADMIN_USER` | | Dashboard username; set together with the password |
| `GLANCE_ADMIN_PASSWORD` | | 8+ characters. Unset = no login |
| `GLANCE_MCP_TOKEN` | | Optional fixed bearer token for the [MCP endpoint](#mcp-for-ai-agents); 16+ characters. Tokens minted in Settings work without it |
| `GLANCE_GOOGLE_CLIENT_ID` | | OAuth client for [Google Search Console](#google-search-terms); set together with the secret |
| `GLANCE_GOOGLE_CLIENT_SECRET` | | |
| `GLANCE_TRUSTED_PROXY_HOPS` | `0` | How many reverse proxies sit in front. Each appends the address it saw to `X-Forwarded-For`, so the visitor is that many entries from the right. `0` ignores the header and reads the peer address. Set `1` behind Traefik, Caddy, nginx or Cloudflare. Both directions of getting this wrong are quiet: too low and every visitor shares the proxy's address, too high and a caller can mint a new address per request |
| `GLANCE_SNIPPET_PATH` | | Extra path to serve the script at, e.g. `/js/app.js`; see [Ad blockers](#ad-blockers) |
| `GLANCE_COLLECT_PATH` | | Extra path for the collector, e.g. `/i`. Written into the served script |
| `GLANCE_ALLOW_LOCAL_EVENTS` | off | Accept events whose page host looks like a development address even from a public client. The host comes from the request body, so this bypasses the site-domain check for every site; only for a collector that is not reachable from the internet |
| `GLANCE_COLLECT_BURST` | `120` | Events accepted per client address before the rate limit bites. A zero in either this or the rate disables the limit |
| `GLANCE_COLLECT_PER_SECOND` | `4` | Sustained events per second per address |
| `GLANCE_GEOIP_PATH` | | Optional MaxMind-format city database (GeoLite2-City.mmdb or DB-IP City Lite) for real cities and regions. Nothing is fetched at runtime; without it, location comes from the visitor's time zone |
| `GLANCE_SMTP_HOST` | | SMTP host for the digest and email alerts. Without it, alerts go to webhooks only |
| `GLANCE_SMTP_PORT` | `587` | |
| `GLANCE_SMTP_USER` | | |
| `GLANCE_SMTP_PASSWORD` | | |
| `GLANCE_SMTP_FROM` | | Required when the host is set |
| `GLANCE_SMTP_TLS` | `starttls` | `starttls`, `tls` (implicit) or `none` |
| `GLANCE_BASE_URL` | | Glance's own public URL, linked from digests and alerts where there is no request to rebuild it from |

Glance reads the client IP from `X-Forwarded-For` counting from the right, as far as `GLANCE_TRUSTED_PROXY_HOPS` allows, and otherwise from the connection. The IP is only ever hashed. Counting from the left would trust a value the caller controls, which is enough to inflate the visitor count without limit.

## Google search terms

Google strips the query from the referrer, so the only way to see which searches bring people in is the Search Console API. Each site's settings panel has a **Connect Google Search Console** button once an OAuth client is configured. Glance pulls the last 16 months on connect and refreshes once a day; the dashboard gets a **Search terms** card and the MCP server a `search_terms` tool. Google's data trails by two to three days.

1. In [Google Cloud](https://console.cloud.google.com/apis/credentials), create a project, enable the **Google Search Console API**, and create an **OAuth client ID** of type *Web application*.
2. Add the redirect URI shown in the site's settings panel, `https://glance.example.com/api/v1/google/callback`. It must match exactly.
3. Set `GLANCE_GOOGLE_CLIENT_ID` and `GLANCE_GOOGLE_CLIENT_SECRET` and restart.
4. On the OAuth consent screen, add your Google account as a test user, or publish the app. While the app is in *Testing*, Google expires the grant after seven days and Glance shows **Reconnect Google**; publishing removes that limit (no verification needed for your own use, you just click through an "unverified app" warning once).
5. Open a site, **Settings**, **Connect Google Search Console**. Glance picks the property matching the domain, preferring a domain property (`sc-domain:`) over URL-prefix ones, and asks you to choose if none matches.

Only the `webmasters.readonly` scope is requested. The refresh token is stored in the SQLite file alongside everything else; disconnecting revokes it with Google and deletes the stored terms.

## Filtering

Click any row in Pages, Sources, Locations, Devices or Events to narrow the whole dashboard to the visitors who matched it: the chart, the totals and every other card. Filters stack, sit in the URL so they can be shared, and clear with one click. Because filtered views are built from raw events rather than rollups, they only reach back as far as raw events are kept: with the default 7 days, a 30d range under a filter shows the last week and says so. Raise retention in Settings if you want to filter longer ranges. The MCP `site_stats` and `breakdown` tools take the same `filters`.

## Revenue from Stripe or Polar

Each site can show revenue next to traffic. Open a site, **Settings**, and connect **Stripe**, **Polar**, or both — orders from every provider land in one table, and a `Processor` tab breaks revenue down by where it came from.

**Stripe** needs a secret or restricted key with read access to charges. Glance reads Charges rather than Payment Intents or Invoices because a charge is the one object that exists for every way money arrives — subscription, one-off, invoice, checkout — and it carries both what was captured and what was refunded, which is exactly what a revenue figure is. Add a webhook subscribed to `charge.succeeded`, `charge.refunded` and `charge.updated` and paste its secret so sales appear within seconds.

**Polar** needs an organization access token (Polar dashboard, Settings, Developers; the `orders:read` scope is enough) and a webhook subscribed to the `order.*` events.

Either way Glance pulls two years of history on connect, then reconciles once a day so refunds and missed webhooks are picked up. If you sell several products, list the ids that belong to this site so the others are ignored.

The dashboard gets revenue, orders and revenue per visitor tiles, revenue bars behind the traffic chart, and a **Revenue** card broken down by first-touch referrer, source, campaign, landing page, country and product. Revenue is the net amount after discounts and before tax, less refunds, in the currency you charge in. The MCP server gains a `revenue` tool.

### Attributing sales to a source

Polar only knows what your checkout tells it. Add `data-attribution` to the snippet and it remembers each visitor's first referrer and landing URL in their own browser (`localStorage`, never sent to Glance, no cookie):

```html
<script defer src="https://glance.example.com/glance.js" data-site="site_…" data-attribution></script>
```

When you create a checkout, read `glance.attribution()` (it returns `{ r: referrer, l: landing URL, t: timestamp }` or `null`) and pass `attr_ref` and `attr_landing` in the checkout `metadata`. Stripe metadata is read from the charge, and from the payment intent, invoice or subscription behind it, because a subscription renewal carries the metadata of the original checkout rather than of the renewal charge. Glance normalises them with the same rules as page views, so "Revenue by source" agrees with "Sources". Orders placed before you wire this up count towards totals but show as unattributed.

## Deploying with Dokploy (or any compose host)

Use `docker-compose.dokploy.yml`, not `docker-compose.yml`. It swaps the `./data` bind mount for a named volume (the bind mount is created root-owned on the host, and the image runs as uid 1000, so SQLite cannot write `/data/glance.db`), joins the external `dokploy-network` so Traefik can route to it, and drops the published port so the server is reachable only through your HTTPS proxy.

1. New **Compose** application → your repo, compose path `docker-compose.dokploy.yml`.
2. **Environment** tab: `GLANCE_ADMIN_USER`, `GLANCE_ADMIN_PASSWORD`, optionally `GLANCE_MCP_TOKEN`. Dokploy writes these to a `.env` beside the compose file, which is what `env_file` picks up.
3. Add a domain with HTTPS in Dokploy pointing at port `8080`; deploy.
4. Use that domain in the snippet: `https://glance.example.com/glance.js`.

Editing the compose file in Dokploy's UI only works for **Raw** compose apps; when the source is Git, commit changes and redeploy.

## Footprint

The image is about 37 MB and idles at under 10 MB of memory. Storage is dominated by rollups: one row per site per day per breakdown key, so a site with a hundred pages and a dozen referrers adds a few hundred rows a day. Raw events are the only thing that grows with traffic, and they are pruned.

## Development

```bash
cd server && GLANCE_DATABASE_PATH=./data/glance.db go run ./cmd/glance   # API on :8080
cd server/web && npm install && npm run dev                               # UI on :5173, proxies /api
make test                                                                 # Go + web tests
make build                                                                # bin/glance with the UI embedded
```

Requires Go 1.27 and Node 24 (see `.tool-versions`). The SQLite driver is pure Go, so `CGO_ENABLED=0` builds work everywhere.

`make snippet` regenerates the served `glance.js` from `glance.src.js`; the
minified file is committed, so this is only needed when the source changes.

The Go tests cover ingest through rollup to stats with a fixed clock, day-scoped
hashing, the user-agent and crawler tables, timezone mapping, the SSRF guard,
favicon parsing, the MCP tools over a real client, tokens, settings, migrations
including the upgrade path, and the config loader.

Two are worth knowing about:

- `TestSimulatedTraffic` generates a week of plausible traffic through the real
  HTTP handler and checks every reported number against the traffic it
  generated, with expectations derived from the generation rather than
  hardcoded. The funnel expectation is computed by replaying each visitor's
  ordered events, so it verifies ordering rather than co-occurrence. It found
  two real counting bugs the first time it ran.
- `TestCollectRejectsSpoofedDevelopmentHost` and
  `TestClientIPCountsFromTheRight` guard the two ways the collector could be
  made to lie about who visited. Both are easy to reintroduce by "simplifying"
  the code they cover.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Releases are listed in [CHANGELOG.md](CHANGELOG.md); security reports go through [SECURITY.md](SECURITY.md).

## Licence

MIT.
