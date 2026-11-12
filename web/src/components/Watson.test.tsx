import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { WatsonItem, WatsonState } from '../api/watson'
import { ApproveAll, splitPending, WatsonPane } from './Watson'

afterEach(cleanup)

describe('Approve all for… (R2-04)', () => {
  it('grants nothing on choosing a length: only the button does', () => {
    const give = vi.fn()
    render(<ApproveAll max={30} busy={false} give={give} />)
    const go = screen.getByTestId('watson-approve-all-go') as HTMLButtonElement
    expect(go.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('watson-approve-all'), { target: { value: '5' } })
    expect(give).not.toHaveBeenCalled()
    expect(go.disabled).toBe(false)
    fireEvent.click(go)
    expect(give).toHaveBeenCalledExactlyOnceWith(5)
  })

  it('offers lengths up to the limit', () => {
    render(<ApproveAll max={15} busy={false} give={vi.fn()} />)
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['Approve all for…', '5 minutes', '15 minutes'])
  })
})

describe('Watson’s history walk', () => {
  const card = (id: string, command: string, created: string) => ({ id, command, kind: 'read', from: '', status: 'done', by: 'me', mine: true, created, exit: 0, canApprove: false, lineStart: 0, rev: 1 }) as WatsonItem
  const state = (items: WatsonItem[]): WatsonState => ({ status: null, items, live: true })
  const pane = (items: WatsonItem[]) => <WatsonPane cluster="local" clusterName="local" state={state(items)} onToast={() => {}} />
  const typed = () => (screen.getByTestId('watson-input') as HTMLInputElement).value
  const key = (k: string) => fireEvent.keyDown(screen.getByTestId('watson-input'), { key: k })
  const two = [card('1', 'kubectl get a', '2026-10-30T10:00:00Z'), card('2', 'kubectl get b', '2026-10-30T10:01:00Z')]
  beforeEach(() => localStorage.clear())

  it('does not jump when the stream brings a card during the walk', () => {
    const { rerender } = render(pane(two))
    key('ArrowUp')
    expect(typed()).toBe('kubectl get b')
    // A new card arrives and an old one is re-dated: the walk goes on through the list it started with.
    rerender(pane([...two, card('3', 'kubectl get c', '2026-10-30T10:02:00Z')]))
    key('ArrowUp')
    expect(typed()).toBe('kubectl get a')
    key('ArrowDown')
    expect(typed()).toBe('kubectl get b')
    key('ArrowDown')
    expect(typed()).toBe('')
    // The walk over, the next one starts from the list as it is now.
    key('ArrowUp')
    expect(typed()).toBe('kubectl get c')
  })
})

describe('Open in Watson (IB-13)', () => {
  it('puts the command in the box for the person to run, and sends nothing', () => {
    const sent = vi.fn(() => Promise.resolve(new Response('{}')))
    vi.stubGlobal('fetch', sent)
    const filled = vi.fn()
    const state: WatsonState = { status: null, items: [], live: true }
    render(<WatsonPane cluster="prod" clusterName="prod" state={state} onToast={() => {}} fill="kubectl rollout restart deployment web -n shop" onFilled={filled} />)
    expect((screen.getByTestId('watson-input') as HTMLInputElement).value).toBe('kubectl rollout restart deployment web -n shop')
    expect(filled).toHaveBeenCalledOnce()
    expect(document.activeElement).toBe(screen.getByTestId('watson-input'))
    expect(sent).not.toHaveBeenCalledWith(expect.stringContaining('/watson/'), expect.objectContaining({ method: 'POST' }))
    vi.unstubAllGlobals()
  })
})

describe('Requests do not pile up: only the newest is active (CF-65)', () => {
  const change = (id: string, created: string, over: Partial<WatsonItem> = {}) =>
    ({ id, command: `kubectl cordon node-${id}`, kind: 'change', from: 'Typed', status: 'pending', by: 'me', mine: true, created, expires: '2099-01-01T00:00:00Z', exit: 0, canApprove: true, lineStart: 0, rev: 1, ...over }) as WatsonItem
  const status = { available: true, readOnly: false, twoPerson: false, env: '', signedIn: true, me: 'me', prompt: 'me@local:~$', lapseSeconds: 600, reads: ['get'], changes: ['cordon'], admin: false }
  const pane = (items: WatsonItem[], scope?: { from: string; label: string }) => <WatsonPane cluster="local" clusterName="local" state={{ status, items, live: true }} onToast={() => {}} scope={scope} />
  const shown = () => screen.queryAllByTestId('watson-card').map((c) => c.getAttribute('data-id'))
  const first = change('1', '2026-11-08T10:00:00Z')
  const second = change('2', '2026-11-08T10:01:00Z')
  let sent: ReturnType<typeof vi.fn>
  beforeEach(() => {
    sent = vi.fn(() => Promise.resolve(new Response('{}')))
    vi.stubGlobal('fetch', sent)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows the newest as a card and folds the earlier one into a line, keeping the waiting count', () => {
    render(pane([second, first]))
    expect(shown()).toEqual(['2'])
    const line = screen.getByTestId('watson-earlier')
    expect(line.textContent).toContain('1 earlier request left waiting')
    expect(line.getAttribute('aria-expanded')).toBe('false')
    // Both still wait for the person's approval.
    expect(screen.getByTestId('watson-waiting-count').textContent).toBe('2 waiting')
  })

  it('expands to show the earlier ones as cards, and folds them again', () => {
    render(pane([first, second, change('3', '2026-11-08T10:02:00Z')]))
    const line = screen.getByTestId('watson-earlier')
    expect(line.textContent).toContain('2 earlier requests left waiting')
    fireEvent.click(line)
    expect(line.getAttribute('aria-expanded')).toBe('true')
    // The earlier ones first, the active one last.
    expect(shown()).toEqual(['1', '2', '3'])
    fireEvent.click(line)
    expect(shown()).toEqual(['3'])
  })

  it('never discards the earlier ones: nothing is sent', () => {
    const { rerender } = render(pane([first, second]))
    fireEvent.click(screen.getByTestId('watson-earlier'))
    rerender(pane([first, second, change('3', '2026-11-08T10:02:00Z')]))
    expect(sent).not.toHaveBeenCalled()
    expect(screen.getByTestId('watson-waiting-count').textContent).toBe('3 waiting')
  })

  it('brings nothing earlier back when the newest is decided', () => {
    const { rerender } = render(pane([first, second]))
    rerender(pane([first, { ...second, status: 'discarded' }]))
    expect(shown()).toEqual([])
    expect(screen.getByTestId('watson-earlier').textContent).toContain('1 earlier request left waiting')
    // A read decided from its card counts as well, in this pane.
    const read = change('4', '2026-11-08T10:03:00Z', { kind: 'read', command: 'kubectl get pods' })
    rerender(pane([first, read]))
    expect(shown()).toEqual(['4'])
    rerender(pane([first, { ...read, status: 'done' }]))
    expect(shown()).toEqual([])
    // A new request is active at once.
    rerender(pane([first, { ...read, status: 'done' }, change('5', '2026-11-08T10:04:00Z')]))
    expect(shown()).toEqual(['5'])
  })

  it('works out the same in a pane opened later, and within a scope', () => {
    expect(splitPending([first, { ...second, status: 'running' }]).active).toBeUndefined()
    expect(splitPending([first, { ...second, kind: 'read', status: 'done' }]).active?.id).toBe('1')
    render(pane([first, { ...second, from: 'case:C-1' }], { from: 'case:C-1', label: 'C-1' }))
    expect(shown()).toEqual(['2'])
    expect(screen.queryByTestId('watson-earlier')).toBeNull()
  })
})
