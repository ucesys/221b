import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { WatsonItem, WatsonState } from '../api/watson'
import { ApproveAll, WatsonPane } from './Watson'

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
