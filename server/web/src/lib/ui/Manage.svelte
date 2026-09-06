<script lang="ts">
  // A small list-plus-add editor, shared by goals, funnels, notes and shares.
  //
  // These four are the same interaction: show what exists, let one be removed,
  // and offer a short form to add another. Writing it once keeps them
  // consistent and keeps four near-identical blocks out of the settings panel.
  import Button from './Button.svelte'
  import type { Snippet } from 'svelte'

  let {
    title,
    hint,
    items,
    empty,
    busy = false,
    addLabel = 'Add',
    canAdd = true,
    row,
    form,
    onadd,
    onremove,
  }: {
    title: string
    hint?: string
    items: { id: string }[]
    empty: string
    busy?: boolean
    addLabel?: string
    canAdd?: boolean
    /** Renders one item's own content; the remove button is supplied here. */
    row: Snippet<[{ id: string }]>
    /** The add form, shown while open. */
    form?: Snippet
    onadd?: () => void
    onremove?: (id: string) => void
  } = $props()

  let open = $state(false)
</script>

<div class="manage">
  <div class="top">
    <div class="text">
      <div class="label">{title}</div>
      {#if hint}<div class="hint">{hint}</div>{/if}
    </div>
    {#if form && onadd !== undefined}
      <Button variant="secondary" size="sm" onclick={() => (open = !open)}>{open ? 'Cancel' : addLabel}</Button>
    {/if}
  </div>

  {#if items.length === 0}
    <div class="hint">{empty}</div>
  {:else}
    <ul>
      {#each items as item (item.id)}
        <li>
          <div class="row">{@render row(item)}</div>
          {#if onremove}
            <button class="remove" type="button" disabled={busy} onclick={() => onremove?.(item.id)} aria-label="Remove">
              Remove
            </button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if open && form}
    <div class="form">
      {@render form()}
      <div class="actions">
        <Button
          size="sm"
          disabled={busy || !canAdd}
          onclick={() => {
            onadd?.()
            open = false
          }}
        >
          {busy ? 'Saving' : addLabel}
        </Button>
      </div>
    </div>
  {/if}
</div>

<style>
  .manage { display: flex; flex-direction: column; gap: 8px; width: 100%; }
  .top { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  .label { font: var(--up-type-ui); color: var(--up-ink); }
  .hint { font: var(--up-type-ui); color: var(--up-text-muted); }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  li { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .row { font: var(--up-type-ui); color: var(--up-ink); min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .remove {
    font: var(--up-type-ui);
    color: var(--up-text-muted);
    background: none;
    border: none;
    cursor: pointer;
    padding: 2px 4px;
    flex-shrink: 0;
  }
  .remove:hover { color: var(--up-ink); }
  .remove:disabled { opacity: 0.5; cursor: default; }
  .form { display: flex; flex-direction: column; gap: 8px; max-width: 460px; padding-top: 4px; }
  .actions { display: flex; gap: 8px; }
</style>
