// Thin typed client for the Glance API.

export type Range = '24h' | '48h' | '7d' | '30d' | '90d' | '180d'
export const RANGES: Range[] = ['24h', '48h', '7d', '30d', '90d', '180d']
export const DEFAULT_RANGE: Range = '7d'
export const isRange = (v: string): v is Range => (RANGES as string[]).includes(v)

export interface Point {
  t: string
  pageviews: number
  visitors: number
}

export interface Row {
  key: string
  pageviews: number
  visitors: number
  /** Summed event value in minor units; only set for events and properties. */
  value?: number
}

export interface Totals {
  pageviews: number
  visitors: number
}

export type Dim =
  | 'page'
  | 'ref'
  | 'country'
  | 'region'
  | 'city'
  | 'device'
  | 'browser'
  | 'os'
  | 'event'
  | 'prop'
  | 'utm_source'
  | 'utm_campaign'
  | 'utm_medium'
  | 'bot'
  | 'aibot'

export interface Marker {
  t: string
  ref: string
  visitors: number
}

/** Dimension to key. An empty value is a real filter (direct, unknown). */
export type Filters = Partial<Record<Dim, string>>

export function filterQuery(filters: Filters): string {
  const q = new URLSearchParams()
  for (const [dim, key] of Object.entries(filters)) if (key !== undefined) q.set(dim, key)
  const s = q.toString()
  return s ? '&' + s : ''
}

export interface Summary {
  range: Range
  from: string
  to: string
  bucket: 'hour' | 'day'
  totals: Totals
  previous: Totals
  series: Point[]
  markers: Marker[]
  breakdowns: Record<Dim, Row[]>
  filters?: Filters
  truncated?: boolean
  previous_unavailable?: boolean
  retention_days?: number
  /** The window contains imported days, which have no hourly detail, so the
   *  chart was bucketed by day even on a range that normally charts hourly. */
  hourly_unavailable?: boolean
}

export interface Live {
  total: number
  countries: Row[]
  recent: { at: string; country: string; path: string }[]
  /** Latest durably written human activity, retained after raw events expire. */
  last_activity: string
  minutes: number[] // distinct visitors per minute, last 30, oldest first
  total_30m: number
}

export interface SiteCard {
  visitors: number
  previous: number
  pageviews: number
  spark: Point[]
}

export interface Site {
  id: string
  name: string
  domain: string
  home_country: string
  /** Overrides the account-wide accent on this site's dashboard; '' follows it. */
  accent: string
  /** Range this site's dashboard opens on; '' uses DEFAULT_RANGE. */
  default_range: string
  has_favicon: boolean
  position: number
  /** Extra registrable domains this site accepts events from. */
  domains: string[]
  /** Glob patterns whose pageviews are ignored, e.g. "/admin/*". */
  exclude_paths: string[]
  /** Addresses or CIDR blocks to ignore, so your own visits do not count. */
  exclude_ips: string[]
  /** Drops localhost and other development-host traffic before it is stored. */
  exclude_local_traffic: boolean
  created_at: string
  updated_at: string
  card: SiteCard
  live: number
}

export interface AuthState {
  auth_required: boolean
  authenticated: boolean
  /** Only sent to a caller who is already signed in. */
  username?: string
  /** Where the credential came from: generated on first boot, set by you, or the environment. */
  source?: 'generated' | 'set' | 'env'
  /** False when the environment owns the credential, since a restart would overwrite a change. */
  can_change?: boolean
}

export interface CollectorDiagnostics {
  /** Payloads that passed collector validation; not a durable-write count. */
  accepted: number
  dropped: number
  reasons: {
    rate_limited: number
    privacy_signal: number
    invalid_body: number
    unknown_site: number
    host_mismatch: number
    local_exclusion: number
    path_exclusion: number
    ip_exclusion: number
    processing_error: number
  }
}

export interface Status {
  version: string
  sites: number
  raw_events: number
  daily_rows: number
  db_bytes: number
  uptime_seconds: number
  written: number
  dropped: number
  admin_auth: boolean
  env_token_set: boolean
}

export interface GeneralSettings {
  accent: string
  title: string
  mcp_enabled: boolean
  retention_days: number
  retention_from_env: boolean
}

export interface Token {
  id: string
  name: string
  prefix: string
  /** 'read' can only read; 'write' may additionally record annotations. */
  scope: 'read' | 'write'
  created_at: string
  last_used_at?: string
}

export interface GoogleConnection {
  site_id: string
  property: string
  email: string
  connected_at: string
  synced_at: string
  sync_error: string
}

export interface GoogleStatus {
  configured: boolean
  connected: boolean
  connection?: GoogleConnection
  available_properties?: string[]
  needs_reconnect: boolean
  latest_day?: string
}

export interface SearchTerm {
  query: string
  clicks: number
  impressions: number
  position: number
}

export interface RevenuePoint {
  t: string
  revenue: number // minor units
  orders: number
}

export interface RevenueTotals {
  revenue: number
  orders: number
}

export interface RevenueRow {
  key: string
  revenue: number
  orders: number
}

export type RevenueDim = 'ref' | 'source' | 'campaign' | 'landing' | 'country' | 'product' | 'provider'

export interface Revenue {
  range: Range
  currency: string
  totals: RevenueTotals
  previous: RevenueTotals
  series: RevenuePoint[]
  breakdowns: Record<RevenueDim, RevenueRow[]>
}

export interface Goal {
  id: string
  site_id: string
  name: string
  kind: 'event' | 'path'
  target: string
  value: number
  position: number
  created_at: string
}

export interface GoalResult extends Goal {
  /** Summed daily converting visitors, matching how visitors are counted. */
  conversions: number
  /** Every firing, so one visitor converting twice contributes two. */
  completions: number
  /** Conversions over the window's visitors, as a percentage. */
  rate: number
  /** Summed worth in minor units. */
  value: number
}

export interface FunnelStep {
  name: string
  kind: 'event' | 'path'
  target: string
}

export interface FunnelStepResult extends FunnelStep {
  visitors: number
  rate: number
  drop_off: number
  drop_off_rate: number
}

export interface Funnel {
  id: string
  site_id: string
  name: string
  steps: FunnelStep[]
  position: number
  created_at: string
}

export interface FunnelResult extends Omit<Funnel, 'steps'> {
  steps: FunnelStepResult[]
  conversion: number
  /** The window was cut to the retention period: funnels read raw events. */
  truncated?: boolean
  retention_days?: number
  from: string
  to: string
}

export interface Note {
  id: string
  site_id: string
  day: string
  text: string
  created_at: string
}

export type VitalMetric = 'LCP' | 'INP' | 'CLS' | 'TTFB' | 'FCP'

export interface VitalRow {
  metric: VitalMetric
  unit: string
  samples: number
  p75: number
  p50: number
  rating: 'good' | 'needs-improvement' | 'poor'
  good_pct: number
  poor_pct: number
}

export interface VitalScope {
  key: string
  rows: VitalRow[]
}

export interface Vitals {
  range: Range
  overall: VitalRow[]
  devices: VitalScope[]
  pages: VitalScope[]
}

export interface Share {
  slug: string
  site_id: string
  has_password: boolean
  show_revenue: boolean
  created_at: string
  last_seen_at: string
  url: string
}

export type AlertKind = 'spike' | 'drop' | 'threshold' | 'digest'
export type AlertChannel = 'webhook' | 'email'
export type AlertMetric = 'visitors' | 'pageviews' | 'revenue'

export interface Alert {
  id: string
  /** Empty covers every site. */
  site_id: string
  kind: AlertKind
  metric: AlertMetric
  window: string
  threshold: number
  channel: AlertChannel
  destination: string
  enabled: boolean
  cooldown_min: number
  last_fired: string
  last_error: string
  created_at: string
}

export type ImportFormat = 'plausible' | 'ga4' | 'fathom' | 'umami' | 'glance'
export const IMPORT_FORMATS: { id: ImportFormat; label: string; hint: string }[] = [
  { id: 'plausible', label: 'Plausible', hint: 'the CSV export, zipped or a single file' },
  { id: 'ga4', label: 'Google Analytics 4', hint: 'a report CSV with Date as a dimension' },
  { id: 'fathom', label: 'Fathom', hint: 'the CSV export' },
  { id: 'umami', label: 'Umami', hint: 'the CSV export' },
  { id: 'glance', label: 'Glance', hint: "another Glance instance's export" },
]

export interface ImportResult {
  format: string
  days: number
  rows: number
  visitors: number
  pageviews: number
  from: string
  to: string
  dimensions: string[]
  warnings: string[]
}

export type PaymentProvider = 'polar' | 'stripe'

export interface PaymentConnection {
  site_id: string
  provider: PaymentProvider
  server: string
  product_ids: string
  has_webhook_secret: boolean
  connected_at: string
  synced_at: string
  sync_error: string
}

export interface PaymentStatus {
  provider: PaymentProvider
  connected: boolean
  connection?: PaymentConnection
  webhook_url: string
}

export interface PaymentsView {
  providers: PaymentStatus[]
  orders: number
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

let onLoginRequired: (() => void) | null = null
export function setLoginHandler(fn: (() => void) | null) {
  onLoginRequired = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let parsed: any = null
  try {
    parsed = text ? JSON.parse(text) : null
  } catch {
    parsed = null
  }
  if (!res.ok) {
    if (res.status === 401 && parsed?.error === 'login_required') onLoginRequired?.()
    throw new ApiError(res.status, parsed?.error ?? 'error', parsed?.message ?? `Request failed (${res.status})`)
  }
  return parsed as T
}

export const api = {
  me: () => request<AuthState>('GET', '/api/v1/auth/me'),
  login: (username: string, password: string) => request<AuthState>('POST', '/api/v1/auth/login', { username, password }),
  logout: () => request<void>('POST', '/api/v1/auth/logout'),
  changePassword: (input: { username?: string; current_password: string; new_password: string }) =>
    request<{ status: string; username: string; signed_out: boolean }>('POST', '/api/v1/auth/password', input),

  sites: () => request<{ sites: Site[] }>('GET', '/api/v1/sites'),
  site: (id: string) => request<Site>('GET', `/api/v1/sites/${id}`),
  createSite: (input: { name?: string; domain: string }) => request<Site>('POST', '/api/v1/sites', input),
  updateSite: (id: string, patch: Partial<Pick<Site, 'name' | 'domain' | 'home_country' | 'accent' | 'default_range' | 'domains' | 'exclude_paths' | 'exclude_ips' | 'exclude_local_traffic'>>) => request<Site>('PATCH', `/api/v1/sites/${id}`, patch),
  deleteSite: (id: string) => request<void>('DELETE', `/api/v1/sites/${id}`),
  reorderSites: (ids: string[]) => request<{ sites: Site[] }>('POST', '/api/v1/sites/reorder', { ids }),
  refreshFavicon: (id: string) => request<Site>('POST', `/api/v1/sites/${id}/refresh-favicon`),
  live: (id: string) => request<Live>('GET', `/api/v1/sites/${id}/live`),
  breakdown: (id: string, dim: Dim, range: Range, filters: Filters = {}) => request<{ dim: Dim; range: Range; rows: Row[] }>('GET', `/api/v1/sites/${id}/breakdown?dim=${dim}&range=${range}&limit=500${filterQuery(filters)}`),
  // An empty range asks the server for the site's own default; the answer says which it used.
  stats: (id: string, range: Range | '', filters: Filters = {}) => request<{ site: Site; live: number; stats: Summary }>('GET', `/api/v1/sites/${id}/stats?range=${range}${filterQuery(filters)}`),
  status: () => request<Status>('GET', '/api/v1/status'),
  diagnostics: () => request<CollectorDiagnostics>('GET', '/api/v1/diagnostics'),
  theme: () => request<{ accent: string; title: string }>('GET', '/api/v1/theme'),
  settings: () => request<GeneralSettings>('GET', '/api/v1/settings'),
  updateSettings: (patch: Partial<Omit<GeneralSettings, 'retention_from_env'>>) => request<GeneralSettings>('PATCH', '/api/v1/settings', patch),
  tokens: () => request<{ tokens: Token[]; env_token_set: boolean; scopes: string[] }>('GET', '/api/v1/tokens'),
  createToken: (name: string, scope: 'read' | 'write' = 'read') => request<{ token: Token; secret: string }>('POST', '/api/v1/tokens', { name, scope }),
  deleteToken: (id: string) => request<void>('DELETE', `/api/v1/tokens/${id}`),
  rollup: () => request<void>('POST', '/api/v1/rollup'),

  google: (id: string) => request<{ status: GoogleStatus; redirect_uri: string }>('GET', `/api/v1/sites/${id}/google`),
  googleSetProperty: (id: string, property: string) => request<{ status: GoogleStatus; redirect_uri: string }>('PATCH', `/api/v1/sites/${id}/google`, { property }),
  googleDisconnect: (id: string) => request<void>('DELETE', `/api/v1/sites/${id}/google`),
  googleSync: (id: string) => request<{ status: GoogleStatus; redirect_uri: string }>('POST', `/api/v1/sites/${id}/google/sync`),
  searchTerms: (id: string, range: Range) => request<{ range: Range; rows: SearchTerm[] }>('GET', `/api/v1/sites/${id}/search-terms?range=${range}&limit=500`),
  /** Starting the OAuth flow is a POST, so the URL comes back for the page to
   *  navigate to rather than as a redirect. */
  googleConnect: (id: string) => request<{ url: string }>('POST', `/api/v1/sites/${id}/google/connect`),

  goals: (id: string, range: Range) => request<{ range: Range; visitors: number; goals: GoalResult[] }>('GET', `/api/v1/sites/${id}/goals?range=${range}`),
  createGoal: (id: string, input: { name?: string; kind?: string; target: string; value?: number }) => request<Goal>('POST', `/api/v1/sites/${id}/goals`, input),
  updateGoal: (id: string, goal: string, patch: Partial<Pick<Goal, 'name' | 'kind' | 'target' | 'value'>>) => request<Goal>('PATCH', `/api/v1/sites/${id}/goals/${goal}`, patch),
  deleteGoal: (id: string, goal: string) => request<void>('DELETE', `/api/v1/sites/${id}/goals/${goal}`),

  funnels: (id: string, range: Range) => request<{ range: Range; funnels: FunnelResult[] }>('GET', `/api/v1/sites/${id}/funnels?range=${range}`),
  createFunnel: (id: string, input: { name?: string; steps: FunnelStep[] }) => request<Funnel>('POST', `/api/v1/sites/${id}/funnels`, input),
  updateFunnel: (id: string, funnel: string, patch: { name?: string; steps?: FunnelStep[] }) => request<Funnel>('PATCH', `/api/v1/sites/${id}/funnels/${funnel}`, patch),
  deleteFunnel: (id: string, funnel: string) => request<void>('DELETE', `/api/v1/sites/${id}/funnels/${funnel}`),

  notes: (id: string, range: Range) => request<{ notes: Note[] }>('GET', `/api/v1/sites/${id}/notes?range=${range}`),
  createNote: (id: string, input: { text: string; day?: string }) => request<Note>('POST', `/api/v1/sites/${id}/notes`, input),
  updateNote: (id: string, note: string, patch: { text?: string; day?: string }) => request<Note>('PATCH', `/api/v1/sites/${id}/notes/${note}`, patch),
  deleteNote: (id: string, note: string) => request<void>('DELETE', `/api/v1/sites/${id}/notes/${note}`),

  vitals: (id: string, range: Range, limit = 10) => request<{ vitals: Vitals }>('GET', `/api/v1/sites/${id}/vitals?range=${range}&limit=${limit}`),

  shares: (id: string) => request<{ shares: Share[] }>('GET', `/api/v1/sites/${id}/shares`),
  createShare: (id: string, input: { password?: string; show_revenue?: boolean } = {}) => request<Share>('POST', `/api/v1/sites/${id}/shares`, input),
  updateShare: (id: string, slug: string, patch: { password?: string; show_revenue?: boolean }) => request<Share>('PATCH', `/api/v1/sites/${id}/shares/${slug}`, patch),
  deleteShare: (id: string, slug: string) => request<void>('DELETE', `/api/v1/sites/${id}/shares/${slug}`),

  alerts: () => request<{ alerts: Alert[]; email_configured: boolean; windows: string[] }>('GET', '/api/v1/alerts'),
  createAlert: (input: Partial<Alert>) => request<Alert>('POST', '/api/v1/alerts', input),
  updateAlert: (id: string, patch: Partial<Alert>) => request<Alert>('PATCH', `/api/v1/alerts/${id}`, patch),
  deleteAlert: (id: string) => request<void>('DELETE', `/api/v1/alerts/${id}`),
  testAlert: (id: string) => request<{ status: string; channel: string; destination: string }>('POST', `/api/v1/alerts/${id}/test`),

  /** Uploads an export file as the raw body; not JSON, so it bypasses request(). */
  async importData(id: string, format: ImportFormat, file: File): Promise<ImportResult> {
    const res = await fetch(`/api/v1/sites/${id}/import?format=${format}`, { method: 'POST', body: file })
    const text = await res.text()
    let parsed: any = null
    try {
      parsed = text ? JSON.parse(text) : null
    } catch {
      parsed = null
    }
    if (!res.ok) throw new ApiError(res.status, parsed?.error ?? 'error', parsed?.message ?? `Import failed (${res.status})`)
    return parsed as ImportResult
  },
}

export const paymentsApi = {
  status: (id: string) => request<PaymentsView>('GET', `/api/v1/sites/${id}/payments`),
  connect: (id: string, provider: PaymentProvider, input: { access_token?: string; server?: string; product_ids?: string; webhook_secret?: string }) =>
    request<PaymentsView>('PUT', `/api/v1/sites/${id}/payments/${provider}`, input),
  disconnect: (id: string, provider: PaymentProvider) => request<void>('DELETE', `/api/v1/sites/${id}/payments/${provider}`),
  sync: (id: string, provider: PaymentProvider) => request<PaymentsView>('POST', `/api/v1/sites/${id}/payments/${provider}/sync`),
  revenue: (id: string, range: Range, limit = 10) => request<Revenue>('GET', `/api/v1/sites/${id}/revenue?range=${range}&limit=${limit}`),
}

/** URL of a site's stored icon (404 when none). */
export const siteIconURL = (id: string) => `/api/v1/sites/${id}/favicon`
/** URL of a cached referrer icon (404 when none). */
export const refIconURL = (host: string) => `/api/v1/favicon?host=${encodeURIComponent(host)}`
