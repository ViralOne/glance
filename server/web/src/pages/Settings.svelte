<script lang="ts">
  // General settings: overview, appearance, MCP and API tokens, retention, export.
  import { api, type Alert, type AlertChannel, type AlertKind, type AlertMetric, type GeneralSettings, type Status, type Token } from '../lib/api'
  import { fmtNum } from '../lib/format'
  import { isHex, setBaseAccent } from '../lib/accent'
  import { panel, reorder } from '../lib/motion'
  import Input from '../lib/ui/Input.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import Segment from '../lib/ui/Segment.svelte'
  import Swatches from '../lib/ui/Swatches.svelte'
  import MetricStat from '../lib/ui/MetricStat.svelte'

  let { ontitle }: { ontitle: (t: string) => void } = $props()
  let status = $state<Status | null>(null)
  let settings = $state<GeneralSettings | null>(null)
  let tokens = $state<Token[]>([])
  let tokenScope = $state<'read' | 'write'>('read')
  // Alerts live here rather than per site because a rule may cover every site.
  let alerts = $state<Alert[]>([])
  let emailConfigured = $state(false)
  let siteNames = $state<Record<string, string>>({})
  let alertBusy = $state(false)
  let alertSent = $state('')
  let alertForm = $state({
    kind: 'spike' as AlertKind,
    metric: 'visitors' as AlertMetric,
    window: '24h',
    threshold: '',
    channel: 'webhook' as AlertChannel,
    destination: '',
  })
  const siteName = (siteID: string) => siteNames[siteID] ?? siteID
  async function loadAlerts() {
    try {
      const [a, sites] = await Promise.all([api.alerts(), api.sites()])
      alerts = a.alerts
      emailConfigured = a.email_configured
      siteNames = Object.fromEntries(sites.sites.map((s) => [s.id, s.name]))
    } catch {
      alerts = []
    }
  }
  async function alertAction(run: () => Promise<unknown>) {
    alertBusy = true
    alertSent = ''
    try {
      await run()
      error = ''
      await loadAlerts()
    } catch (e: any) {
      error = e.message
    } finally {
      alertBusy = false
    }
  }
  function addAlert() {
    const n = Number(alertForm.threshold)
    alertAction(() =>
      api.createAlert({
        kind: alertForm.kind,
        metric: alertForm.metric,
        window: alertForm.window,
        // A blank threshold is left out so the server applies its own default,
        // which for a digest is 08:00 rather than midnight.
        ...(alertForm.threshold.trim() && Number.isFinite(n) ? { threshold: n } : {}),
        channel: alertForm.channel,
        destination: alertForm.destination.trim(),
      }),
    )
  }
  async function testAlert(alertID: string) {
    alertBusy = true
    try {
      const r = await api.testAlert(alertID)
      alertSent = `Sent to ${r.destination}.`
      error = ''
    } catch (e: any) {
      error = e.message
    } finally {
      alertBusy = false
    }
  }
  let envToken = $state(false)
  let error = $state('')
  let tokenName = $state('')
  let minted = $state<{ name: string; secret: string } | null>(null)
  let copied = $state(false)

  async function load() {
    try {
      const [st, g, tk] = await Promise.all([api.status(), api.settings(), api.tokens()])
      loadAlerts()
      status = st
      settings = g
      tokens = tk.tokens
      envToken = tk.env_token_set
    } catch (e: any) {
      error = e.message
    }
  }
  $effect(() => {
    load()
  })

  let timers: Record<string, ReturnType<typeof setTimeout>> = {}
  function save(patch: Parameters<typeof api.updateSettings>[0], key = 'x', delay = 0) {
    clearTimeout(timers[key])
    timers[key] = setTimeout(() => {
      error = ''
      api
        .updateSettings(patch)
        .then((g) => {
          settings = g
          setBaseAccent(g.accent)
          ontitle(g.title)
        })
        .catch((e: any) => (error = e.message))
    }, delay)
  }
  function pickAccent(hex: string) {
    if (!isHex(hex)) return
    setBaseAccent(hex) // instant preview
    save({ accent: hex })
  }
  async function mint() {
    try {
      const r = await api.createToken(tokenName.trim() || 'API token', tokenScope)
      minted = { name: r.token.name, secret: r.secret }
      tokenName = ''
      tokens = [r.token, ...tokens]
      error = ''
    } catch (e: any) {
      error = e.message
    }
  }
  function revoke(t: Token) {
    if (!confirm(`Revoke "${t.name}"? Anything using it stops working immediately.`)) return
    api
      .deleteToken(t.id)
      .then(() => (tokens = tokens.filter((x) => x.id !== t.id)))
      .catch((e: any) => (error = e.message))
  }
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {}
  }
  const mcpURL = `${location.origin}/mcp`
  const mcpConfig = $derived(JSON.stringify({ mcpServers: { glance: { url: mcpURL, headers: { Authorization: `Bearer ${minted?.secret ?? 'glance_tok_…'}` } } } }, null, 2))
  const fmtBytes = (b: number) => (b >= 1 << 30 ? (b / (1 << 30)).toFixed(1) + ' GB' : b >= 1 << 20 ? (b / (1 << 20)).toFixed(1) + ' MB' : Math.round(b / 1024) + ' KB')
  const fmtUptime = (s: number) => (s >= 86400 ? `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h` : s >= 3600 ? `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m` : `${Math.floor(s / 60)}m`)
</script>

{#if status && settings}
  <div class="metrics">
    <MetricStat label="Websites" value={String(status.sites)} />
    <MetricStat label="Raw events" value={fmtNum(status.raw_events)} />
    <MetricStat label="Rollup rows" value={fmtNum(status.daily_rows)} />
    <MetricStat label="Database" value={fmtBytes(status.db_bytes)} />
    <MetricStat label="Uptime" value={fmtUptime(status.uptime_seconds)} />
  </div>

  <section class="card">
    <div class="card-title">Appearance</div>
    <div class="setting">
      <div class="text">
        <div class="label">Accent colour</div>
        <div class="hint">Used for the wordmark, links, charts and the map</div>
      </div>
      <Swatches value={settings.accent} onpick={pickAccent} />
    </div>
    <div class="setting">
      <div class="text">
        <div class="label">Title</div>
        <div class="hint">Shown as the wordmark in the header and the browser tab</div>
      </div>
      <div class="ctl"><Input value={settings.title} placeholder="Glance" aria-label="Title" oninput={(e) => save({ title: e.currentTarget.value }, 'title', 400)} /></div>
    </div>
  </section>

  <section class="card">
    <div class="card-title">MCP</div>
    <div class="setting">
      <div class="text">
        <div class="label">MCP endpoint</div>
        <div class="hint">Lets an AI agent read your analytics at <code>{mcpURL}</code>. Tokens minted below and the admin login work there{envToken ? ', as does GLANCE_MCP_TOKEN from the environment' : ''}.</div>
      </div>
      <Switch checked={settings.mcp_enabled} label="MCP endpoint" onchange={(v) => save({ mcp_enabled: v })} />
    </div>
    <div class="tokens">
      <div class="label">API tokens</div>
      <div class="hint">
        A read token can only read. A write token may additionally record chart annotations, which is
        the one thing an agent can change; nothing a token can do alters a measurement. Each token is
        shown once when minted; only a hash is kept.
      </div>
      {#if minted}
        <div class="minted" transition:panel>
          <div class="code">
            <span class="snippet">{minted.secret}</span>
            <button type="button" class="copy" onclick={() => copy(minted!.secret)}>{copied ? 'Copied' : 'Copy'}</button>
          </div>
          <div class="hint">This is the only time "{minted.name}" is shown. Add it to your agent:</div>
          <pre class="config">{mcpConfig}</pre>
          <button type="button" class="plain" onclick={() => (minted = null)}>Done</button>
        </div>
      {/if}
      {#each tokens as t (t.id)}
        <div class="row" animate:reorder transition:panel>
          <div class="text">
            <div class="name">{t.name}</div>
            <div class="hint mono">{t.prefix}… · {t.scope} · created {new Date(t.created_at).toLocaleDateString()}{#if t.last_used_at}{' · '}last used {new Date(t.last_used_at).toLocaleString()}{:else}{' · '}never used{/if}</div>
          </div>
          <button type="button" class="plain remove" title="Revoke" aria-label="Revoke token" onclick={() => revoke(t)}>×</button>
        </div>
      {/each}
      <form
        class="add"
        onsubmit={(e) => {
          e.preventDefault()
          mint()
        }}
      >
        <div class="name-in"><Input bind:value={tokenName} placeholder="Name, e.g. Claude" aria-label="Token name" /></div>
        <Segment
          options={[{ value: 'read' as const, label: 'Read' }, { value: 'write' as const, label: 'Write' }]}
          value={tokenScope}
          gap={12}
          onchange={(v) => (tokenScope = v)}
        />
        <Button type="submit">Mint token</Button>
      </form>
    </div>
  </section>

  <section class="card">
    <div class="card-title">Alerts</div>
    <div class="hint">
      {emailConfigured
        ? 'Delivered to a webhook or an email address.'
        : 'Delivered to a webhook. Set GLANCE_SMTP_HOST and GLANCE_SMTP_FROM to send email as well.'}
      A spike or drop compares with the same window a week earlier, so a rule fires on news rather
      than on the weekly shape of your traffic.
    </div>
    {#each alerts as a (a.id)}
      <div class="row" transition:panel>
        <div class="text">
          <div class="name">
            {a.kind === 'digest'
              ? `Weekly digest, Mondays at ${String(a.threshold).padStart(2, '0')}:00 UTC`
              : `${a.kind} · ${a.metric} · ${a.window} · ${a.threshold}${a.kind === 'threshold' ? '' : '%'}`}
          </div>
          <div class="hint mono">
            {a.site_id ? siteName(a.site_id) : 'every site'} · {a.channel} · {a.destination}
            {#if a.last_error}· <span class="bad">{a.last_error}</span>{/if}
            {#if a.last_fired}· last fired {new Date(a.last_fired).toLocaleString()}{/if}
          </div>
        </div>
        <div class="alert-actions">
          <Switch checked={a.enabled} label="Enabled" onchange={(v) => alertAction(() => api.updateAlert(a.id, { enabled: v }))} />
          <button type="button" class="plain" onclick={() => testAlert(a.id)} title="Send now">Test</button>
          <button type="button" class="plain remove" title="Delete" aria-label="Delete alert" onclick={() => alertAction(() => api.deleteAlert(a.id))}>×</button>
        </div>
      </div>
    {/each}
    <form
      class="alert-add"
      onsubmit={(e) => {
        e.preventDefault()
        addAlert()
      }}
    >
      <select bind:value={alertForm.kind} aria-label="Alert kind">
        <option value="spike">Spike</option>
        <option value="drop">Drop</option>
        <option value="threshold">Threshold</option>
        <option value="digest">Weekly digest</option>
      </select>
      {#if alertForm.kind !== 'digest'}
        <select bind:value={alertForm.metric} aria-label="Metric">
          <option value="visitors">Visitors</option>
          <option value="pageviews">Pageviews</option>
          <option value="revenue">Revenue</option>
        </select>
        <select bind:value={alertForm.window} aria-label="Window">
          <option value="1h">1h</option>
          <option value="24h">24h</option>
          <option value="7d">7d</option>
        </select>
      {/if}
      <div class="thresh">
        <Input
          bind:value={alertForm.threshold}
          placeholder={alertForm.kind === 'digest' ? 'Hour, 0-23' : alertForm.kind === 'threshold' ? 'Count' : 'Percent'}
          aria-label="Threshold"
        />
      </div>
      <select bind:value={alertForm.channel} aria-label="Channel">
        <option value="webhook">Webhook</option>
        {#if emailConfigured}<option value="email">Email</option>{/if}
      </select>
      <div class="dest">
        <Input
          bind:value={alertForm.destination}
          placeholder={alertForm.channel === 'email' ? 'you@example.com' : 'https://hooks.slack.com/…'}
          aria-label="Destination"
        />
      </div>
      <Button type="submit" disabled={alertBusy || !alertForm.destination.trim()}>{alertBusy ? 'Saving' : 'Add alert'}</Button>
    </form>
    {#if alertSent}<div class="hint ok">{alertSent}</div>{/if}
  </section>

  <section class="card">
    <div class="card-title">Retention</div>
    <div class="setting">
      <div class="text">
        <div class="label">Keep raw events for</div>
        <div class="hint">{settings.retention_from_env ? 'Set by GLANCE_RETENTION_DAYS in the environment.' : 'Hourly and daily rollups are kept forever; raw rows only feed the live view and today\'s rebuild.'}</div>
      </div>
      <Segment options={[2, 7, 14, 30, 90].map((d) => ({ value: d, label: `${d}d` }))} value={settings.retention_days} gap={14} onchange={(d) => !settings?.retention_from_env && save({ retention_days: d })} />
    </div>
  </section>

  <section class="card">
    <div class="card-title">Data</div>
    <div class="setting">
      <div class="text">
        <div class="label">Export</div>
        <div class="hint">Every site and daily rollup as one JSON file</div>
      </div>
      <a class="btn" href="/api/v1/export" download>Download</a>
    </div>
    <div class="setting">
      <div class="text">
        <div class="label">Ingest</div>
        <div class="hint">{fmtNum(status.written)} events written since start{#if status.dropped > 0} · <span class="bad">{status.dropped} dropped when the queue was full</span>{/if}</div>
      </div>
      <span class="hint">v{status.version}</span>
    </div>
  </section>

  {#if error}<p class="bad">{error}</p>{/if}
{:else if error}
  <p class="bad">{error}</p>
{/if}

<style>
  .metrics { display: flex; gap: 36px; flex-wrap: wrap; }
  .card { border: 1px solid var(--up-border-hairline); border-radius: var(--up-radius-card); padding: 20px; display: flex; flex-direction: column; gap: 4px; }
  .card-title { font: var(--up-type-setting); font-weight: 700; margin-bottom: 6px; }
  .setting { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding: 14px 0; border-bottom: 1px solid var(--up-border-hairline); }
  .setting:last-child { border-bottom: none; }
  .text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .label { font: var(--up-type-setting); }
  .name { font: var(--up-type-row-title); }
  .hint { font: var(--up-type-meta); color: var(--up-text-muted); line-height: 1.5; }
  .hint code { font: var(--up-type-code); }
  .mono { font: var(--up-type-code); }
  .ctl { width: 220px; flex-shrink: 0; }
  .tokens { display: flex; flex-direction: column; gap: 10px; padding-top: 14px; }
  .row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 0; border-top: 1px solid var(--up-border-hairline); }
  .plain { background: none; border: none; padding: 0; cursor: pointer; font: var(--up-type-ui); color: var(--up-text-muted); }
  .plain:hover { color: var(--up-ink); }
  .remove { font-size: 16px; color: var(--up-text-faint); line-height: 1; }
  .remove:hover { color: var(--up-degraded-strong); }
  .add { display: flex; gap: 10px; align-items: center; padding-top: 6px; }
  .name-in { flex: 1; max-width: 280px; }
  .minted { display: flex; flex-direction: column; gap: 8px; padding: 12px 0 6px; }
  .code { background: var(--up-surface-dark); border-radius: var(--up-radius-tooltip); padding: 12px 14px; display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .snippet { font: var(--up-type-code); color: var(--up-text-on-dark); word-break: break-all; }
  .copy { background: none; border: none; padding: 2px 0; cursor: pointer; font: var(--up-type-small); color: var(--up-operational-strong); flex-shrink: 0; }
  .copy:hover { color: var(--up-text-on-dark); }
  .config { font: var(--up-type-code); color: var(--up-text-secondary); background: var(--up-bg-hover); border-radius: var(--up-radius-control); padding: 10px 12px; margin: 0; overflow-x: auto; }
  .btn { font: var(--up-type-ui); color: var(--up-ink); background: var(--up-bg); box-shadow: inset 0 0 0 1px var(--up-border-control); border-radius: var(--up-radius-control); height: 34px; padding: 0 16px; display: inline-flex; align-items: center; }
  .btn:hover { background: var(--up-bg-hover); color: var(--up-ink); }
  .bad { font: var(--up-type-meta); color: var(--up-degraded-strong); }
  @media (max-width: 600px) {
    .setting { flex-direction: column; align-items: flex-start; gap: 10px; }
    .ctl { width: 100%; }
  }
  .alert-actions { display: flex; align-items: center; gap: 10px; flex-shrink: 0; }
  .alert-add { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; padding-top: 4px; }
  .alert-add select {
    font: var(--up-type-ui);
    color: var(--up-ink);
    background: var(--up-bg);
    box-shadow: inset 0 0 0 1px var(--up-border-control);
    border: none;
    border-radius: var(--up-radius-control);
    height: 30px;
    padding: 0 8px;
  }
  .alert-add .thresh { width: 130px; }
  .alert-add .dest { flex: 1; min-width: 220px; }
  .hint.ok { color: var(--up-operational-strong); }
  .bad { color: var(--up-critical-strong, #d64545); }
</style>
