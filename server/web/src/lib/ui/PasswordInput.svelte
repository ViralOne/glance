<script lang="ts">
  // A labelled password field with a reveal toggle and a Caps Lock warning.
  //
  // Both affordances exist for the same reason: the first password Glance issues
  // is random, printed once in the server log, and then typed by hand. Not being
  // able to see what you typed — or having Caps Lock silently on — turns that
  // into guesswork, and the only feedback is a rejected sign-in.
  import Input from './Input.svelte'

  let {
    value = $bindable(''),
    label,
    hint = '',
    autocomplete = 'current-password',
    placeholder = '',
    required = false,
    disabled = false,
    autofocus = false,
  }: {
    value?: string
    label: string
    hint?: string
    autocomplete?: 'current-password' | 'new-password'
    placeholder?: string
    required?: boolean
    disabled?: boolean
    autofocus?: boolean
  } = $props()

  const id = $props.id()
  let shown = $state(false)
  let caps = $state(false)

  // Read from the event rather than tracking keydown/keyup ourselves: the
  // modifier state is authoritative, and it stays right even if the key was
  // pressed while another window had focus.
  const track = (e: KeyboardEvent) => (caps = e.getModifierState('CapsLock'))
</script>

<div class="field">
  <label for={id}>{label}</label>
  <div class="wrap">
    <Input
      {id}
      bind:value
      type={shown ? 'text' : 'password'}
      {autocomplete}
      {placeholder}
      {required}
      {disabled}
      {autofocus}
      onkeydown={track}
      onkeyup={track}
    />
    <button
      type="button"
      class="reveal"
      onclick={() => (shown = !shown)}
      aria-pressed={shown}
      aria-controls={id}
      title={shown ? 'Hide password' : 'Show password'}
    >
      {shown ? 'Hide' : 'Show'}
    </button>
  </div>
  {#if caps}<p class="warn" role="status">Caps Lock is on.</p>{/if}
  {#if hint}<p class="hint">{hint}</p>{/if}
</div>

<style>
  .field { display: flex; flex-direction: column; gap: 6px; }
  label { font: var(--up-type-ui); color: var(--up-text-secondary); }
  .wrap { position: relative; display: flex; }
  /* Room for the toggle, so a long password does not run underneath it. */
  .wrap :global(input) { padding-right: 56px; }
  .reveal {
    position: absolute;
    right: 1px;
    top: 1px;
    bottom: 1px;
    padding: 0 12px;
    background: none;
    border: none;
    border-radius: 0 var(--up-radius-control) var(--up-radius-control) 0;
    cursor: pointer;
    font: var(--up-type-small);
    color: var(--up-text-muted);
  }
  .reveal:hover { color: var(--up-ink); }
  .warn { font: var(--up-type-meta); color: var(--up-degraded-strong); margin: 0; }
  .hint { font: var(--up-type-meta); color: var(--up-text-muted); margin: 0; line-height: 1.5; }
</style>
