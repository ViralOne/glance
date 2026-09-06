/**
 * Copying text to the clipboard, including on plain HTTP.
 *
 * `navigator.clipboard` only exists in a secure context — HTTPS, or localhost.
 * A self-hosted Glance reached over plain `http://` therefore has no
 * `navigator.clipboard` at all, and every copy button silently did nothing:
 * the call threw, the `catch` swallowed it, and no "Copied" ever appeared.
 *
 * So there are two paths. The modern API when it is there, and otherwise a
 * hidden textarea with `document.execCommand('copy')`, which is deprecated but
 * is the only thing that works without TLS and is still supported everywhere.
 * The return value says whether it worked, so the caller can show a failure
 * instead of pretending.
 */
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission refused, or a non-secure context that still exposes the
      // object. Fall through rather than giving up.
    }
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const el = document.createElement('textarea')
  el.value = text
  // Off-screen rather than hidden: execCommand needs a selectable element, and
  // display:none or visibility:hidden make it unselectable.
  el.setAttribute('readonly', '')
  el.style.position = 'fixed'
  el.style.top = '-1000px'
  el.style.opacity = '0'
  document.body.appendChild(el)
  try {
    el.select()
    el.setSelectionRange(0, text.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(el)
  }
}
