<script lang="ts">
  // Per-site dashboard: metrics, chart, breakdowns, world map, settings.
  import { api, DEFAULT_RANGE, isRange, paymentsApi, RANGES, refIconURL, siteIconURL, type Dim, type Filters, type FunnelResult, type GoalResult, type GoogleStatus, type Live, type Note, type PaymentProvider, type PaymentsView, type Range, type Revenue, type RevenueDim, type Row, type SearchTerm, IMPORT_FORMATS, type Share, type ImportFormat, type ImportResult, type Site, type Summary, type Vitals } from '../lib/api'
  import { setAccentOverride } from '../lib/accent'
  import { copyText } from '../lib/clipboard'
  import { countryName, flag, fmtAgo, fmtDelta, fmtMoney, fmtNum, fmtRatio } from '../lib/format'
  import { pageIn, panel } from '../lib/motion'
  import Icon from '../lib/ui/Icon.svelte'
  import Segment from '../lib/ui/Segment.svelte'
  import Swatches from '../lib/ui/Swatches.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import MetricStat from '../lib/ui/MetricStat.svelte'
  import BarList, { type BarRow } from '../lib/ui/BarList.svelte'
  import Manage from '../lib/ui/Manage.svelte'
  import Funnels from '../lib/ui/Funnels.svelte'
  import VitalsCard from '../lib/ui/Vitals.svelte'
  import AreaChart from '../lib/ui/AreaChart.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import BrandIcon from '../lib/ui/BrandIcon.svelte'
  import Realtime from '../lib/ui/Realtime.svelte'

  let { id }: { id: string } = $props()
  let site = $state<Site | null>(null)
  let stats = $state<Summary | null>(null)
  let live = $state(0)
  // The dashboard opens on the range saved against the site. The first stats
  // call leaves the range out and the server answers with the one it used, so
  // learning the site's default costs no extra round trip.
  let range = $state<Range | ''>('')
  let error = $state('')
  // Click-to-filter: dimension to key, mirrored into the URL so back and
  // share work. Filtered views come from raw events, so the server may
  // truncate them to the retention window.
  const DIMS: Dim[] = ['page', 'ref', 'country', 'region', 'device', 'browser', 'os', 'event', 'utm_source', 'utm_campaign']
  function filtersFromURL(): Filters {
    const q = new URLSearchParams(location.search)
    const f: Filters = {}
    for (const d of DIMS) if (q.has(d)) f[d] = q.get(d) ?? ''
    return f
  }
  let filters = $state<Filters>(filtersFromURL())
  const hasFilters = $derived(Object.keys(filters).length > 0)
  function setFilters(next: Filters) {
    filters = next
    const q = new URLSearchParams(location.search)
    for (const d of DIMS) q.delete(d)
    for (const [d, k] of Object.entries(next)) if (k !== undefined) q.set(d, k)
    const s = q.toString()
    history.pushState(null, '', location.pathname + (s ? '?' + s : ''))
  }
  function toggleFilter(dim: Dim, key: string) {
    const next = { ...filters }
    if (next[dim] === key) delete next[dim]
    else next[dim] = key
    setFilters(next)
  }
  const select = (dim: Dim) => (r: BarRow) => toggleFilter(dim, r.key === '∅' ? '' : r.key === 'direct' ? '' : r.key === 'XX' ? '' : r.key)
  $effect(() => {
    const onPop = () => (filters = filtersFromURL())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  })
  const filterLabel = (dim: Dim, key: string) =>
    dim === 'country' ? countryName(key) || 'Unknown' : dim === 'ref' ? key || 'Direct' : key || 'Unknown'
  const FILTER_DIM: Record<Dim, string> = {
    page: 'Page', ref: 'Referrer', utm_source: 'Source', utm_campaign: 'Campaign', utm_medium: 'Medium',
    country: 'Country', region: 'Region', city: 'City',
    browser: 'Browser', device: 'Device', os: 'OS', event: 'Event', prop: 'Property',
    bot: 'Crawler', aibot: 'AI crawler',
  }
  const selectedKey = (dim: Dim) => (filters[dim] === undefined ? undefined : filters[dim] === '' ? (dim === 'ref' ? 'direct' : dim === 'country' ? 'XX' : '∅') : filters[dim])
  let settingsOpen = $state(false)
  let copied = $state(false)
  // The map pulls in MapLibre; load it only once the dashboard is up.
  let WorldMap = $state<typeof import('../lib/map/WorldMap.svelte').default | null>(null)
  let LiveMap = $state<typeof import('../lib/map/LiveMap.svelte').default | null>(null)
  let mapView = $state<'live' | 'range'>('live')
  let liveData = $state<Live | null>(null)
  const online = $derived(liveData?.total ?? live)
  const lastActivity = $derived(liveData?.last_activity ? fmtAgo(liveData.last_activity) : '')

  // Google Search Console: connection status for the settings panel and
  // the search terms it feeds. Google's data trails by two to three days.
  let google = $state<GoogleStatus | null>(null)
  let googleRedirect = $state('')
  let googleBusy = $state(false)
  let googleNotice = $state('')
  let terms = $state<SearchTerm[]>([])

  async function loadGoogle() {
    try {
      const r = await api.google(id)
      google = r.status
      googleRedirect = r.redirect_uri
    } catch (e: any) {
      google = null
    }
  }
  async function loadTerms() {
    if (!google?.connected || !range) {
      terms = []
      return
    }
    try {
      terms = (await api.searchTerms(id, range)).rows
    } catch {
      terms = []
    }
  }
  async function googleAction(run: () => Promise<unknown>) {
    googleBusy = true
    try {
      await run()
      await loadGoogle()
      await loadTerms()
    } catch (e: any) {
      error = e.message
    } finally {
      googleBusy = false
    }
  }
  // Connecting is a POST that answers with Google's authorize URL, so the
  // browser is sent there from here rather than by following a link. A link
  // would be a cross-site-triggerable GET that links an account to a site.
  async function connectGoogle() {
    googleBusy = true
    try {
      const { url } = await api.googleConnect(id)
      window.location.href = url
    } catch (e: any) {
      error = e.message
      googleBusy = false
    }
  }
  function disconnectGoogle() {
    if (!confirm('Disconnect Google Search Console? Stored search terms for this site are deleted.')) return
    googleAction(() => api.googleDisconnect(id))
  }
  // The callback lands here with ?google=connected or ?google_error=…
  $effect(() => {
    const params = new URLSearchParams(location.search)
    const err = params.get('google_error')
    if (params.get('google') === 'connected') {
      googleNotice = 'Google Search Console connected. The first pull can take a minute.'
      settingsOpen = true
    } else if (err) {
      error = 'Google: ' + err
      settingsOpen = true
    }
    if (params.has('google') || params.has('google_error')) history.replaceState(null, '', location.pathname)
    loadGoogle()
  })
  $effect(() => {
    range
    google?.connected
    loadTerms()
  })
  // The card shows the same top 10 as the other breakdowns; the modal has everything.
  const TOP_TERMS = 10
  // Polar: revenue next to traffic, attributed to first touch when the
  // site passes it into checkout metadata.
  // Payments cover several processors, so the panel is generic: one block per
  // provider, driven by the provider's own status and form state.
  let payments = $state<PaymentsView | null>(null)
  const PROVIDERS: { id: PaymentProvider; label: string; tokenHint: string; tokenPlaceholder: string; help: string }[] = [
    {
      id: 'polar',
      label: 'Polar',
      tokenHint: 'organization access token (Settings, Developers) with the orders:read scope',
      tokenPlaceholder: 'polar_oat_…',
      help: 'https://api.polar.sh',
    },
    {
      id: 'stripe',
      label: 'Stripe',
      tokenHint: 'secret or restricted key with read access to charges',
      tokenPlaceholder: 'rk_live_… or sk_live_…',
      help: 'https://api.stripe.com',
    },
  ]
  const providerOf = (id: PaymentProvider) => payments?.providers.find((p) => p.provider === id)
  const anyConnected = $derived((payments?.providers ?? []).some((p) => p.connected))
  let openProvider = $state<PaymentProvider | null>(null)
  let forms = $state<Record<PaymentProvider, { access_token: string; server: string; product_ids: string; webhook_secret: string }>>({
    polar: { access_token: '', server: '', product_ids: '', webhook_secret: '' },
    stripe: { access_token: '', server: '', product_ids: '', webhook_secret: '' },
  })
  let revenue = $state<Revenue | null>(null)
  let polarBusy = $state(false)
  let revenueTab = $state<RevenueDim>('ref')

  async function loadPolar() {
    try {
      payments = await paymentsApi.status(id)
      for (const p of payments.providers) {
        if (p.connection) {
          forms[p.provider] = { access_token: '', server: p.connection.server, product_ids: p.connection.product_ids, webhook_secret: '' }
        }
      }
    } catch {
      payments = null
    }
  }
  async function loadRevenue() {
    if (!anyConnected || !range) {
      revenue = null
      return
    }
    try {
      revenue = await paymentsApi.revenue(id, range)
    } catch {
      revenue = null
    }
  }
  async function polarAction(run: () => Promise<unknown>) {
    polarBusy = true
    try {
      await run()
      error = ''
      openProvider = null
      await loadPolar()
      await loadRevenue()
    } catch (e: any) {
      error = e.message
    } finally {
      polarBusy = false
    }
  }
  function saveProvider(provider: PaymentProvider) {
    const form = forms[provider]
    const input: Parameters<typeof paymentsApi.connect>[2] = { server: form.server, product_ids: form.product_ids }
    // A blank secret means "keep what is stored", so it is not sent at all.
    if (form.access_token.trim()) input.access_token = form.access_token.trim()
    if (form.webhook_secret.trim()) input.webhook_secret = form.webhook_secret.trim()
    polarAction(() => paymentsApi.connect(id, provider, input))
  }
  function disconnectProvider(provider: PaymentProvider) {
    const label = PROVIDERS.find((p) => p.id === provider)?.label ?? provider
    if (!confirm(`Disconnect ${label}? Its stored orders for this site are deleted.`)) return
    polarAction(() => paymentsApi.disconnect(id, provider))
  }
  $effect(() => {
    loadPolar()
  })
  $effect(() => {
    range
    anyConnected
    loadRevenue()
  })
  const REVENUE_DIMS: { value: RevenueDim; label: string }[] = [
    { value: 'ref', label: 'Referrer' }, { value: 'source', label: 'Source' }, { value: 'campaign', label: 'Campaign' },
    { value: 'landing', label: 'Landing' }, { value: 'country', label: 'Country' }, { value: 'product', label: 'Product' },
    { value: 'provider', label: 'Processor' },
  ]
  const revenueRows = $derived(
    (revenue?.breakdowns[revenueTab] ?? []).map((r) => ({
      key: r.key || '∅',
      label: revenueTab === 'country' ? countryName(r.key) || 'Unknown' : revenueTab === 'ref' ? r.key || 'Direct or unattributed' : r.key || 'Unattributed',
      value: r.revenue,
      prefix: revenueTab === 'country' ? flag(r.key) : undefined,
      icon: revenueTab === 'ref' && r.key ? refIconURL(r.key) : '',
      direct: revenueTab === 'ref' && r.key === '',
      title: `${r.orders} ${r.orders === 1 ? 'order' : 'orders'}`,
    })),
  )
  const money = $derived((v: number) => fmtMoney(v, revenue?.currency ?? ''))
  const revenueSeries = $derived(revenue && stats && revenue.series.length === stats.series.length ? revenue.series.map((p) => p.revenue) : [])

  const termRows = $derived(
    terms.map((t) => ({
      key: t.query,
      label: t.query,
      value: t.clicks,
      title: `${fmtNum(t.impressions)} impressions · position ${t.position.toFixed(1)}`,
    })),
  )

  // The window already asked for, so resolving the opening range below does
  // not fire the same query a second time.
  let requested = ''
  const windowKey = () => JSON.stringify([range, filters])

  async function load() {
    try {
      const r = await api.stats(id, range, filters)
      site = r.site
      stats = r.stats
      live = r.live
      error = ''
      document.title = `${r.site.name} · Glance`
      if (!range) {
        range = isRange(r.stats.range) ? r.stats.range : DEFAULT_RANGE
        requested = windowKey() // this load already covered it
      }
    } catch (e: any) {
      error = e.message
    }
  }
  $effect(() => {
    const key = windowKey()
    if (key === requested) return
    requested = key
    load()
  })
  // A site's own colour takes over the whole UI while its dashboard is open,
  // and hands back to the account-wide accent on the way out.
  $effect(() => {
    setAccentOverride(site?.accent ?? '')
    return () => setAccentOverride('')
  })
  $effect(() => {
    const t = setInterval(load, 60_000)
    import('../lib/map/WorldMap.svelte').then((m) => (WorldMap = m.default)).catch(() => {})
    import('../lib/map/LiveMap.svelte').then((m) => (LiveMap = m.default)).catch(() => {})
    return () => clearInterval(t)
  })
  // Live snapshot every 5s: feeds the realtime card and the live globe.
  $effect(() => {
    const poll = () => api.live(id).then((l) => (liveData = l)).catch(() => {})
    poll()
    const t = setInterval(poll, 5000)
    return () => clearInterval(t)
  })

  // One mapping per dimension so the cards and the "view all" modal agree.
  const toRows = (dim: Dim, src: Row[]): BarRow[] => {
    const sorted = [...src].sort((a, b) => (dim === 'event' ? b.pageviews - a.pageviews : b.visitors - a.visitors))
    switch (dim) {
      case 'ref':
        return sorted.map((r) => ({ key: r.key || 'direct', label: r.key || 'Direct', value: r.visitors, direct: r.key === '', icon: r.key ? refIconURL(r.key) : '', title: `${fmtNum(r.pageviews)} views` }))
      case 'country':
        return sorted.map((r) => ({ key: r.key || 'XX', label: countryName(r.key), value: r.visitors, prefix: flag(r.key), title: `${fmtNum(r.pageviews)} views` }))
      case 'event':
      case 'prop':
        return sorted.map((r) => ({ key: r.key, label: r.key, value: r.pageviews, title: `${fmtNum(r.visitors)} visitors${r.value ? ` · ${money(r.value)}` : ''}` }))
      case 'bot':
      case 'aibot':
        // Crawlers are never counted as visitors, so a crawler row measured in
        // visitors would read zero for every one of them. Requests is the only
        // number these rows have.
        return sorted.map((r) => ({ key: r.key, label: r.key, value: r.pageviews, title: `${fmtNum(r.pageviews)} requests, never counted as visitors` }))
      default:
        return sorted.map((r) => ({ key: r.key || '∅', label: r.key || 'Unknown', value: r.visitors, title: `${fmtNum(r.pageviews)} views` }))
    }
  }
  const DIM_TITLE: Record<Dim, string> = {
    page: 'Pages', ref: 'Referrers', utm_source: 'Sources', utm_campaign: 'Campaigns', utm_medium: 'Mediums',
    country: 'Countries', region: 'Regions', city: 'Cities',
    browser: 'Browsers', device: 'Devices', os: 'Operating systems', event: 'Events', prop: 'Properties',
    bot: 'Crawlers', aibot: 'AI crawlers',
  }
  // Card tabs, as in the reference: one card per group, a dimension per tab.
  type SourceTab = 'ref' | 'utm_source' | 'utm_campaign' | 'utm_medium'
  type LocationTab = 'country' | 'region' | 'city'
  type EventTab = 'event' | 'prop'
  let sourceTab = $state<SourceTab>('ref')
  let locationTab = $state<LocationTab>('country')
  let deviceTab = $state<'browser' | 'os' | 'device'>('browser')
  let eventTab = $state<EventTab>('event')
  let crawlerTab = $state<'bot' | 'aibot'>('bot')
  // Tabs whose dimension only exists once there is data in it: a "Cities" tab
  // on an instance with no GeoIP database, or "Properties" with no event
  // properties, would just be an empty list to click on.
  const sourceTabs = $derived<{ value: SourceTab; label: string }[]>([
    { value: 'ref', label: 'Referrer' },
    { value: 'utm_source', label: 'Source' },
    { value: 'utm_campaign', label: 'Campaign' },
    ...(stats?.breakdowns.utm_medium?.length ? [{ value: 'utm_medium' as const, label: 'Medium' }] : []),
  ])
  const locationTabs = $derived<{ value: LocationTab; label: string }[]>([
    { value: 'country', label: 'Countries' },
    { value: 'region', label: 'Regions' },
    ...(stats?.breakdowns.city?.length ? [{ value: 'city' as const, label: 'Cities' }] : []),
  ])
  const eventTabs = $derived<{ value: EventTab; label: string }[]>([
    { value: 'event', label: 'Events' },
    ...(stats?.breakdowns.prop?.length ? [{ value: 'prop' as const, label: 'Properties' }] : []),
  ])
  const rowsFor = (dim: Dim) => toRows(dim, stats?.breakdowns[dim] ?? [])
  const pages = $derived(rowsFor('page'))
  const sources = $derived(rowsFor(sourceTab))
  const locations = $derived(rowsFor(locationTab))
  const devices = $derived(rowsFor(deviceTab))
  const crawlers = $derived(rowsFor(crawlerTab))

  // Goals, funnels, vitals and notes are loaded alongside the summary rather
  // than inside it: each is optional, and a site with none should not pay for
  // the queries.
  let goalResults = $state<GoalResult[]>([])
  let funnelResults = $state<FunnelResult[]>([])
  let vitals = $state<Vitals | null>(null)
  let notes = $state<Note[]>([])
  // The bar shows the conversion rate; the count and value go in the tooltip,
  // because a rate is what a goal is for.
  const goalRows = $derived<BarRow[]>(
    goalResults.map((g) => ({
      key: g.id,
      label: g.name,
      value: g.rate,
      title: `${fmtNum(g.conversions)} of ${fmtNum(goalVisitors)} visitors${g.value ? ` · ${money(g.value)}` : ''}`,
    })),
  )
  let goalVisitors = $state(0)
  async function loadFeatures() {
    if (!range) return
    // A failure in any one of these must not blank the dashboard, so each is
    // settled independently and falls back to empty.
    const [g, f, v, n] = await Promise.allSettled([
      api.goals(id, range),
      api.funnels(id, range),
      api.vitals(id, range),
      api.notes(id, range),
    ])
    goalResults = g.status === 'fulfilled' ? g.value.goals : []
    goalVisitors = g.status === 'fulfilled' ? g.value.visitors : 0
    funnelResults = f.status === 'fulfilled' ? f.value.funnels : []
    vitals = v.status === 'fulfilled' ? v.value.vitals : null
    notes = n.status === 'fulfilled' ? n.value.notes : []
  }
  $effect(() => {
    range
    loadFeatures()
  })

  // ---- management: goals, funnels, notes, shares, import ----
  let manageBusy = $state(false)
  let shares = $state<Share[]>([])
  let goalForm = $state({ name: '', target: '', value: '' })
  let funnelForm = $state({ name: '', steps: '' })
  let noteForm = $state({ day: '', text: '' })
  let importFormat = $state<ImportFormat>('plausible')
  let importFile = $state<File | null>(null)
  let importResult = $state<ImportResult | null>(null)

  async function manage(run: () => Promise<unknown>) {
    manageBusy = true
    try {
      await run()
      error = ''
      await loadFeatures()
      await loadShares()
    } catch (e: any) {
      error = e.message
    } finally {
      manageBusy = false
    }
  }
  async function loadShares() {
    try {
      shares = (await api.shares(id)).shares
    } catch {
      shares = []
    }
  }
  $effect(() => {
    loadShares()
  })

  const addGoal = () =>
    manage(async () => {
      await api.createGoal(id, {
        name: goalForm.name.trim() || undefined,
        target: goalForm.target.trim(),
        value: goalForm.value.trim() ? Math.round(Number(goalForm.value) * 100) : undefined,
      })
      goalForm = { name: '', target: '', value: '' }
    })
  // Steps are typed one per line: a repeater for three fields is more UI than
  // the task needs, and a path or an event name is already one line of text.
  const addFunnel = () =>
    manage(async () => {
      const steps = funnelForm.steps
        .split('\n')
        .map((l) => l.trim())
        .filter(Boolean)
        .map((target) => ({ name: target, kind: target.startsWith('/') ? ('path' as const) : ('event' as const), target }))
      await api.createFunnel(id, { name: funnelForm.name.trim() || undefined, steps })
      funnelForm = { name: '', steps: '' }
    })
  const addNote = () =>
    manage(async () => {
      await api.createNote(id, { text: noteForm.text.trim(), day: noteForm.day.trim() || undefined })
      noteForm = { day: '', text: '' }
    })
  const addShare = () => manage(() => api.createShare(id))
  const runImport = () =>
    manage(async () => {
      if (!importFile) return
      importResult = await api.importData(id, importFormat, importFile)
      importFile = null
    })
  const events = $derived(rowsFor('event'))

  // "View all" modal.
  let modal = $state<Dim | 'search' | null>(null)
  let modalRows = $state<BarRow[] | null>(null)
  let filter = $state('')
  function openAll(dim: Dim | 'search') {
    modal = dim
    modalRows = null
    filter = ''
    if (dim === 'search') {
      modalRows = termRows
      return
    }
    api
      .breakdown(id, dim, range || DEFAULT_RANGE, filters)
      .then((r) => (modalRows = toRows(dim, r.rows)))
      .catch((e: any) => (error = e.message))
  }
  const filtered = $derived((modalRows ?? []).filter((r) => !filter || r.label.toLowerCase().includes(filter.toLowerCase())))
  const more = (dim: Dim | 'search') => () => openAll(dim)
  const topCountry = $derived(stats?.breakdowns.country?.[0]?.key ?? '')

  const snippet = $derived(site ? `<script defer src="${location.origin}/glance.js" data-site="${site.id}"><\/script>` : '')
  async function copy() {
    if (await copyText(snippet)) {
      copied = true
      setTimeout(() => (copied = false), 1500)
      return
    }
    // Better to say so than to leave the button looking broken.
    error = 'Could not copy automatically. Select the snippet and copy it manually.'
  }
  let timers: Record<string, ReturnType<typeof setTimeout>> = {}
  function save(patch: Parameters<typeof api.updateSite>[1], key: string) {
    clearTimeout(timers[key])
    timers[key] = setTimeout(() => {
      api
        .updateSite(id, patch)
        .then((s) => (site = { ...site!, ...s }))
        .catch((e: any) => (error = e.message))
    }, 400)
  }
  /** Per-site accent; '' hands the page back to the account-wide colour. */
  function pickAccent(hex: string) {
    setAccentOverride(hex) // instant preview
    save({ accent: hex }, 'accent')
  }
  /** The range this dashboard opens on, and the one it switches to now. */
  function pickDefaultRange(r: Range) {
    range = r
    save({ default_range: r }, 'default_range')
  }
</script>

{#if site && stats}
  <div class="title">
    <Icon src={site.has_favicon ? siteIconURL(site.id) : ''} size={22} />
    <span class="name">{site.name}</span>
    <span class="domain">{site.domain}</span>
    <span class="live" class:idle={online === 0} title={liveData?.last_activity ? `Last human activity ${new Date(liveData.last_activity).toLocaleString()}` : 'No human activity recorded yet'}>
      <span class="dot"></span>{online} online <span class="last">· {lastActivity ? `last activity ${lastActivity}` : 'no activity yet'}</span>
    </span>
    <span class="spacer"></span>
    <div class="ranges"><Segment options={RANGES.map((r) => ({ value: r, label: r }))} value={range || DEFAULT_RANGE} gap={14} onchange={(r) => (range = r)} /></div>
    <button type="button" class="plain" class:on={settingsOpen} onclick={() => (settingsOpen = !settingsOpen)}>Settings</button>
  </div>

  {#if settingsOpen}
    <div class="settings" transition:panel>
      <div class="setting">
        <div class="text"><div class="label">Tracking code</div><div class="hint">Paste before the closing &lt;/head&gt; tag. No cookies, no consent banner needed.</div></div>
      </div>
      <div class="code">
        <span class="snippet">{snippet}</span>
        <button type="button" class="copy" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
      </div>
      <div class="setting">
        <div class="text"><div class="label">Name</div></div>
        <div class="ctl"><Input value={site.name} aria-label="Name" oninput={(e) => save({ name: e.currentTarget.value }, 'name')} /></div>
      </div>
      <div class="setting">
        <div class="text"><div class="label">Domain</div><div class="hint">Events from other hosts are ignored</div></div>
        <div class="ctl"><Input value={site.domain} aria-label="Domain" oninput={(e) => save({ domain: e.currentTarget.value }, 'domain')} /></div>
      </div>
      <div class="setting">
        <div class="text">
          <div class="label">Ignore local development traffic</div>
          <div class="hint">Drops future visits from localhost, loopback, private networks, and .local or .test hosts before they reach analytics.</div>
        </div>
        <Switch checked={site.exclude_local_traffic} label="Ignore local development traffic" onchange={(v) => save({ exclude_local_traffic: v }, 'exclude_local_traffic')} />
      </div>
      <div class="setting">
        <div class="text"><div class="label">Default date range</div><div class="hint">The range this dashboard opens on, remembered for {site.name}</div></div>
        <div class="ranges-setting"><Segment options={RANGES.map((r) => ({ value: r, label: r }))} value={(site.default_range || DEFAULT_RANGE) as Range} gap={14} onchange={pickDefaultRange} /></div>
      </div>
      <div class="setting">
        <div class="text"><div class="label">Accent colour</div><div class="hint">Replaces the account colour while you are looking at this site</div></div>
        <Swatches value={site.accent} inherit="Account colour" onpick={pickAccent} />
      </div>
      <div class="setting">
        <div class="text"><div class="label">Home country</div><div class="hint">Where the map arcs converge. Blank uses your top country ({countryName(topCountry) || 'none yet'}).</div></div>
        <div class="ctl short"><Input value={site.home_country} placeholder={topCountry || 'GB'} maxlength={2} aria-label="Home country" oninput={(e) => save({ home_country: e.currentTarget.value.toUpperCase() }, 'home')} /></div>
      </div>
      <div class="setting">
        <div class="text"><div class="label">Favicon</div><div class="hint">Fetched from your site by Glance, never from a third party</div></div>
        <Button variant="secondary" size="sm" onclick={() => api.refreshFavicon(id).then((s) => (site = { ...site!, ...s }))}>Refresh</Button>
      </div>

      <div class="setting google">
        <Manage
          title="Goals"
          hint="An event name, or a page path starting with /. A trailing * matches a prefix."
          items={goalResults}
          empty="No goals yet."
          busy={manageBusy}
          addLabel="Add goal"
          canAdd={goalForm.target.trim() !== ''}
          onadd={addGoal}
          onremove={(g) => manage(() => api.deleteGoal(id, g))}
        >
          {#snippet row(item)}
            {@const g = goalResults.find((x) => x.id === item.id)!}
            <span>{g.name}</span>
            <span class="quiet"> · {g.kind === 'path' ? g.target : `event ${g.target}`} · {fmtNum(g.conversions)} in {range}</span>
          {/snippet}
          {#snippet form()}
            <Input bind:value={goalForm.target} placeholder="signup, or /thanks" aria-label="Goal target" mono />
            <Input bind:value={goalForm.name} placeholder="Display name (optional)" aria-label="Goal name" />
            <Input bind:value={goalForm.value} placeholder="Worth per conversion, e.g. 19.00 (optional)" aria-label="Goal value" />
          {/snippet}
        </Manage>
      </div>

      <div class="setting google">
        <Manage
          title="Funnels"
          hint="Two to eight steps, one per line, in order. A line starting with / is a page, anything else an event."
          items={funnelResults}
          empty="No funnels yet."
          busy={manageBusy}
          addLabel="Add funnel"
          canAdd={funnelForm.steps.split('\n').filter((l) => l.trim()).length >= 2}
          onadd={addFunnel}
          onremove={(f) => manage(() => api.deleteFunnel(id, f))}
        >
          {#snippet row(item)}
            {@const f = funnelResults.find((x) => x.id === item.id)!}
            <span>{f.name}</span>
            <span class="quiet"> · {f.steps.map((st) => st.target).join(' → ')}</span>
          {/snippet}
          {#snippet form()}
            <Input bind:value={funnelForm.name} placeholder="Name (optional)" aria-label="Funnel name" />
            <textarea
              bind:value={funnelForm.steps}
              rows="4"
              placeholder={'/\n/pricing\nsignup'}
              aria-label="Funnel steps, one per line"
            ></textarea>
          {/snippet}
        </Manage>
      </div>

      <div class="setting google">
        <Manage
          title="Notes"
          hint="Dated annotations, so a spike still has a reason next to it a month later."
          items={notes}
          empty="No notes in this range."
          busy={manageBusy}
          addLabel="Add note"
          canAdd={noteForm.text.trim() !== ''}
          onadd={addNote}
          onremove={(n) => manage(() => api.deleteNote(id, n))}
        >
          {#snippet row(item)}
            {@const n = notes.find((x) => x.id === item.id)!}
            <span class="mono">{n.day}</span>
            <span> {n.text}</span>
          {/snippet}
          {#snippet form()}
            <Input bind:value={noteForm.text} placeholder="Launched on Product Hunt" aria-label="Note text" />
            <Input bind:value={noteForm.day} placeholder="YYYY-MM-DD (blank = today)" aria-label="Note day" mono />
          {/snippet}
        </Manage>
      </div>

      <div class="setting google">
        <Manage
          title="Shared dashboards"
          hint="A read-only link anyone can open, with no account. The address is the credential, so treat it like one."
          items={shares.map((sh) => ({ id: sh.slug }))}
          empty="Not shared."
          busy={manageBusy}
          addLabel="Create link"
          onadd={addShare}
          onremove={(slug) => manage(() => api.deleteShare(id, slug))}
        >
          {#snippet row(item)}
            {@const sh = shares.find((x) => x.slug === item.id)!}
            <button class="prop" type="button" onclick={() => copyText(sh.url)} title="Copy">
              {sh.url}
            </button>
            {#if sh.has_password}<span class="quiet"> · password set</span>{/if}
          {/snippet}
          {#snippet form()}
            <div class="hint">Creates an unguessable link. Add a password afterwards if you need one.</div>
          {/snippet}
        </Manage>
      </div>

      <div class="setting google">
        <div class="text">
          <div class="label">Import history</div>
          <div class="hint">
            Loads another tool's export into this site's daily history, which is kept forever. Imported
            days have no hourly detail and cannot be filtered, because the export has no individual
            events in it.
          </div>
          {#if importResult}
            <div class="hint ok">
              Imported {fmtNum(importResult.days)} days, {fmtNum(importResult.visitors)} visitors and
              {fmtNum(importResult.pageviews)} pageviews ({importResult.from} to {importResult.to}).
              {#each importResult.warnings as w}<br />{w}{/each}
            </div>
          {/if}
          <div class="polar-form">
            <select bind:value={importFormat} aria-label="Import format">
              {#each IMPORT_FORMATS as f}
                <option value={f.id}>{f.label} — {f.hint}</option>
              {/each}
            </select>
            <input
              type="file"
              aria-label="Export file"
              onchange={(e) => (importFile = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)}
            />
            <div class="google-actions">
              <Button size="sm" disabled={manageBusy || !importFile} onclick={runImport}>
                {manageBusy ? 'Importing' : 'Import'}
              </Button>
            </div>
          </div>
        </div>
      </div>
      {#if payments}
        {#each PROVIDERS as provider (provider.id)}
          {@const st = providerOf(provider.id)}
          <div class="setting google">
            <div class="text">
              <div class="label">{provider.label}</div>
              {#if st?.connected && st.connection}
                <div class="hint">
                  {st.connection.server.replace('https://', '')}{#if st.connection.product_ids}
                    · {st.connection.product_ids.split(',').length} {st.connection.product_ids.includes(',') ? 'products' : 'product'}{/if}
                  · {fmtNum(payments.orders)} orders across processors
                  {#if st.connection.sync_error}
                    · <span class="bad">{st.connection.sync_error}</span>
                  {:else if st.connection.synced_at}
                    · synced {new Date(st.connection.synced_at).toLocaleString()}
                  {:else}
                    · first pull pending
                  {/if}
                  {#if !st.connection.has_webhook_secret}
                    · <span class="warn">no webhook, sales appear daily</span>
                  {/if}
                </div>
              {:else}
                <div class="hint">Show revenue next to traffic. Needs a {provider.tokenHint}.</div>
              {/if}
              {#if openProvider === provider.id}
                <div class="polar-form" transition:panel>
                  <Input
                    bind:value={forms[provider.id].access_token}
                    placeholder={st?.connected ? 'Key (leave blank to keep)' : provider.tokenPlaceholder}
                    aria-label="{provider.label} key"
                    type="password"
                    mono
                  />
                  <Input bind:value={forms[provider.id].product_ids} placeholder="Product ids, comma separated (blank = all)" aria-label="{provider.label} product ids" mono />
                  <Input
                    bind:value={forms[provider.id].webhook_secret}
                    placeholder={st?.connection?.has_webhook_secret ? 'Webhook secret (leave blank to keep)' : 'Webhook secret (optional)'}
                    aria-label="{provider.label} webhook secret"
                    type="password"
                    mono
                  />
                  <Input bind:value={forms[provider.id].server} placeholder={provider.help} aria-label="{provider.label} API server" mono />
                  <div class="hint">
                    Webhook URL for {provider.label}, subscribed to
                    {provider.id === 'polar' ? 'the order events' : 'charge.succeeded, charge.refunded and charge.updated'}:
                    <code>{st?.webhook_url}</code>
                  </div>
                  <div class="google-actions">
                    <Button size="sm" disabled={polarBusy} onclick={() => saveProvider(provider.id)}>{polarBusy ? 'Checking' : st?.connected ? 'Save' : 'Connect'}</Button>
                    <Button variant="secondary" size="sm" onclick={() => (openProvider = null)}>Cancel</Button>
                  </div>
                </div>
              {/if}
            </div>
            <div class="google-actions">
              {#if st?.connected}
                <Button variant="secondary" size="sm" disabled={polarBusy} onclick={() => polarAction(() => paymentsApi.sync(id, provider.id))}>
                  {polarBusy ? 'Working' : 'Sync now'}
                </Button>
                <Button variant="secondary" size="sm" onclick={() => (openProvider = openProvider === provider.id ? null : provider.id)}>Edit</Button>
                <Button variant="secondary" size="sm" disabled={polarBusy} onclick={() => disconnectProvider(provider.id)}>Disconnect</Button>
              {:else}
                <Button variant="secondary" size="sm" onclick={() => (openProvider = openProvider === provider.id ? null : provider.id)}>Connect {provider.label}</Button>
              {/if}
            </div>
          </div>
        {/each}
      {/if}
      {#if google}
        <div class="setting google">
          <div class="text">
            <div class="label">Google Search Console</div>
            {#if !google.configured}
              <div class="hint">Shows the search terms Google sends here. Create an OAuth client in Google Cloud, add <code>{googleRedirect}</code> as a redirect URI, then set <code>GLANCE_GOOGLE_CLIENT_ID</code> and <code>GLANCE_GOOGLE_CLIENT_SECRET</code>.</div>
            {:else if google.connected && google.connection}
              <div class="hint">
                {google.connection.email || 'Connected'}{#if google.connection.property} · {google.connection.property}{/if}
                {#if google.needs_reconnect}
                  · <span class="bad">access expired, connect again</span>
                {:else if google.connection.sync_error}
                  · <span class="bad">{google.connection.sync_error}</span>
                {:else if google.latest_day}
                  · data to {google.latest_day}
                {:else if google.connection.property}
                  · first pull pending
                {/if}
              </div>
              {#if !google.connection.property && google.available_properties?.length}
                <div class="hint">No property matches {site.domain}. Pick one:</div>
                <div class="props">
                  {#each google.available_properties as p (p)}
                    <button type="button" class="prop" disabled={googleBusy} onclick={() => googleAction(() => api.googleSetProperty(id, p))}>{p}</button>
                  {/each}
                </div>
              {:else if !google.connection.property}
                <div class="hint bad">This Google account has no Search Console property for {site.domain}. Verify the site in Search Console, then connect again.</div>
              {/if}
            {:else}
              <div class="hint">Shows the search terms Google sends here. Read-only access; Glance pulls once a day.</div>
            {/if}
            {#if googleNotice}<div class="hint ok">{googleNotice}</div>{/if}
          </div>
          <div class="google-actions">
            {#if google.connected && !google.needs_reconnect}
              <Button variant="secondary" size="sm" disabled={googleBusy || !google.connection?.property} onclick={() => googleAction(() => api.googleSync(id))}>{googleBusy ? 'Working' : 'Sync now'}</Button>
              <Button variant="secondary" size="sm" disabled={googleBusy} onclick={disconnectGoogle}>Disconnect</Button>
            {:else if google.configured}
              <Button variant="secondary" size="sm" onclick={connectGoogle}>{google.needs_reconnect ? 'Reconnect Google' : 'Connect Google Search Console'}</Button>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {/if}

  <div class="strip">
    <div class="metrics">
      <MetricStat label="Visitors" value={fmtNum(stats.totals.visitors)} delta={stats.previous_unavailable ? '' : fmtDelta(stats.totals.visitors, stats.previous.visitors)} />
      <MetricStat label="Page views" value={fmtNum(stats.totals.pageviews)} delta={stats.previous_unavailable ? '' : fmtDelta(stats.totals.pageviews, stats.previous.pageviews)} />
      <MetricStat label="Views / visitor" value={fmtRatio(stats.totals.pageviews, stats.totals.visitors)} />
      {#if revenue && !hasFilters}
        <div class="group">
          <MetricStat label="Revenue" value={money(revenue.totals.revenue)} delta={fmtDelta(revenue.totals.revenue, revenue.previous.revenue)} />
          <MetricStat label="Orders" value={fmtNum(revenue.totals.orders)} delta={fmtDelta(revenue.totals.orders, revenue.previous.orders)} />
          <MetricStat label="Per visitor" value={stats.totals.visitors ? money(Math.round(revenue.totals.revenue / stats.totals.visitors)) : money(0)} />
        </div>
      {/if}
    </div>
  </div>

  {#if hasFilters}
    <div class="filters" transition:panel>
      <span class="filters-label">Showing visitors who match</span>
      {#each Object.entries(filters) as [dim, key] (dim)}
        <button type="button" class="chip" onclick={() => toggleFilter(dim as Dim, key ?? '')} title="Remove filter">
          <span class="chip-dim">{FILTER_DIM[dim as Dim]}</span>{filterLabel(dim as Dim, key ?? '')}<span class="chip-x">×</span>
        </button>
      {/each}
      <button type="button" class="plain" onclick={() => setFilters({})}>Clear</button>
      <span class="filters-note">
        {#if stats.truncated}Filters read raw events, kept {stats.retention_days} days, so this range is cut short. Raise retention in Settings to filter further back.{:else}Realtime, revenue and search terms cannot be filtered; revenue and search terms are hidden.{/if}
      </span>
    </div>
  {/if}

  {#key stats.range}
    <div in:pageIn class="stack">
      <AreaChart series={stats.series} markers={stats.markers} bucket={stats.bucket} {range} revenue={hasFilters ? [] : revenueSeries} currency={revenue?.currency ?? ''} />
      {#if stats.hourly_unavailable}
        <p class="chart-note">
          Charted by day: this range covers history imported from another tool, which carries a daily
          total but no hourly detail.
        </p>
      {/if}
      {#if notes.length > 0}
        <ul class="notes">
          {#each notes as n (n.id)}
            <li><span class="note-day">{n.day}</span> {n.text}</li>
          {/each}
        </ul>
      {/if}

      <div class="grid">
        {#if revenue && !hasFilters}
          <div class="wide">
          <BarList
            title="Revenue"
            rows={revenueRows}
            empty={revenueTab === 'ref' || revenueTab === 'source' || revenueTab === 'campaign' || revenueTab === 'landing' ? 'No attributed orders in this range. Pass attribution into checkout to see where sales come from.' : 'No orders in this range'}
            tabs={REVENUE_DIMS}
            tab={revenueTab}
            ontab={(v) => (revenueTab = v)}
            format={money}
          >
            {#snippet icon(r)}{#if revenueTab === 'ref'}<Icon src={r.icon} direct={r.direct} />{/if}{/snippet}
          </BarList>
          </div>
        {/if}
        <Realtime minutes={liveData?.minutes ?? Array(30).fill(0)} total={liveData?.total_30m ?? 0} onmore={() => { mapView = 'live'; document.querySelector('.map-section')?.scrollIntoView({ behavior: 'smooth', block: 'start' }) }} />
        {#if google?.connected && !hasFilters}
          <BarList title="Search terms" rows={termRows.slice(0, TOP_TERMS)} empty={range === '24h' ? 'Google reports search terms two to three days late' : 'No Google search terms for this range yet'} onmore={more('search')} />
        {/if}
        <BarList title="Pages" rows={pages} empty="No page views yet" onmore={more('page')} onselect={select('page')} selected={selectedKey('page')} />
        <BarList
          title="Sources"
          rows={sources}
          empty={sourceTab === 'ref' ? 'No referrers yet' : sourceTab === 'utm_source' ? 'No utm_source or ?ref= tags seen' : sourceTab === 'utm_medium' ? 'No utm_medium tags seen' : 'No utm_campaign tags seen'}
          onmore={more(sourceTab)}
          onselect={select(sourceTab)}
          selected={selectedKey(sourceTab)}
          tabs={sourceTabs}
          tab={sourceTab}
          ontab={(v) => (sourceTab = v)}
        >
          {#snippet icon(r)}{#if sourceTab === 'ref'}<Icon src={r.icon} direct={r.direct} />{/if}{/snippet}
        </BarList>
        <BarList
          title="Locations"
          rows={locations}
          empty={locationTab === 'country' ? 'No locations yet' : locationTab === 'city' ? 'No cities yet. Cities need a GeoIP database; without one, "regions" are time-zone cities.' : 'No regions yet'}
          onmore={more(locationTab)}
          onselect={select(locationTab)}
          selected={selectedKey(locationTab)}
          tabs={locationTabs}
          tab={locationTab}
          ontab={(v) => (locationTab = v)}
        />
        <BarList
          title="Devices"
          rows={devices}
          onmore={more(deviceTab)}
          onselect={select(deviceTab)}
          selected={selectedKey(deviceTab)}
          tabs={[{ value: 'browser', label: 'Browsers' }, { value: 'os', label: 'OS' }, { value: 'device', label: 'Devices' }]}
          tab={deviceTab}
          ontab={(v) => (deviceTab = v)}
        >
          {#snippet icon(r)}<BrandIcon kind={deviceTab} name={r.key} />{/snippet}
        </BarList>
        {#if events.length > 0}
          <BarList
            title="Events"
            rows={rowsFor(eventTab)}
            empty={eventTab === 'prop' ? 'No event properties recorded. Pass an object as the second argument to glance().' : 'No events yet'}
            onmore={more(eventTab)}
            onselect={select(eventTab)}
            selected={selectedKey(eventTab)}
            tabs={eventTabs}
            tab={eventTab}
            ontab={(v) => (eventTab = v)}
          />
        {/if}
        {#if goalResults.length > 0}
          <BarList
            title="Goals"
            rows={goalRows}
            empty="No conversions in this range"
            format={(v) => `${v.toFixed(1)}%`}
          />
        {/if}
        {#if crawlers.length > 0 && !hasFilters}
          <BarList
            title="Crawlers"
            rows={crawlers}
            empty={crawlerTab === 'aibot' ? 'No AI crawlers seen in this range' : 'No crawlers seen in this range'}
            onmore={more(crawlerTab)}
            tabs={[{ value: 'bot', label: 'All' }, { value: 'aibot', label: 'AI' }]}
            tab={crawlerTab}
            ontab={(v) => (crawlerTab = v)}
          />
        {/if}
      </div>

      {#if !hasFilters && (funnelResults.length > 0 || vitals?.overall.length)}
        <div class="grid">
          {#if funnelResults.length > 0}
            <div class="wide"><Funnels funnels={funnelResults} /></div>
          {/if}
          {#if vitals?.overall.length}
            <VitalsCard overall={vitals.overall} devices={vitals.devices} pages={vitals.pages} />
          {/if}
        </div>
      {/if}

      <div class="map-section">
        <div class="head">
          <div class="card-title">{mapView === 'live' ? 'Live' : 'Visitors by country'}</div>
          <div class="map-controls">
            <span class="hint">{mapView === 'live' ? `${online} online${lastActivity ? ` · last activity ${lastActivity}` : ''}` : `${stats.breakdowns.country.length} ${stats.breakdowns.country.length === 1 ? 'country' : 'countries'}`}</span>
            <Segment options={[{ value: 'live', label: 'Live' }, { value: 'range', label: range }]} value={mapView} gap={14} onchange={(v) => (mapView = v)} />
          </div>
        </div>
        {#if mapView === 'live'}
          {#if LiveMap && liveData}
            <LiveMap live={liveData} home={site.home_country || topCountry} />
          {:else}
            <div class="map-placeholder tall"></div>
          {/if}
        {:else if WorldMap}
          <WorldMap rows={stats.breakdowns.country} home={site.home_country || topCountry} />
        {:else}
          <div class="map-placeholder"></div>
        {/if}
      </div>
    </div>
  {/key}
{:else if error}
  <p class="bad">{error}</p>
{/if}

{#if modal}
  <Modal title={modal === 'search' ? 'Search terms' : DIM_TITLE[modal]} subtitle="{site?.name} · {range} · {modalRows ? `${modalRows.length} ${modal === 'event' ? 'events' : modal === 'search' ? 'terms' : 'rows'}` : 'loading'}" onclose={() => (modal = null)}>
    <div class="modal-search"><Input bind:value={filter} placeholder="Filter" aria-label="Filter rows" /></div>
    {#if modalRows}
      <BarList bare rows={filtered} empty="No matches">
        {#snippet icon(r)}
          {#if modal === 'ref'}<Icon src={r.icon} direct={r.direct} />
          {:else if modal === 'browser' || modal === 'os' || modal === 'device'}<BrandIcon kind={modal} name={r.key} />{/if}
        {/snippet}
      </BarList>
    {/if}
  </Modal>
{/if}

<style>
  .modal-search { position: sticky; top: 0; background: var(--up-bg); padding: 4px 0 12px; z-index: 1; }
  .title { display: flex; align-items: center; gap: 10px; margin-top: 4px; }
  .title .ranges { margin-right: 22px; }
  .name { font: var(--up-type-row-title); }
  .domain { font: var(--up-type-meta); color: var(--up-text-muted); }
  .live { display: flex; align-items: center; gap: 6px; font: var(--up-type-meta); color: var(--up-text-muted); margin-left: 6px; }
  .live .last { color: var(--up-text-faint); }
  .live.idle .dot { background: var(--up-border-control); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--up-accent); }
  .spacer { flex: 1; }
  .plain { background: none; border: none; padding: 0; cursor: pointer; font: var(--up-type-ui); color: var(--up-text-muted); }
  .plain:hover, .plain.on { color: var(--up-ink); }

  .settings { display: flex; flex-direction: column; gap: 14px; padding: 16px 18px; border: 1px solid var(--up-border-hairline); border-radius: var(--up-radius-card); margin-top: -16px; }
  .setting { display: flex; align-items: center; justify-content: space-between; gap: 24px; }
  .text { display: flex; flex-direction: column; gap: 2px; }
  .label { font: var(--up-type-setting); }
  .hint { font: var(--up-type-meta); color: var(--up-text-muted); }
  .ctl { width: 240px; flex-shrink: 0; }
  .ctl.short { width: 90px; }
  .ranges-setting { flex-shrink: 0; }
  .code { background: var(--up-surface-dark); border-radius: var(--up-radius-tooltip); padding: 12px 14px; display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .snippet { font: var(--up-type-code); color: var(--up-text-on-dark); word-break: break-all; }
  .copy { background: none; border: none; padding: 2px 0; cursor: pointer; font: var(--up-type-small); color: var(--up-operational-strong); flex-shrink: 0; }
  .copy:hover { color: var(--up-text-on-dark); }
  .google { align-items: flex-start; }
  .google .text { gap: 6px; }
  .hint code { font: var(--up-type-code); user-select: all; }
  .hint.ok { color: var(--up-operational-strong); }
  .warn { color: var(--up-text-muted); }
  .polar-form { display: flex; flex-direction: column; gap: 8px; width: 100%; max-width: 460px; padding-top: 4px; }
  .google-actions { display: flex; gap: 8px; flex-shrink: 0; }
  .quiet { color: var(--up-text-muted); }
  .mono { font: var(--up-type-code); color: var(--up-ink); }
  textarea, select, input[type='file'] {
    font: var(--up-type-code);
    color: var(--up-ink);
    background: var(--up-bg);
    box-shadow: inset 0 0 0 1px var(--up-border-control);
    border: none;
    border-radius: var(--up-radius-control);
    padding: 8px 10px;
    width: 100%;
    resize: vertical;
  }
  .chart-note { font: var(--up-type-ui); color: var(--up-text-muted); margin: -4px 0 0; }
  .notes { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  .notes li { font: var(--up-type-ui); color: var(--up-text-muted); }
  .note-day { font: var(--up-type-code); color: var(--up-ink); margin-right: 6px; }
  .props { display: flex; flex-wrap: wrap; gap: 6px; }
  .prop { font: var(--up-type-code); color: var(--up-ink); background: var(--up-bg-hover); border: none; border-radius: var(--up-radius-control); padding: 4px 8px; cursor: pointer; }
  .prop:hover { box-shadow: inset 0 0 0 1px var(--up-border-control); }
  .prop:disabled { opacity: 0.5; cursor: default; }

  .filters { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: -12px; }
  .filters-label { font: var(--up-type-meta); color: var(--up-text-muted); }
  .chip { display: inline-flex; align-items: center; gap: 6px; font: var(--up-type-meta); color: var(--up-ink); background: var(--up-accent-tint); border: none; border-radius: var(--up-radius-pill); padding: 4px 8px 4px 10px; cursor: pointer; }
  .chip:hover { box-shadow: inset 0 0 0 1px var(--up-accent); }
  .chip-dim { color: var(--up-text-muted); }
  .chip-x { color: var(--up-text-muted); font-size: 14px; line-height: 1; }
  .filters .plain { margin-left: 4px; }
  .filters-note { font: var(--up-type-meta); color: var(--up-text-muted); width: 100%; }
  .strip { display: flex; align-items: flex-start; }
  .metrics { display: flex; gap: 28px; flex-wrap: nowrap; }
  .group { display: flex; gap: 28px; padding-left: 28px; border-left: 1px solid var(--up-border-hairline); }
  .stack { display: flex; flex-direction: column; gap: 36px; min-width: 0; }
  .stack > :global(*) { min-width: 0; }
  /* minmax(0, 1fr): a plain 1fr column grows to fit its widest child, which is how cards escaped the viewport on phones. */
  .grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 20px; }
  .grid > :global(*) { min-width: 0; }
  .wide { grid-column: 1 / -1; }
  .map-section { display: flex; flex-direction: column; gap: 12px; }
  .head { display: flex; align-items: baseline; justify-content: space-between; }
  .card-title { font: var(--up-type-setting); font-weight: 700; }
  .map-placeholder { height: 340px; border: 1px solid var(--up-border-hairline); border-radius: var(--up-radius-card); }
  .map-placeholder.tall { height: 420px; }
  .map-controls { display: flex; align-items: center; gap: 18px; }
  .bad { font: var(--up-type-meta); color: var(--up-degraded-strong); }
  @media (max-width: 600px) {
    .grid { grid-template-columns: minmax(0, 1fr); }
    .title { flex-wrap: wrap; row-gap: 8px; }
    .strip { min-width: 0; overflow: hidden; }
    .title .ranges { margin-right: 0; width: 100%; order: 9; }
    .metrics { gap: 24px; flex-wrap: wrap; row-gap: 16px; }
    .group { gap: 24px; padding-left: 0; border-left: none; width: 100%; }
    .setting { flex-direction: column; align-items: flex-start; gap: 8px; }
    .ctl { width: 100%; }
  }
</style>
