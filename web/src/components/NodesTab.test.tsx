import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Me, NodeRow } from '../api/types'
import { setPickedNodes } from '../lib/fleets'
import { parseHash } from '../lib/router'
import { HeadPickBox, RowPickBox } from './NodePicks'
import { NodesTab } from './NodesTab'
import { ToolboxContext, type ToolboxApi } from './toolbox/context'

const row = (name: string) =>
  ({
    name,
    role: 'worker',
    roles: [],
    status: 'Ready',
    problems: [],
    errors: 0,
    warnings: 0,
    taints: [],
    labels: {},
    conditions: [],
    cpu: { allocatable: 1000, capacity: 1000 },
    memory: { allocatable: 1000, capacity: 1000 },
  }) as unknown as NodeRow
const rows = [row('n1'), row('n2'), row('n3')]

vi.mock('../api/live', () => ({
  useView: (view: string) => ({ rows: view === 'nodes' ? rows : [], loaded: true }),
  useLiveMeta: () => ({ settings: {} }),
}))
// The table's own tests are in DataTable.test.tsx; here it is the rows' boxes.
vi.mock('./DataTable', () => ({
  DataTable: ({ rows: shown }: { rows: NodeRow[] }) => (
    <div data-testid="table">
      <HeadPickBox />
      {shown.map((r) => (
        <RowPickBox key={r.name} name={r.name} />
      ))}
    </div>
  ),
}))

const route = parseHash('#/nodes')
const me = (admin: boolean) => ({ admin }) as Me

function nodes(admin: boolean, tb: Partial<ToolboxApi> = {}, onToast: (m: string) => void = () => undefined) {
  const api = { available: true, cluster: route.cluster, openTool: vi.fn(), action: vi.fn(), showSession: vi.fn(), showWatson: vi.fn(), ...tb }
  render(
    <ToolboxContext.Provider value={api}>
      <NodesTab cluster={null} now={0} onToast={onToast} route={route} me={me(admin)} />
    </ToolboxContext.Provider>,
  )
  return api
}
const pick = (i: number) => fireEvent.click(screen.getAllByTestId('fleet-pick-node')[i]!)

describe('picked nodes on the Nodes page', () => {
  const answer = vi.fn()
  const facts = vi.fn()
  beforeEach(() => {
    answer.mockReturnValue({ off: {}, ssh: { enabled: true, pairings: {} } })
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      if (String(url).includes('automation/facts')) return new Response(JSON.stringify(facts(JSON.parse(String(init?.body)))))
      return new Response(JSON.stringify(answer()))
    })
  })
  afterEach(() => {
    cleanup()
    setPickedNodes(route.cluster, [])
    vi.unstubAllGlobals()
  })

  it('gives the boxes and the bar to no one who could not act on them', () => {
    setPickedNodes(route.cluster, ['n1'])
    nodes(false)
    expect(screen.queryByTestId('fleet-pick-node')).toBeNull()
    expect(screen.queryByTestId('fleet-nodebar')).toBeNull()
  })

  it('puts the bar above the table, in the flow, with the picks of the Fleets tab', () => {
    setPickedNodes(route.cluster, ['n2'])
    nodes(true, { available: false })
    const bar = screen.getByTestId('fleet-nodebar')
    expect(bar.textContent).toContain('Picked 1 node')
    expect(bar.style.position).toBe('static')
    expect(bar.compareDocumentPosition(screen.getByTestId('table')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getByTestId('fleet-nodebar-new').textContent).toBe('New fleet…')
  })

  it('picks all in view from the heading, half ticked when some are, and Cancel drops them', () => {
    nodes(true, { available: false })
    const all = screen.getByTestId('fleet-pick-all') as HTMLInputElement
    expect(all.checked).toBe(false)
    expect(all.title).toBe('Pick the 3 nodes in view')
    pick(0)
    expect(all.indeterminate).toBe(true)
    fireEvent.click(all)
    expect(screen.getByTestId('fleet-nodebar').textContent).toContain('Picked 3 nodes')
    expect(all.checked).toBe(true)
    expect(all.indeterminate).toBe(false)
    expect(all.title).toBe('Unpick the 3 nodes in view')
    fireEvent.click(all)
    expect(screen.queryByTestId('fleet-nodebar')).toBeNull()
    fireEvent.click(all)
    fireEvent.click(screen.getByTestId('fleet-nodebar-cancel'))
    expect(screen.queryByTestId('fleet-nodebar')).toBeNull()
  })

  it("collects the picked nodes' facts and says which answered", async () => {
    facts.mockReturnValue({
      nodes: [
        { cluster: 'local', node: 'n1', at: '' },
        { cluster: 'local', node: 'n3', at: '', error: 'timed out' },
      ],
    })
    const toast = vi.fn()
    nodes(true, { available: false }, toast)
    pick(0)
    pick(1)
    pick(2)
    const collect = screen.getByTestId('fleet-nodebar-facts') as HTMLButtonElement
    fireEvent.click(collect)
    expect(collect.textContent).toBe('Collecting…')
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Collected facts from 1 of 3 nodes. 1 did not answer. 1 skipped: not paired for SSH, or in no fleet.'))
    expect(facts).toHaveBeenCalledWith({ nodes: ['n1', 'n2', 'n3'] })
    expect(collect.textContent).toBe('Collect facts')
  })

  it('sends the picked nodes to one pairing dialog', async () => {
    const tb = nodes(true, { cluster: 'pair-on' })
    pick(2)
    pick(0)
    const pair = screen.getByTestId('fleet-nodebar-ssh') as HTMLButtonElement
    await waitFor(() => expect(answer).toHaveBeenCalled())
    fireEvent.click(pair)
    expect(tb.openTool).toHaveBeenCalledWith({ tool: 'ssh-pair', nodes: ['n3', 'n1'], from: 'Nodes page' })
  })

  it('says why there is no pairing', async () => {
    answer.mockReturnValue({ off: {} })
    nodes(true, { cluster: 'pair-off' })
    pick(0)
    const pair = screen.getByTestId('fleet-nodebar-ssh') as HTMLButtonElement
    await waitFor(() => expect(pair.disabled).toBe(true))
    expect(pair.title).toBe('SSH is turned off on this console')
    cleanup()
    nodes(true, { available: false })
    expect(screen.getByTestId('fleet-nodebar-ssh').title).toBe('Watson is off here')
  })
})
