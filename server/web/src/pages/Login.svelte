<script lang="ts">
  // Sign-in screen.
  //
  // Deliberately a single centred card with nothing else on it. This is the one
  // screen where the only useful action is to type two fields, so the page gives
  // no competing targets and the first field is already focused.
  import { api } from '../lib/api'
  import Input from '../lib/ui/Input.svelte'
  import Button from '../lib/ui/Button.svelte'
  import PasswordInput from '../lib/ui/PasswordInput.svelte'

  let { onsuccess, notice = '' }: { onsuccess: () => void; notice?: string } = $props()
  let username = $state('')
  let password = $state('')
  let error = $state('')
  let busy = $state(false)
  // Seconds left before another attempt is worth making. The server's limiter
  // refills roughly one attempt every five seconds, so counting down is honest
  // and stops the reflex of hammering a button that will only be refused again.
  let cooldown = $state(0)

  $effect(() => {
    if (cooldown <= 0) return
    const t = setInterval(() => (cooldown = Math.max(0, cooldown - 1)), 1000)
    return () => clearInterval(t)
  })

  const blocked = $derived(busy || cooldown > 0 || !username.trim() || !password)

  async function submit() {
    if (blocked) return
    busy = true
    error = ''
    try {
      await api.login(username.trim(), password)
      password = ''
      onsuccess()
    } catch (e: any) {
      if (e.status === 401) {
        // The password is left in the field on purpose: it is likely a long
        // generated string, and clearing it would mean retyping all of it to fix
        // a single character. Reveal it instead.
        error = 'That username and password did not match.'
      } else if (e.status === 429) {
        error = 'Too many attempts.'
        cooldown = 5
      } else {
        error = e.message
      }
    } finally {
      busy = false
    }
  }
</script>

<div class="screen">
  <div class="card">
    <div class="head">
      <h2>Sign in</h2>
      <!-- A notice is something that just happened, so it carries text weight;
           the standing description does not. -->
      <p class="hint" class:told={notice}>{notice || 'Glance is behind a single administrator account.'}</p>
    </div>

    <form
      onsubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <div class="field">
        <label for="glance-user">Username</label>
        <!-- svelte-ignore a11y_autofocus -->
        <Input
          id="glance-user"
          bind:value={username}
          autocomplete="username"
          placeholder="admin"
          required
          autofocus
          disabled={busy}
        />
      </div>

      <PasswordInput bind:value={password} label="Password" autocomplete="current-password" required disabled={busy} />

      <!-- Announced rather than merely shown: after a failed submit the focus is
           still on the button, so a screen reader would otherwise say nothing.
           role="alert" is announced when the element appears, so no empty live
           region has to sit here reserving space. -->
      {#if error}
        <p class="bad" role="alert">
          {error}
          {#if cooldown > 0}<span class="wait">Try again in {cooldown}s.</span>{/if}
        </p>
      {/if}

      <Button type="submit" disabled={blocked}>
        {busy ? 'Signing in' : cooldown > 0 ? `Wait ${cooldown}s` : 'Sign in'}
      </Button>
    </form>

    <details>
      <summary>First time here?</summary>
      <p class="hint">
        The account is <code>admin</code>, and its password was printed once when the server first
        started — the log line is <code>auth.first_run_credential</code>. Only a hash is kept, so if
        that line is gone, set <code>GLANCE_ADMIN_USER</code> and <code>GLANCE_ADMIN_PASSWORD</code>
        and restart.
      </p>
    </details>
  </div>
</div>

<style>
  /* Centred in the space the header leaves, rather than pinned to the top-left
     corner of a page whose other furniture is hidden on this screen. */
  .screen { display: grid; place-items: center; min-height: 55vh; }
  .card {
    width: 100%;
    max-width: 340px;
    display: flex;
    flex-direction: column;
    gap: var(--up-space-4);
    padding: var(--up-space-5);
    border: 1px solid var(--up-border-hairline);
    border-radius: var(--up-radius-card);
    background: var(--up-bg);
  }
  .head { display: flex; flex-direction: column; gap: 4px; }
  h2 { font: var(--up-type-status-line); font-weight: 700; margin: 0; }
  .hint { font: var(--up-type-meta); color: var(--up-text-muted); line-height: 1.5; margin: 0; }
  .hint code { font: var(--up-type-code); }
  .hint.told { color: var(--up-ink); }
  form { display: flex; flex-direction: column; gap: var(--up-space-4); }
  .field { display: flex; flex-direction: column; gap: 6px; }
  label { font: var(--up-type-ui); color: var(--up-text-secondary); }
  .bad { font: var(--up-type-meta); color: var(--up-degraded-strong); margin: 0; line-height: 1.5; }
  .wait { color: var(--up-text-muted); }
  details { border-top: 1px solid var(--up-border-hairline); padding-top: var(--up-space-3); }
  summary { font: var(--up-type-ui); color: var(--up-text-muted); cursor: pointer; }
  summary:hover { color: var(--up-ink); }
  details p { margin: var(--up-space-2) 0 0; }
</style>
