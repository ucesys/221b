import { describe, expect, it } from 'vitest'
import { mergeHistory } from './Watson'

// Watson's recall history holds the typed commands and the person's
// cards alike, in the order they were sent (asked October 30, 2026).
describe('Watson history', () => {
  const card = (command: string, created: string, extra: Partial<{ mine: boolean; session: unknown }> = {}) => ({ command, created, mine: true, ...extra })

  it('orders typed commands and cards by time, each once, the newest last', () => {
    const typed = [
      { cmd: 'kubectl get nodes', at: '2026-10-30T10:00:00Z' },
      { cmd: 'kubectl exec -it web-0 -- sh', at: '2026-10-30T10:02:00Z' },
    ]
    const cards = [card('helm upgrade shop --git x --ref main -n web --app', '2026-10-30T09:00:00Z'), card('kubectl get nodes', '2026-10-30T10:00:01Z'), card('helm uninstall old -n web', '2026-10-30T09:30:00Z', { mine: false }), card('toolbox shell', '2026-10-30T10:03:00Z', { session: {} })]
    expect(mergeHistory(typed, cards)).toEqual(['helm upgrade shop --git x --ref main -n web --app', 'kubectl get nodes', 'kubectl exec -it web-0 -- sh'])
  })

  it('puts entries without a time first, and keeps fifty', () => {
    const typed = [{ cmd: 'old', at: '' }]
    const cards = Array.from({ length: 60 }, (_, i) => card(`c${i}`, `2026-10-30T10:${String(i).padStart(2, '0')}:00Z`))
    const h = mergeHistory(typed, cards)
    expect(h).toHaveLength(50)
    expect(h[49]).toBe('c59')
    expect(h).not.toContain('old')
    expect(mergeHistory(typed, cards.slice(0, 3))).toEqual(['old', 'c0', 'c1', 'c2'])
  })
})
