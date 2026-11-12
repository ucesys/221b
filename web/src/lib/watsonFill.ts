// Open in Watson (Phase 12, IB-13): a page hands Watson a command for a
// cluster, and the console opens Watson's pane on that cluster with the
// command in its box. Nothing runs by itself: the person reads it and
// presses Enter, and it runs as them, through Watson's rules and cards.

/** Fired on window with the cluster and the command; the console's shell (App.tsx) listens. */
export const WATSON_FILL = '221b-watson-fill'

export interface WatsonFill {
  cluster: string
  command: string
}

/** Opens Watson on a cluster with a command waiting in its box, for the person to run. */
export function openWatson(cluster: string, command: string): void {
  window.dispatchEvent(new CustomEvent<WatsonFill>(WATSON_FILL, { detail: { cluster, command } }))
}

/** Listens for openWatson; returns the function that stops listening. */
export function onWatsonFill(fn: (f: WatsonFill) => void): () => void {
  const listen = (e: Event) => {
    const d = (e as CustomEvent<WatsonFill>).detail
    if (d && typeof d.cluster === 'string' && typeof d.command === 'string') fn(d)
  }
  window.addEventListener(WATSON_FILL, listen)
  return () => window.removeEventListener(WATSON_FILL, listen)
}
