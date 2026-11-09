import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Fleet, NodeRow } from '../api/types'
import { FleetEditor } from './FleetEditor'
import { ToolboxContext, type ToolboxApi } from './toolbox/context'

const row = (name: string, labels: Record<string, string> = {}, host = false) => ({ name, role: host ? 'host' : 'worker', labels, taints: [], ...(host ? { host: {} } : {}) }) as unknown as NodeRow
// h1 is a host outside Kubernetes, in the fleet by its label.
const nodes = [row('n1', { tier: 'db' }), row('n2'), row('n3', { tier: 'db' }), row('h1', { tier: 'db' }, true)]
const fleet: Fleet = { id: 'f1', name: 'Databases', color: 0, labels: ['tier=db'], taints: [] }

function editor(f: Fleet | null, tb: Partial<ToolboxApi> = {}, onToast = vi.fn()) {
  const api = { available: true, cluster: 'local', openTool: vi.fn(), action: vi.fn(), showSession: vi.fn(), showWatson: vi.fn(), ...tb }
  render(
    <ToolboxContext.Provider value={api}>
      <FleetEditor seed={{ fleet: f }} fleets={f ? [f] : []} nodes={nodes} now={0} onClose={() => undefined} onSave={async () => undefined} onDelete={async () => undefined} onToast={onToast} />
    </ToolboxContext.Provider>,
  )
  return { api, onToast }
}

describe("the fleet drawer's Tools", () => {
  const facts = vi.fn()
  const toolbox = vi.fn()
  beforeEach(() => {
    toolbox.mockReturnValue({ off: {}, namespaces: {}, nodes: {}, ssh: { enabled: true, pairings: {} } })
    facts.mockReturnValue({ status: 200, body: { nodes: [], keys: [] } })
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      if (String(url).includes('automation/facts')) {
        const f = facts(init?.method === 'POST' ? JSON.parse(String(init.body)) : undefined)
        return new Response(JSON.stringify(f.body), { status: f.status })
      }
      return new Response(JSON.stringify(toolbox()))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('are only on a saved fleet', () => {
    editor(null)
    expect(screen.queryByTestId('tools-fleet-New fleet')).toBeNull()
    expect(document.querySelector('.tb-tools')).toBeNull()
  })

  it("pair the fleet's Kubernetes nodes for SSH, one session each, and leave its hosts to their panel", async () => {
    const { api } = editor(fleet)
    fireEvent.click(screen.getByTestId('tools-fleet-Databases'))
    expect(screen.getByTestId('tools-menu').textContent).toContain('Tools for fleet Databases · 3 nodes')
    const pair = screen.getByTestId('tool-ssh-pair')
    await waitFor(() => expect(pair.getAttribute('aria-disabled')).toBeNull())
    fireEvent.click(pair)
    expect(api.openTool).toHaveBeenCalledWith({ tool: 'ssh-pair', nodes: ['n1', 'n3'], from: 'Fleet' })
  })

  it("collect the fleet's facts and say how it went", async () => {
    facts.mockImplementation((body?: { nodes: string[] }) => (body ? { status: 200, body: { nodes: [{ cluster: 'local', node: 'n1', at: '' }, { cluster: 'local', node: 'n3', at: '' }, { cluster: 'local', node: 'h1', at: '' }] } } : { status: 200, body: { nodes: [], keys: [] } }))
    const { onToast } = editor(fleet)
    fireEvent.click(screen.getByTestId('tools-fleet-Databases'))
    const collect = screen.getByTestId('tool-facts')
    await waitFor(() => expect(collect.getAttribute('aria-disabled')).toBeNull())
    fireEvent.click(collect)
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('Collected facts from 3 of 3 nodes.'))
    expect(facts).toHaveBeenCalledWith({ nodes: ['n1', 'n3', 'h1'] })
  })

  it('say why not: no SSH, no Automation, no nodes', async () => {
    toolbox.mockReturnValue({ off: {}, namespaces: {}, nodes: {} })
    facts.mockReturnValue({ status: 403, body: { error: 'Automation is for SuperAdmins' } })
    // The toolbox's answer is kept per cluster.
    editor(fleet, { cluster: 'no-ssh' })
    fireEvent.click(screen.getByTestId('tools-fleet-Databases'))
    await waitFor(() => expect(screen.getByTestId('tool-ssh-pair').title).toBe('Pair for SSH: SSH is turned off on this console'))
    await waitFor(() => expect(screen.getByTestId('tool-facts').getAttribute('aria-disabled')).toBe('true'))
    cleanup()
    editor({ ...fleet, labels: ['tier=none'] })
    fireEvent.click(screen.getByTestId('tools-fleet-Databases'))
    await waitFor(() => expect(screen.getByTestId('tool-facts').title).toBe('Collect facts: no node is in this fleet'))
    cleanup()
    editor({ ...fleet, kind: 'nodes', labels: [], nodes: ['h1'] })
    fireEvent.click(screen.getByTestId('tools-fleet-Databases'))
    await waitFor(() => expect(screen.getByTestId('tool-ssh-pair').title).toBe('Pair for SSH: its hosts pair from their own panel'))
  })
})
