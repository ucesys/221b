// Copying to the clipboard. The asynchronous Clipboard API exists only in
// a secure context (HTTPS or localhost); a console reached at
// http://<address> has no navigator.clipboard at all, so a copy there
// falls back to selecting the text in a hidden field and the copy command,
// which browsers still allow during a click.

/** Copies text; resolves to whether it reached the clipboard. */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Refused (no permission, the page not focused): try the old way.
    }
  }
  return copyBySelection(text)
}

function copyBySelection(text: string): boolean {
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  // In the page but out of sight, without scrolling it.
  Object.assign(area.style, { position: 'fixed', top: '0', left: '0', width: '1px', height: '1px', padding: '0', border: '0', opacity: '0', pointerEvents: 'none' })
  const active = document.activeElement as HTMLElement | null
  const sel = document.getSelection()
  const range = sel && sel.rangeCount > 0 ? sel.getRangeAt(0) : null
  document.body.appendChild(area)
  area.focus({ preventScroll: true })
  area.select()
  area.setSelectionRange(0, text.length)
  let ok: boolean
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  area.remove()
  // Give back the focus and the selection the person had.
  active?.focus?.({ preventScroll: true })
  if (range && sel) {
    sel.removeAllRanges()
    sel.addRange(range)
  }
  return ok
}
