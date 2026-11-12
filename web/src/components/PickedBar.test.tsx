import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { saveFleets } from '../api/fleets'
import type { Fleet, FleetRow, NodeRow } from '../api/types'
import { NodePicking, PickBox, RowPickBox, usePickedNodes } from './NodePicks'
import { PickedBar } from './PickedBar'

vi.mock('../api/fleets', () => ({ saveFleets: vi.fn(async () => undefined) }))

const node = (name: string, labels: Record<string, string> = {}) => ({ name, labels, taints: [] }) as unknown as NodeRow
const list = (id: string, nodes: string[]): Fleet => ({ id, name: id, color: 0, labels: [], taints: [], kind: 'nodes', nodes })
const [a, b] = [node('a'), node('b')]

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function bar(fleets: Fleet[], over: Partial<Parameters<typeof PickedBar>[0]> = {}) {
  const calls = { onCancel: vi.fn(), onCreated: vi.fn(), onToast: vi.fn() }
  render(<PickedBar cluster="c1" nodes={[a, b]} all={[a, b]} fleets={fleets} rows={[] as FleetRow[]} {...calls} {...over} />)
  return calls
}

describe('picked nodes', () => {
  it('are kept per cluster after the page goes, and gone nodes drop out', () => {
    const first = renderHook(() => usePickedNodes('p1', [a, b]))
    act(() => first.result.current.togglePick('a'))
    act(() => first.result.current.togglePick('b'))
    first.unmount()
    expect(renderHook(() => usePickedNodes('p1', [a, b])).result.current.picks).toEqual(['a', 'b'])
    expect(renderHook(() => usePickedNodes('p1', [b])).result.current.pickedRows).toEqual([b])
    expect(renderHook(() => usePickedNodes('p2', [a, b])).result.current.picks).toEqual([])
    const second = renderHook(() => usePickedNodes('p1', [a, b]))
    act(() => second.result.current.togglePick('a'))
    expect(second.result.current.picks).toEqual(['b'])
  })

  it('follow the cluster the page moves to', () => {
    const hook = renderHook(({ cluster }) => usePickedNodes(cluster, [a, b]), { initialProps: { cluster: 'p3' } })
    act(() => hook.result.current.togglePick('a'))
    hook.rerender({ cluster: 'p4' })
    expect(hook.result.current.picks).toEqual([])
    hook.rerender({ cluster: 'p3' })
    expect(hook.result.current.picks).toEqual(['a'])
  })
})

describe('the pick box', () => {
  it('has the 24px target, and a click on it leaves the row alone', () => {
    const row = vi.fn()
    const toggle = vi.fn()
    const { container } = render(
      <div onClick={row}>
        <PickBox name="a" picked={false} onToggle={toggle} />
      </div>,
    )
    const target = container.querySelector('label.rk-pickbox')!
    fireEvent.click(target)
    expect(toggle).toHaveBeenCalledWith('a')
    expect(row).not.toHaveBeenCalled()
  })

  it('shows in a table only where the page offers the picks', () => {
    render(<RowPickBox name="a" />)
    expect(screen.queryByTestId('fleet-pick-node')).toBeNull()
    render(
      <NodePicking.Provider value={{ picked: new Set(['a']), toggle: () => undefined, shown: ['a'], setMany: () => undefined }}>
        <RowPickBox name="a" />
      </NodePicking.Provider>,
    )
    expect((screen.getByTestId('fleet-pick-node') as HTMLInputElement).checked).toBe(true)
  })
})

describe('the picked bar', () => {
  it('says how many, and offers only what can be done', () => {
    bar([])
    expect(screen.getByTestId('fleet-nodebar').textContent).toContain('Picked 2 nodes')
    expect((screen.getByTestId('fleet-nodebar-add') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('fleet-nodebar-remove') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('fleet-nodebar-new') as HTMLButtonElement).disabled).toBe(false)
  })

  it('floats unless the page keeps it in the flow, and takes the page’s own actions', () => {
    bar([], { inFlow: true, note: <span data-testid="note" />, children: <button data-testid="extra" /> })
    expect(screen.getByTestId('fleet-nodebar').style.position).toBe('static')
    expect(screen.getByTestId('note')).toBeTruthy()
    expect(screen.getByTestId('extra')).toBeTruthy()
    cleanup()
    bar([])
    expect(screen.getByTestId('fleet-nodebar').style.position).toBe('')
  })

  it('makes a list of the nodes through the one dialog, with its toast, and clears the picks', async () => {
    const { onCreated, onCancel, onToast } = bar([])
    fireEvent.click(screen.getByTestId('fleet-nodebar-new'))
    fireEvent.change(screen.getByTestId('fleet-nodes-name'), { target: { value: 'Batch' } })
    fireEvent.click(screen.getByTestId('fleet-nodes-go'))
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('Created the fleet “Batch”.'))
    expect(vi.mocked(saveFleets).mock.calls[0]![1][0]).toMatchObject({ name: 'Batch', kind: 'nodes', nodes: ['a', 'b'] })
    expect(onCreated).toHaveBeenCalledWith(expect.objectContaining({ name: 'Batch' }))
    expect(onCancel).toHaveBeenCalled()
    expect(screen.queryByTestId('fleet-nodes')).toBeNull()
  })

  it('takes the nodes off a list', async () => {
    const { onToast } = bar([list('l1', ['a', 'z'])])
    fireEvent.click(screen.getByTestId('fleet-nodebar-remove'))
    fireEvent.click(screen.getByTestId('fleet-nodes-go'))
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('Removed 1 node from “l1”.'))
    expect(vi.mocked(saveFleets).mock.calls[0]![1][0]).toMatchObject({ id: 'l1', nodes: ['z'] })
  })

  it('opens the dialog on a fleet of labels too, which the page’s own path once left silent', () => {
    bar([{ id: 'gpu', name: 'gpu', color: 0, labels: ['pool=gpu'], taints: [] }])
    fireEvent.click(screen.getByTestId('fleet-nodebar-add'))
    expect(screen.getByTestId('fleet-nodes-commands').textContent).toContain('kubectl label nodes a b pool=gpu')
  })
})
