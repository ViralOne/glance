<script lang="ts">
  // Funnel card: one bar per step, widths relative to the first step, with the
  // loss between steps called out.
  //
  // Drop-off is shown as its own number rather than left for the reader to
  // subtract, because the useful question about a funnel is always "where do
  // people leave", not "how many reached the end".
  import type { FunnelResult } from '../api'
  import { fmtNum } from '../format'

  let { funnels }: { funnels: FunnelResult[] } = $props()
</script>

{#each funnels as f (f.id)}
  <section class="card">
    <div class="head">
      <div class="card-title">{f.name}</div>
      <span class="conv">{f.conversion.toFixed(1)}% end to end</span>
    </div>

    {#if f.truncated}
      <p class="note">
        Showing the last {f.retention_days} days, not the whole range: a funnel needs the individual
        events, and those are kept for {f.retention_days} days. Raise retention in Settings to reach further back.
      </p>
    {/if}

    <ol class="steps">
      {#each f.steps as s, i (s.target + i)}
        <li>
          <div class="label">
            <span class="name">{s.name}</span>
            <span class="count">{fmtNum(s.visitors)}</span>
          </div>
          <div class="bar" aria-hidden="true">
            <div class="fill" style="width: {Math.max(s.rate, 0.5)}%"></div>
          </div>
          <div class="meta">
            <span>{s.rate.toFixed(1)}% of step 1</span>
            {#if i > 0 && s.drop_off > 0}
              <span class="drop">−{fmtNum(s.drop_off)} left here ({s.drop_off_rate.toFixed(0)}%)</span>
            {/if}
          </div>
        </li>
      {/each}
    </ol>

    <p class="note quiet">
      Measured within a single day. Visitor identities rotate daily by design, so someone who lands
      on Monday and converts on Tuesday cannot be joined up.
    </p>
  </section>
{/each}

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
  .head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
  .card-title { font: var(--up-type-ui-strong); color: var(--up-ink); }
  .conv { font: var(--up-type-ui); color: var(--up-text-muted); font-variant-numeric: tabular-nums; }
  .note { font: var(--up-type-ui); color: var(--up-text-muted); margin: 0; }
  .note.quiet { opacity: 0.75; }
  .steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 12px; counter-reset: step; }
  .label { display: flex; justify-content: space-between; gap: 10px; align-items: baseline; }
  .name { font: var(--up-type-ui); color: var(--up-ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .count { font: var(--up-type-ui-strong); color: var(--up-ink); font-variant-numeric: tabular-nums; }
  .bar { height: 8px; border-radius: 4px; background: var(--up-bg-hover); overflow: hidden; margin-top: 4px; }
  .fill { height: 100%; border-radius: 4px; background: var(--up-accent); transition: width 240ms ease; }
  .meta { display: flex; justify-content: space-between; gap: 10px; font: var(--up-type-ui); color: var(--up-text-muted); margin-top: 4px; font-variant-numeric: tabular-nums; }
  .drop { color: var(--up-text-muted); }
</style>
