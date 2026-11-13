import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { FindHit, FindResult } from '../../api/knowledge'
import { findHref, Marked, MycroftSearchBox, MycroftSearchResults, useMycroftFind } from './MycroftSearch'

const hits: FindHit[] = [
  { kind: 'knownIssue', id: 'KI-3', title: 'Checkout hangs in the EU store', status: 'identified', date: '2026-11-02T10:00:00Z', text: 'Symptoms: checkout hangs after pressing pay.', score: 4.2, terms: ['checkout', 'hang'] },
  { kind: 'page', path: 'pages/runbook/checkout.md', title: 'Runbook: checkout', date: '2026-10-01T00:00:00Z', stale: true, text: 'When checkout hangs, restart the web pods.', score: 3.1, terms: ['checkout', 'hang'] },
  { kind: 'exception', id: 'EX-4', title: 'Checkout runs without a PDB', status: 'active', text: 'Justification: one replica.', score: 1, terms: ['checkout'] },
  { kind: 'excerpt', title: 'Shop notes', url: 'https://wiki.example.com/shop', text: 'checkout is slow', score: 0.5, terms: ['checkout'] },
]
const answer = (over: Partial<FindResult> = {}): FindResult => ({ hits, counts: { knownIssue: 1, page: 1, exception: 1, excerpt: 1 }, kinds: ['page', 'knownIssue', 'fix', 'exception', 'excerpt'], ...over })

let urls: string[] = []
let reply: (url: string) => unknown = () => answer()

function Harness() {
  const find = useMycroftFind()
  return (
    <div>
      <MycroftSearchBox find={find} />
      {find.active ? <MycroftSearchResults find={find} /> : <p data-testid="sub-tab">the sub-tab</p>}
    </div>
  )
}

describe('One search across Mycroft (KN-35)', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
  })
  beforeEach(() => {
    urls = []
    reply = () => answer()
    window.location.hash = '#/aiops/mycroft/wiki'
    vi.stubGlobal('fetch', async (url: string) => {
      urls.push(String(url))
      return new Response(JSON.stringify(reply(String(url))))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('searches after a pause, in place of the sub-tab: every kind ranked together, with its id, status, date and the words matched', async () => {
    render(<Harness />)
    const box = screen.getByTestId('mycroft-search')
    fireEvent.change(box, { target: { value: 'c' } })
    expect(screen.getByTestId('sub-tab')).toBeTruthy()
    fireEvent.change(box, { target: { value: 'checkout hang' } })
    expect(screen.queryByTestId('sub-tab')).toBeNull()
    const rows = await screen.findAllByTestId('mycroft-search-hit')
    expect(urls).toEqual(['/api/v1/aiops/mycroft/search?q=checkout%20hang&limit=30'])
    expect(rows.map((r) => r.querySelector('.rk-pill')?.textContent)).toEqual(['Known issue', 'Page', 'Exception', 'Source'])
    for (const want of ['KI-3', 'Checkout hangs in the EU store', 'identified', '2026-11-02']) expect(rows[0]!.textContent).toContain(want)
    for (const want of ['Runbook: checkout', 'Stale', '2026-10-01', 'runbook/checkout.md']) expect(rows[1]!.textContent).toContain(want)
    expect([...rows[0]!.querySelectorAll('mark')].map((m) => m.textContent)).toEqual(['checkout', 'hangs'])
    expect(screen.getByTestId('mycroft-search-kind-all').textContent).toBe('All 4')
    expect(screen.getByTestId('mycroft-search-kind-fix').textContent).toBe('Fixes 0')
    expect(screen.getByTestId('mycroft-search-status').textContent).toBe('4 found')
  })

  it('narrows to one kind with its chip, asking the server for that kind', async () => {
    render(<Harness />)
    fireEvent.change(screen.getByTestId('mycroft-search'), { target: { value: 'checkout' } })
    await screen.findAllByTestId('mycroft-search-hit')
    reply = () => answer({ hits: [hits[2]!] })
    fireEvent.click(screen.getByTestId('mycroft-search-kind-exception'))
    await waitFor(() => expect(screen.getAllByTestId('mycroft-search-hit')).toHaveLength(1))
    expect(urls.at(-1)).toBe('/api/v1/aiops/mycroft/search?q=checkout&kind=exception&limit=30')
    expect(screen.getByTestId('mycroft-search-kind-exception').getAttribute('aria-pressed')).toBe('true')
  })

  it('is used from the keyboard: ↓ ↑ move, Enter opens the hit and clears the box, Escape clears it', async () => {
    render(<Harness />)
    const box = screen.getByTestId('mycroft-search')
    fireEvent.change(box, { target: { value: 'checkout' } })
    await screen.findAllByTestId('mycroft-search-hit')
    expect(box.getAttribute('aria-activedescendant')).toBe('mycroft-find-0')
    fireEvent.keyDown(box, { key: 'ArrowDown' })
    fireEvent.keyDown(box, { key: 'ArrowDown' })
    fireEvent.keyDown(box, { key: 'ArrowUp' })
    expect(screen.getAllByTestId('mycroft-search-hit')[1]!.getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(window.location.hash).toBe('#/aiops/mycroft/wiki/pages/runbook/checkout.md')
    expect((box as HTMLInputElement).value).toBe('')
    expect(screen.getByTestId('sub-tab')).toBeTruthy()
    fireEvent.change(box, { target: { value: 'checkout' } })
    await screen.findAllByTestId('mycroft-search-hit')
    fireEvent.keyDown(box, { key: 'Escape' })
    expect((box as HTMLInputElement).value).toBe('')
    expect(screen.getByTestId('sub-tab')).toBeTruthy()
  })

  it('says when nothing matches, and when the person may search nothing', async () => {
    reply = () => answer({ hits: [], counts: {} })
    render(<Harness />)
    fireEvent.change(screen.getByTestId('mycroft-search'), { target: { value: 'nothing' } })
    await waitFor(() => expect(screen.getByTestId('mycroft-search-hits').textContent).toBe('Nothing matches.'))
    cleanup()
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ error: 'no' }), { status: 403 }))
    render(<Harness />)
    fireEvent.change(screen.getByTestId('mycroft-search'), { target: { value: 'anything' } })
    expect((await screen.findByTestId('mycroft-search-error')).textContent).toBe('Your roles let you search nothing in Mycroft.')
  })

  it('opens each kind where it lives', () => {
    expect(hits.map(findHref)).toEqual(['#/aiops/mycroft/known/KI-3', '#/aiops/mycroft/wiki/pages/runbook/checkout.md', '#/aiops/mycroft/exceptions/EX-4', 'https://wiki.example.com/shop'])
    expect(findHref({ kind: 'fix', id: 'F-2', title: '', text: '', score: 0 })).toBe('#/aiops/mycroft/fixes/F-2')
    expect(findHref({ kind: 'excerpt', title: '', text: '', score: 0 })).toBe('#/aiops/mycroft/wiki/sources')
  })

  it('marks each word that starts with a term searched, whatever its case', () => {
    const { container } = render(<Marked text="Restarts and restarting; prestart is not one, nor aXb." terms={['restart', 'a.b']} />)
    expect([...container.querySelectorAll('mark')].map((m) => m.textContent)).toEqual(['Restarts', 'restarting'])
  })
})
