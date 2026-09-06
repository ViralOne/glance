<script lang="ts">
  // Core Web Vitals card: one row per metric with its p75, Google's rating as
  // a colour, and the share of page loads rated good.
  //
  // The p75 leads because that is the figure Google's own reporting uses, and
  // the good/poor split sits behind it because a percentile alone hides
  // whether a bad number is everyone or a tail.
  import type { VitalRow, VitalScope } from '../api'
  import Segment from './Segment.svelte'

  let {
    overall,
    devices = [],
    pages = [],
  }: { overall: VitalRow[]; devices?: VitalScope[]; pages?: VitalScope[] } = $props()

  type Cut = 'overall' | 'device' | 'page'
  let cut = $state<Cut>('overall')
  const cuts = $derived<{ value: Cut; label: string }[]>([
    { value: 'overall', label: 'Overall' },
    ...(devices.length ? [{ value: 'device' as const, label: 'Device' }] : []),
    ...(pages.length ? [{ value: 'page' as const, label: 'Page' }] : []),
  ])

  // What each metric measures, in one line, because "INP" means nothing to
  // most people and a tooltip is cheaper than a docs link.
  const MEANING: Record<string, string> = {
    LCP: 'Largest Contentful Paint: when the biggest thing on screen finished rendering',
    INP: 'Interaction to Next Paint: how long the page took to respond to a tap or click',
    CLS: 'Cumulative Layout Shift: how much the page moved around while loading',
    TTFB: 'Time to First Byte: how long the server took to start replying',
    FCP: 'First Contentful Paint: when anything appeared',
  }

  function display(r: VitalRow): string {
    // CLS is stored multiplied by 1000 so every metric shares one column.
    if (r.metric === 'CLS') return (r.p75 / 1000).toFixed(3)
    if (r.p75 >= 1000) return (r.p75 / 1000).toFixed(2) + ' s'
    return Math.round(r.p75) + ' ms'
  }

  const scopes = $derived<VitalScope[]>(
    cut === 'overall' ? [{ key: '', rows: overall }] : cut === 'device' ? devices : pages,
  )
  const empty = $derived(overall.length === 0)
</script>

<section class="card">
  <div class="head">
    <div class="card-title">Speed</div>
    {#if cuts.length > 1}
      <Segment options={cuts} value={cut} gap={12} onchange={(v) => (cut = v)} />
    {/if}
  </div>

  {#if empty}
    <p class="empty">
      No Core Web Vitals yet. They arrive from real page loads in browsers that support
      <code>PerformanceObserver</code>, which is Chromium and, for some metrics, Firefox and Safari.
    </p>
  {:else}
    {#each scopes as scope (scope.key)}
      {#if cut !== 'overall'}
        <div class="scope">{scope.key}</div>
      {/if}
      <ul class="rows">
        {#each scope.rows as r (r.metric)}
          <li>
            <span class="metric" title={MEANING[r.metric] ?? r.metric}>{r.metric}</span>
            <span class="value {r.rating}">{display(r)}</span>
            <span class="bar" aria-hidden="true">
              <!-- The bar is the share of loads rated good, not the value: a
                   percentile has no natural scale to draw against. -->
              <span class="fill {r.rating}" style="width: {r.good_pct}%"></span>
            </span>
            <span class="share" title="{r.samples} samples">{Math.round(r.good_pct)}% good</span>
          </li>
        {/each}
      </ul>
    {/each}
  {/if}
</section>

<style>
  .card {
    background: var(--up-bg);
    box-shadow: inset 0 0 0 1px var(--up-border);
    border-radius: var(--up-radius-card);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 20px; }
  .card-title { font: var(--up-type-ui-strong); color: var(--up-ink); }
  .empty { font: var(--up-type-ui); color: var(--up-text-muted); margin: 0; }
  .scope { font: var(--up-type-ui); color: var(--up-text-muted); padding-top: 4px; }
  .rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  li { display: grid; grid-template-columns: 44px 72px 1fr auto; align-items: center; gap: 10px; }
  .metric { font: var(--up-type-code); color: var(--up-text-muted); cursor: help; }
  .value { font: var(--up-type-ui-strong); font-variant-numeric: tabular-nums; }
  .value.good { color: var(--up-operational-strong); }
  .value.poor { color: var(--up-critical-strong, #d64545); }
  .bar { height: 6px; border-radius: 3px; background: var(--up-bg-hover); overflow: hidden; }
  .fill { display: block; height: 100%; border-radius: 3px; background: var(--up-accent); }
  .fill.poor { background: var(--up-critical-strong, #d64545); }
  .share { font: var(--up-type-ui); color: var(--up-text-muted); font-variant-numeric: tabular-nums; }
</style>
