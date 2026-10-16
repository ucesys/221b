/**
 * Runs start once the page has loaded, at once if it has; the returned
 * function cancels a start still waiting. Event streams opened while a
 * page loads hold its load event in some browsers (Firefox), and take
 * connections the page's own files need; the assistants' streams wait.
 */
export function whenLoaded(start: () => void): () => void {
  if (typeof document === 'undefined' || document.readyState === 'complete') {
    start()
    return () => undefined
  }
  const go = () => start()
  window.addEventListener('load', go, { once: true })
  return () => window.removeEventListener('load', go)
}
