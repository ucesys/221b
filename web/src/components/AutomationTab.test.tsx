import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AutomationFleets, AutomationIndex, Run, Script } from '../api/automation'
import type { Me } from '../api/types'
import type { Route } from '../lib/router'
import { AutomationTab } from './AutomationTab'
import { rowProblems } from '../lib/variables'

const admin = {
  mode: 'local',
  user: { subject: 'pat', username: 'pat' },
  admin: true,
  method: 'local',
  sessionIdleSeconds: 0,
} as Me
const route = (path: string[] = [], query = ''): Route => ({
  cluster: 'local',
  view: 'tabs',
  tab: path[0] === 'runs' ? 'run-history' : 'scripts',
  path: [],
  params: new URLSearchParams(query),
})

const header = {
  describe: 'Keep chrony',
  params: [{ name: 'PACKAGE', required: true }],
  privilege: 'full' as const,
  dryRun: true,
}
const script: Script = {
  name: 'ensure-chrony',
  group: 'os',
  created: '2026-10-30T10:00:00Z',
  updated: '2026-10-31T10:00:00Z',
  versions: [
    {
      n: 1,
      text: '#v1',
      hash: 'h1',
      header,
      findings: [],
      state: 'approved',
      author: { name: 'Pat', user: 'pat' },
      created: '2026-10-30T10:00:00Z',
      approvals: [{ by: { name: 'Sam', user: 'sam' }, at: '2026-10-30T11:00:00Z' }],
      may: { approve: false, reject: false, involved: true, runProduction: true, runElsewhere: true, dry: true },
    },
    {
      n: 2,
      text: '#v2',
      hash: 'h2',
      header,
      findings: [{ level: 'warning', line: 3, code: 'reboot', msg: 'it reboots' }],
      state: 'draft',
      author: { name: 'Pat', user: 'pat' },
      created: '2026-10-31T10:00:00Z',
      may: { approve: false, reject: false, involved: true, runProduction: false, runElsewhere: true, dry: true },
    },
  ],
}
const index: AutomationIndex = {
  groups: [{ name: 'empty' }],
  off: '',
  me: { name: 'Pat', user: 'pat' },
  running: 0,
  limits: { text: 32768, timeout: 60, parallel: 20, nodes: 500 },
  scripts: [
    {
      name: 'ensure-chrony',
      group: 'os',
      describe: 'Keep chrony',
      version: 2,
      state: 'draft',
      inReview: 0,
      approved: 1,
      approvals: 1,
      privilege: 'full',
      dryRun: true,
      errors: 0,
      warnings: 1,
      created: script.created,
      updated: script.updated,
    },
  ],
}
const fleets: AutomationFleets = {
  cluster: 'local',
  production: false,
  fleets: [
    {
      id: 'workers',
      name: 'Workers',
      nodes: [
        { name: 'w1', paired: true, loginUser: 'root' },
        { name: 'w2', paired: false, why: 'not paired' },
      ],
      params: { values: { PACKAGE: 'chrony' }, ceiling: 'full' },
    },
    {
      id: '_all',
      name: 'All nodes',
      nodes: [
        { name: 'w1', paired: true, loginUser: 'root' },
        { name: 'w2', paired: false, why: 'not paired' },
        { name: 'w3', paired: true, loginUser: 'root' },
      ],
      params: { values: { PACKAGE: 'chrony' }, ceiling: 'full' },
    },
  ],
}
const run: Run = {
  id: 7,
  script: 'ensure-chrony',
  version: 1,
  hash: 'h1',
  dryRun: false,
  privilege: 'full',
  cluster: 'local',
  fleet: 'workers',
  fleetName: 'Workers',
  rollout: { mode: 'canary', stopAfter: 1, timeoutMinutes: 10 },
  by: { name: 'Pat' },
  state: 'succeeded',
  started: '2026-10-31T10:00:00Z',
  ended: '2026-10-31T10:00:30Z',
  beat: '2026-10-31T10:00:30Z',
  nodes: [
    {
      node: 'w1',
      batch: 0,
      state: 'changed',
      exit: 0,
      msg: 'installed',
      values: { PACKAGE: 'chrony' },
    },
  ],
}

let draft = (_: RequestInit, ok: (v: unknown) => Response): Response | Promise<Response> =>
  ok({
    text: '#!/bin/sh\n',
    findings: [],
    questions: ['Which port?'],
    notes: ['Wrote a script that installs the package.'],
    rounds: 1,
  })
let calls: { url: string; method: string; body?: unknown }[] = []
// FU-05: what weighing a run's own values says, and what a start answers.
let weigh: unknown = { needsApproval: false, changed: [], impact: [] }
let started: unknown = null
// The Secrets a secret parameter may name (FU-03): names and keys, never values.
let secretRefs: unknown = {
  secrets: [
    { namespace: 'web', name: 'db', keys: ['password', 'user'] },
    { namespace: 'ops', name: 'ntp', keys: ['key'] },
  ],
}
beforeEach(() => {
  calls = []
  Element.prototype.scrollTo = () => {}
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = init.method ?? 'GET'
      calls.push({
        url,
        method,
        body: init.body ? JSON.parse(init.body as string) : undefined,
      })
      const ok = (v: unknown) =>
        new Response(JSON.stringify(v), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      if (url === '/api/v1/automation') return ok(index)
      if (url === '/api/v1/automation/run-profiles') return ok({ profiles: [] })
      if (url === '/api/v1/automation/sherlock/draft') return draft(init, ok)
      if (url.endsWith('/versions/1/revoke')) return ok(script)
      if (/\/versions\/\d+\/(approve|reject)$/.test(url)) return ok(script)
      if (url === '/api/v1/automation/runs' && method === 'GET') return ok({ runs: [run] })
      if (url === '/api/v1/automation/runs' && method === 'POST') return started ? new Response(JSON.stringify(started), { status: 202, headers: { 'Content-Type': 'application/json' } }) : ok({ ...run, id: 8, state: 'running' })
      if (url === '/api/v1/automation/runs/weigh') return ok(weigh)
      if (url.startsWith('/api/v1/automation/runs/'))
        return ok({
          run,
          output: { w1: 'hello from w1\n{"changed": true}' },
          live: false,
        })
      if (url === '/api/v1/automation/scripts/ensure-chrony') return ok(script)
      if (url === '/api/v1/automation/check') return ok({ header, findings: [] })
      if (url === '/api/v1/automation/fleets') return ok(fleets)
      if (url === '/api/v1/automation/secret-refs') return secretRefs ? ok(secretRefs) : new Response(JSON.stringify({ error: 'Kubernetes said no' }), { status: 502 })
      return new Response(JSON.stringify({ error: 'unexpected ' + url }), {
        status: 500,
      })
    }),
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const props = (path?: string[], query?: string) => ({
  cluster: null,
  now: Date.parse('2026-10-31T10:01:00Z'),
  onToast: vi.fn(),
  route: route(path, query),
  me: admin,
})

describe('AutomationTab', () => {
  it('filters one table by the groups on the left', async () => {
    index.scripts.push({ ...index.scripts[0]!, name: 'loose', group: '' })
    try {
      render(<AutomationTab {...props([], 'group=_ungrouped')} />)
      const pane = await screen.findByTestId('script-groups')
      expect(pane.textContent).toContain('All scripts2')
      expect(pane.textContent).toContain('os1')
      expect(pane.textContent).toContain('Ungrouped1')
      const rows = screen.getAllByTestId('script-row')
      expect(rows.map((r) => r.getAttribute('data-name'))).toEqual(['loose'])
      expect(screen.getAllByRole('table')).toHaveLength(1)
    } finally {
      index.scripts.pop()
    }
  })

  it('lists scripts with their group, state and last run', async () => {
    render(<AutomationTab {...props()} />)
    const row = await screen.findByTestId('script-row')
    expect(row.textContent).toContain('ensure-chrony')
    expect(row.textContent).toContain('os')
    expect(row.textContent).toContain('v2 draft')
    expect(row.textContent).toContain('v1, 1 approval')
    expect(row.textContent).toContain('1 warnings')
  })

  it('runs the version chosen off production, starting from the newest, approved or not', async () => {
    render(<AutomationTab {...props()} />)
    const dialogOpen = (await screen.findByTestId('script-run')) as HTMLButtonElement
    await waitFor(() => expect(dialogOpen.title).toContain('v2, the latest'))
    fireEvent.click(dialogOpen)
    const version = (await screen.findByTestId('run-version')) as HTMLSelectElement
    await waitFor(() => expect(version.value).toBe('2'))
    expect([...version.options].map((o) => o.textContent)).toEqual(['v2 (draft)', 'v1 (approved)'])
    expect((await screen.findByTestId('run-unapproved')).textContent).toContain('v2 is draft')
    fireEvent.change(version, { target: { value: '1' } })
    await waitFor(() => expect(screen.queryByTestId('run-unapproved')).toBeNull())
    fireEvent.change(version, { target: { value: '2' } })
    await screen.findByTestId('run-unapproved')
    fireEvent.click(screen.getByTestId('run-rollout-size'))
    fireEvent.click(screen.getByTestId('run-start'))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')!.body).toMatchObject({
      version: 2,
      dryRun: false,
      rollout: { mode: 'size' },
    })
  })

  it('starts a dry run from the newest text', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-dry-run'))
    await waitFor(() => expect((screen.getByTestId('run-version') as HTMLSelectElement).value).toBe('2'))
  })

  it('offers a production run the approved version alone, a new draft or not', async () => {
    fleets.production = true
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const version = (await screen.findByTestId('run-version')) as HTMLSelectElement
      await waitFor(() => expect(version.value).toBe('1'))
      expect(version.options).toHaveLength(1)
    } finally {
      fleets.production = false
    }
  })

  it('keeps the numbers beside the rollout, and they follow the choice', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-run'))
    const inputs = await screen.findByTestId('run-rollout-inputs')
    expect(inputs.textContent).toContain('Then batches of')
    fireEvent.click(screen.getByTestId('run-rollout-size'))
    expect(inputs.textContent).toContain('Batch size')
    expect(inputs.textContent).not.toContain('Then batches of')
    fireEvent.click(screen.getByTestId('run-rollout-all'))
    expect(within(inputs).queryByTestId('run-size')).toBeNull()
    expect(within(inputs).getByTestId('run-stop-after')).toBeTruthy()
    expect(within(inputs).getByTestId('run-timeout')).toBeTruthy()
  })

  it('compares the version for approval with every other, and goes back with no diff', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-open'))
    const drawer = await screen.findByTestId('script-drawer')
    fireEvent.click(await within(drawer).findByRole('button', { name: /Versions/ }))
    expect((await screen.findByTestId('version-text')).textContent).toContain('#v2')
    const rows = screen.getAllByTestId('version-row')
    expect(rows.map((r) => r.getAttribute('data-n'))).toEqual(['2', '1'])
    // The latest has no diff of its own, however the page is showing.
    const icons = () => [within(rows[0]!).queryByTestId('version-diff'), within(rows[1]!).queryByTestId('version-diff')]
    expect(icons()[0]).toBeNull()
    expect(icons()[1]).not.toBeNull()
    fireEvent.click(within(rows[1]!).getByTestId('version-no-diff'))
    expect(screen.getByTestId('version-text').textContent).toContain('#v1')
    expect(icons()[0]).toBeNull()
    expect(icons()[1]).not.toBeNull()
    expect(within(rows[0]!).getByTestId('version-no-diff')).toBeTruthy()
    // A diff always reads against the latest, whatever was on show.
    fireEvent.click(within(rows[1]!).getByRole('button', { name: 'Diff v2 against v1' }))
    const diff = await screen.findByTestId('version-diff-text')
    expect(diff.textContent).toContain('-#v1')
    expect(diff.textContent).toContain('+#v2')
    expect(screen.getByTestId('version-diff-summary').textContent).toContain('v1 → v2: 1 added, 1 removed')
    expect((screen.getByTestId('version-against') as HTMLSelectElement).value).toBe('1')
    fireEvent.click(within(rows[0]!).getByTestId('version-no-diff'))
    expect(screen.getByTestId('version-text').textContent).toContain('#v2')
    fireEvent.change(screen.getByTestId('version-against'), {
      target: { value: '1' },
    })
    fireEvent.click(screen.getByTestId('version-no-diff-pane'))
    expect(screen.getByTestId('version-text').textContent).toContain('#v2')
    fireEvent.click(within(rows[1]!).getByTestId('version-show'))
    expect(screen.getByTestId('version-text').textContent).toContain('#v1')
    expect(within(rows[0]!).queryByTestId('version-diff')).toBeNull()
  })

  it('lets any SuperAdmin revoke an approved version, after asking', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-open'))
    const drawer = await screen.findByTestId('script-drawer')
    fireEvent.click(await within(drawer).findByRole('button', { name: /Versions/ }))
    const rows = await screen.findAllByTestId('version-row')
    expect(within(rows[0]!).queryByTestId('version-revoke')).toBeNull()
    fireEvent.click(within(rows[1]!).getByTestId('version-revoke'))
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke approval' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/versions/1/revoke'))).toBe(true))
  })

  it('asks before approving, and a rejection needs a reason', async () => {
    const v3 = {
      ...script.versions[1]!,
      n: 3,
      text: '#v3',
      hash: 'h3',
      findings: [],
      state: 'review' as const,
      author: { name: 'Ann', user: 'ann' },
      submitted: { by: { name: 'Ann', user: 'ann' }, at: '2026-10-31T11:00:00Z' },
      may: { approve: true, reject: true, involved: false, runProduction: false, runElsewhere: true, dry: true },
    }
    script.versions.push(v3)
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-open'))
      const drawer = await screen.findByTestId('script-drawer')
      fireEvent.click(await within(drawer).findByRole('button', { name: /Versions/ }))
      const rows = await screen.findAllByTestId('version-row')
      fireEvent.click(within(rows[0]!).getByTestId('version-approve'))
      const ask = await screen.findByRole('alertdialog', {
        name: 'Approve ensure-chrony v3?',
      })
      expect(calls.some((c) => c.url.endsWith('/approve'))).toBe(false)
      fireEvent.click(within(ask).getByRole('button', { name: 'Cancel' }))
      expect(calls.some((c) => c.url.endsWith('/approve'))).toBe(false)

      fireEvent.click(within(rows[0]!).getByTestId('version-reject'))
      const rej = await screen.findByRole('alertdialog', {
        name: 'Reject ensure-chrony v3?',
      })
      const go = within(rej).getByTestId('confirm-ok')
      expect(go).toHaveProperty('disabled', true)
      fireEvent.change(within(rej).getByTestId('decide-comment'), {
        target: { value: '  pin the version  ' },
      })
      expect(go).toHaveProperty('disabled', false)
      fireEvent.click(go)
      await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/versions/3/reject'))?.body).toEqual({ comment: 'pin the version' }))
    } finally {
      script.versions.pop()
    }
  })

  it('starts a new script from the template that reserves resources', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-new'))
    fireEvent.change(await screen.findByTestId('script-template'), {
      target: { value: 'reserve' },
    })
    expect((screen.getByTestId('script-name') as HTMLInputElement).value).toBe('reserve-node-resources')
    expect((screen.getByTestId('script-text') as HTMLTextAreaElement).value).toContain('SYSTEM_RESERVED_MEMORY')
  })

  it("keeps a run's parameters apart from its results", async () => {
    render(<AutomationTab {...props(['runs'])} />)
    fireEvent.click(await screen.findByTestId('run-row'))
    const params = await screen.findByTestId('run-parameters')
    expect(params.textContent).toContain('PACKAGE')
    expect(params.textContent).toContain('chrony')
    const results = screen.getByTestId('run-results')
    expect(results.textContent).toBe('Results')
    expect(params.contains(results)).toBe(false)
    const nodes = await screen.findByTestId('run-nodes-table')
    expect(params.contains(nodes)).toBe(false)
    expect(params.compareDocumentPosition(results) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(results.compareDocumentPosition(nodes) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('says the tab is a SuperAdmin’s to anyone else', async () => {
    render(<AutomationTab {...props()} me={{ ...admin, admin: false }} />)
    expect((await screen.findByTestId('automation-denied')).textContent).toContain('SuperAdmin')
  })

  it('explains what Save needs, and offers it for an older version to approve', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-new'))
    const save = (await screen.findByTestId('script-save')) as HTMLButtonElement
    expect(save.disabled).toBe(false)
    fireEvent.click(save)
    expect((await screen.findByTestId('script-save-why')).textContent).toContain('name')
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/scripts')).toBe(false)
  })

  it('locks the script while Sherlock writes, and Cancel lets go of it', async () => {
    const kept = draft
    draft = (init) =>
      new Promise((_, reject) => {
        init.signal!.addEventListener('abort', () => reject(new Error('aborted')))
      })
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-new'))
      fireEvent.change(await screen.findByTestId('script-sherlock-request'), {
        target: { value: 'do it' },
      })
      fireEvent.click(screen.getByTestId('script-sherlock-draft'))
      expect(await screen.findByTestId('script-sherlock-working')).toBeTruthy()
      expect((screen.getByTestId('script-text') as HTMLTextAreaElement).readOnly).toBe(true)
      expect((screen.getByTestId('script-save') as HTMLButtonElement).disabled).toBe(true)
      expect((screen.getByTestId('script-name') as HTMLInputElement).disabled).toBe(true)
      fireEvent.click(screen.getByTestId('script-sherlock-cancel'))
      await waitFor(() => expect(screen.queryByTestId('script-sherlock-working')).toBeNull())
      expect((screen.getByTestId('script-text') as HTMLTextAreaElement).readOnly).toBe(false)
      expect(screen.queryByTestId('script-sherlock-error')).toBeNull()
    } finally {
      draft = kept
    }
  })

  it('shows what Sherlock asks, and a finding goes to its line', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-new'))
    fireEvent.change(await screen.findByTestId('script-sherlock-request'), {
      target: { value: 'do it' },
    })
    fireEvent.click(screen.getByTestId('script-sherlock-draft'))
    expect((await screen.findByTestId('script-sherlock-questions')).textContent).toContain('Which port?')
    // What it said stands beside the code it changed.
    const said = screen.getByTestId('script-sherlock-notes').textContent
    expect(said).toContain('Wrote a script that installs the package.')
    expect(said).toMatch(/\d+ lines added, \d+ removed/)
  })

  it('says nothing about saving once a save went through', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-open'))
    const text = (await screen.findByTestId('script-text')) as HTMLTextAreaElement
    await waitFor(() => expect(text.value).toContain('#v2'))
    fireEvent.click(screen.getByTestId('script-save'))
    expect((await screen.findByTestId('script-save-why')).textContent).toContain('Nothing to save')
    fireEvent.change(text, { target: { value: '#v3' } })
    expect(screen.queryByTestId('script-save-why')).toBeNull()
  })

  it('numbers the lines of the script', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-new'))
    const text = (await screen.findByTestId('script-text')) as HTMLTextAreaElement
    fireEvent.change(text, { target: { value: 'one\ntwo\nthree' } })
    expect(screen.getByTestId('script-text-lines').textContent).toBe('1\n2\n3')
  })

  it('offers one rollout per line, each saying what it does', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-run'))
    const radios = within(await screen.findByTestId('run-rollout')).getAllByRole('radio') as HTMLInputElement[]
    expect(radios.map((r) => r.checked)).toEqual([true, false, false])
    fireEvent.click(radios[2]!)
    expect(radios.map((r) => r.checked)).toEqual([false, false, true])
    expect(screen.getByTestId('run-rollout').textContent).toContain('One node runs alone first')
  })

  it('offers only approved versions on production', async () => {
    fleets.production = true
    index.scripts[0]!.approved = 0
    try {
      render(<AutomationTab {...props()} />)
      const run = (await screen.findByTestId('script-run')) as HTMLButtonElement
      await waitFor(() => expect(run.title).toContain('No version is approved'))
      expect(run.disabled).toBe(true)
    } finally {
      fleets.production = false
      index.scripts[0]!.approved = 1
    }
  })

  it('starts a run of the approved version with the fleet’s paired nodes', async () => {
    fleets.production = true
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-run'))
    const dialog = await screen.findByTestId('run-dialog')
    await waitFor(() => expect(within(dialog).getByTestId('run-nodes').textContent).toContain('w2'))
    // FU-06: the parameter starts bound to the variable of its name, which each node inherits anyway.
    expect(within(dialog).getByTestId('run-param-PACKAGE-binding').dataset.auto).toBe('true')
    const start = within(dialog).getByTestId('run-start') as HTMLButtonElement
    expect(start.disabled).toBe(false)
    fireEvent.click(start)
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')!
    expect(post.body).toMatchObject({
      script: 'ensure-chrony',
      dryRun: false,
      fleet: 'workers',
      rollout: { mode: 'canary', stopAfter: 1, timeoutMinutes: 10 },
    })
    expect((post.body as { nodes?: string[]; version?: number }).nodes).toEqual(['w1'])
    expect((post.body as { version?: number }).version).toBe(1)
    // The automatic binding is not the run's own value: nothing is sent, and on production nothing is weighed.
    expect((post.body as { params?: unknown }).params).toBeUndefined()
    expect(calls.some((c) => c.url === '/api/v1/automation/runs/weigh')).toBe(false)
    expect(await screen.findByTestId('run-drawer')).toBeTruthy()
    fleets.production = false
  })

  it('selects a fleet’s nodes alone, and a change by hand leaves the fleet', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-run'))
    const dialog = await screen.findByTestId('run-dialog')
    const boxes = () => within(within(dialog).getByTestId('run-nodes')).getAllByRole('checkbox') as HTMLInputElement[]
    await waitFor(() => expect(boxes()).toHaveLength(2))
    const picked = () => boxes().map((b) => b.checked)
    const fleet = within(dialog).getByTestId('run-fleet') as HTMLSelectElement
    expect(within(dialog).getByTestId('run-nodes').textContent).not.toContain('w3')
    expect(picked()).toEqual([true, false])
    expect(within(dialog).getByTestId('run-node-count').textContent).toBe('1 of 2 selected')
    expect(fleet.value).toBe('workers')
    expect(within(dialog).getByTestId('run-left-out').textContent).toContain('1 of this fleet')
    fireEvent.change(fleet, { target: { value: '_all' } })
    expect(picked()).toEqual([true, false, true])
    fireEvent.click(boxes()[2]!)
    fireEvent.click(boxes()[0]!)
    fireEvent.click(boxes()[2]!)
    expect(picked()).toEqual([false, false, true])
    expect(fleet.value).toBe('custom')
    expect(within(dialog).getByTestId('run-custom').textContent).toContain('not configured for a fleet')
    fireEvent.click(within(dialog).getByTestId('run-start'))
    // A real run asks first, and starts nothing until it is confirmed.
    const ask = await screen.findByTestId('confirm')
    expect(ask.textContent).toContain('not a dry run')
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')!.body).toMatchObject({
      fleet: '_all',
      nodes: ['w3'],
    })
  })

  it('has no dry run for a script that does not declare one', async () => {
    index.scripts[0]!.dryRun = false
    try {
      render(<AutomationTab {...props()} />)
      const dry = (await screen.findByTestId('script-dry-run')) as HTMLButtonElement
      expect(dry.disabled).toBe(true)
      expect(dry.title).toContain('does not declare dry-run')
    } finally {
      index.scripts[0]!.dryRun = true
    }
  })

  // --- FU-03, FU-04: parameters typed with suggestions, by their kind ---
  it('says each parameter’s kind, binds it to the variable of its name, and offers the variables from the keyboard (FU-06)', async () => {
    render(<AutomationTab {...props()} />)
    fireEvent.click(await screen.findByTestId('script-run'))
    const dialog = await screen.findByTestId('run-dialog')
    expect(within(dialog).getByTestId('run-param-type').textContent).toBe('Variable · required')
    // Every node of the run inherits PACKAGE: the field holds its chip, with its one value, not an empty box.
    const auto = await within(dialog).findByTestId('run-param-PACKAGE-binding')
    expect(auto.dataset.auto).toBe('true')
    expect(auto.getAttribute('role')).toBe('group')
    expect(auto.getAttribute('aria-describedby')).toContain('run-param-type-PACKAGE')
    expect(auto.textContent).toBe('variable PACKAGE · chrony✕')
    // The chip's text is what the field's description reads; its tooltip gives each node's value and where it is set.
    const textId = auto.getAttribute('aria-describedby')!.split(' ')[0]!
    expect(document.getElementById(textId)!.textContent).toBe('variable PACKAGE · chrony')
    expect(auto.querySelector('.rk-pill')!.getAttribute('title')).toBe('w1: chrony\nSet on fleet Workers, All nodes.')
    expect(within(dialog).getByTestId('run-param-PACKAGE-source').textContent).toBe('from Workers')
    const clear = within(auto).getByTestId('run-param-PACKAGE-unbind')
    expect(clear.getAttribute('aria-label')).toBe('Clear variable PACKAGE from PACKAGE, to type a constant')
    // Cleared, the box is empty, takes the focus and offers every variable, by name, with its value on the run's nodes and where it is set.
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/automation/runs' && c.method === 'GET')).toBe(true))
    fireEvent.click(clear)
    const input = within(dialog).getByTestId('run-param-PACKAGE') as HTMLInputElement
    expect(input.value).toBe('')
    expect(document.activeElement).toBe(input)
    expect(input.placeholder).toBe('a constant for every node, or a variable')
    // FU-07: the empty box says it sends the empty string, as a chip the field's description reads too.
    const empty = within(dialog).getByTestId('run-param-PACKAGE-empty')
    expect(empty.getAttribute('data-empty')).toBe('empty')
    expect(input.getAttribute('aria-describedby')).toBe(`run-param-type-PACKAGE ${empty.parentElement!.id}`)
    expect(input.getAttribute('role')).toBe('combobox')
    expect(within(dialog).queryByTestId('run-param-PACKAGE-badge')).toBeNull()
    fireEvent.focus(input)
    const options = () => within(dialog).queryAllByTestId('run-param-PACKAGE-option')
    expect(options().map((o) => o.textContent)).toEqual(['PACKAGEchronyfleet Workers, All nodes'])
    expect(input.getAttribute('aria-expanded')).toBe('true')
    // Typing searches values too; the arrows move and Enter takes one, which binds.
    fireEvent.change(input, { target: { value: 'CHR' } })
    expect(options()).toHaveLength(1)
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(input.getAttribute('aria-activedescendant')).toBe(options()[0]!.id)
    expect(options()[0]!.getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(input, { key: 'Enter' })
    const chip = within(dialog).getByTestId('run-param-PACKAGE-binding')
    expect(chip.getAttribute('data-variable')).toBe('PACKAGE')
    expect(within(chip).getByTestId('run-param-PACKAGE-spread').textContent).toBe('chrony')
    // Typed, a value is a constant: a badge says so inside the field, and the field's description reads it.
    fireEvent.click(within(chip).getByTestId('run-param-PACKAGE-unbind'))
    const again = within(dialog).getByTestId('run-param-PACKAGE') as HTMLInputElement
    fireEvent.change(again, { target: { value: 'zzz' } })
    const badge = within(dialog).getByTestId('run-param-PACKAGE-badge')
    expect(badge.textContent).toBe('constant')
    expect(again.getAttribute('aria-describedby')).toBe(`run-param-type-PACKAGE ${badge.id}`)
    expect(within(dialog).queryAllByTestId('run-param-PACKAGE-option')).toHaveLength(0)
    expect(within(dialog).getByTestId('run-param-PACKAGE-source').textContent).toBe('this run’s, on every node')
    // Escape closes the list and leaves the dialog open.
    fireEvent.change(again, { target: { value: '' } })
    expect(within(dialog).queryAllByTestId('run-param-PACKAGE-option')).toHaveLength(1)
    fireEvent.keyDown(again, { key: 'Escape' })
    expect(within(dialog).queryAllByTestId('run-param-PACKAGE-option')).toHaveLength(0)
    expect(screen.getByTestId('run-dialog')).toBeTruthy()
  })

  it('stays bound where nodes differ, says how many values, and keeps what was typed when the nodes change (FU-04, FU-06)', async () => {
    const all = fleets.fleets[1]!
    all.params = { ...all.params, nodes: { w3: { PACKAGE: 'ntp' } } }
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const dialog = await screen.findByTestId('run-dialog')
      const spread = () => within(dialog).getByTestId('run-param-PACKAGE-spread').textContent
      await waitFor(() => expect(spread()).toBe('chrony'))
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: '_all' } })
      // Over All nodes the chip follows the nodes: two values, each node's in its tooltip.
      const chip = within(dialog).getByTestId('run-param-PACKAGE-binding')
      expect(chip.dataset.auto).toBe('true')
      expect(chip.textContent).toBe('variable PACKAGE · 2 values✕')
      expect(chip.querySelector('.rk-pill')!.getAttribute('title')).toBe('w1: chrony\nw3: ntp\nSet on fleet Workers, All nodes, node w3.')
      expect(within(dialog).getByTestId('run-param-varies').textContent).toBe('varies by node (2 values)')
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: 'workers' } })
      expect(spread()).toBe('chrony')
      // What was typed stays when the nodes change.
      fireEvent.click(within(dialog).getByTestId('run-param-PACKAGE-unbind'))
      const box = () => within(dialog).getByTestId('run-param-PACKAGE') as HTMLInputElement
      fireEvent.change(box(), { target: { value: 'var:typed' } })
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: '_all' } })
      expect(box().value).toBe('var:typed')
      expect(within(dialog).getByTestId('run-param-PACKAGE-badge').textContent).toBe('constant')
      // A constant that starts like a binding is sent escaped, so it never binds.
      fireEvent.click(within(dialog).getByTestId('run-start'))
      fireEvent.click(await screen.findByTestId('confirm-ok'))
      await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')).toBe(true))
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')!.body).toMatchObject({ params: { PACKAGE: '\\var:typed' } })
    } finally {
      all.params = { values: { PACKAGE: 'chrony' }, ceiling: 'full' }
    }
  })

  it('binds automatically where only some nodes have the variable, names the others, and sends nothing (FU-06)', async () => {
    const all = fleets.fleets[1]!
    all.params = { values: {}, ceiling: 'full' }
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const dialog = await screen.findByTestId('run-dialog')
      await within(dialog).findByTestId('run-param-PACKAGE-binding')
      expect(within(dialog).queryByTestId('run-param-PACKAGE-unset')).toBeNull()
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: '_all' } })
      // w3 is in no fleet that sets PACKAGE: the chip stays, and the same warning as for a binding picked names it.
      expect(within(dialog).getByTestId('run-param-PACKAGE-binding').dataset.auto).toBe('true')
      expect(within(dialog).getByTestId('run-param-PACKAGE-unset').textContent).toBe('Not set on w3: they have no value for this required parameter.')
      expect(within(dialog).getByTestId('run-blocked').textContent).toContain('w3: PACKAGE')
      // Off w3, it starts with nothing of its own.
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: 'workers' } })
      expect(within(dialog).queryByTestId('run-param-PACKAGE-unset')).toBeNull()
      fireEvent.click(within(dialog).getByTestId('run-start'))
      fireEvent.click(await screen.findByTestId('confirm-ok'))
      await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')).toBe(true))
      expect((calls.find((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')!.body as { params?: unknown }).params).toBeUndefined()
    } finally {
      all.params = { values: { PACKAGE: 'chrony' }, ceiling: 'full' }
    }
  })

  it('binds to another variable, warns of the nodes without it, and hides a sensitive one’s values (FU-04)', async () => {
    const workers = fleets.fleets[0]!
    workers.params = { ...workers.params, values: { PACKAGE: 'chrony', ZONE: 'eu-1', DB_PASS: 'hunter2' } }
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const dialog = await screen.findByTestId('run-dialog')
      fireEvent.click(await within(dialog).findByTestId('run-param-PACKAGE-unbind'))
      const input = within(dialog).getByTestId('run-param-PACKAGE') as HTMLInputElement
      fireEvent.focus(input)
      fireEvent.change(input, { target: { value: 'pass' } })
      const options = () => within(dialog).queryAllByTestId('run-param-PACKAGE-option')
      expect(options().map((o) => o.textContent)).toEqual(['DB_PASSvalue hiddenfleet Workers'])
      // Picked, a sensitive variable's chip hides its value, and its tooltip says only where it is set.
      fireEvent.click(options()[0]!)
      const hidden = within(dialog).getByTestId('run-param-PACKAGE-binding')
      expect(hidden.textContent).toBe('variable DB_PASS · value hidden✕')
      expect(hidden.querySelector('.rk-pill')!.getAttribute('title')).toBe('Set on fleet Workers.')
      fireEvent.click(within(hidden).getByTestId('run-param-PACKAGE-unbind'))
      const box = within(dialog).getByTestId('run-param-PACKAGE') as HTMLInputElement
      fireEvent.change(box, { target: { value: 'hunter' } })
      expect(options()).toHaveLength(0)
      // A variable's exact name typed is a constant until it is picked from the list.
      fireEvent.change(box, { target: { value: 'ZONE' } })
      expect(within(dialog).getByTestId('run-param-PACKAGE-badge').textContent).toBe('constant')
      expect(options()[0]!.dataset.bind).toBe('ZONE')
      fireEvent.keyDown(box, { key: 'ArrowDown' })
      fireEvent.keyDown(box, { key: 'Enter' })
      const zone = within(dialog).getByTestId('run-param-PACKAGE-binding')
      expect(zone.getAttribute('data-variable')).toBe('ZONE')
      expect(zone.dataset.auto).toBe('false')
      expect(zone.textContent).toBe('variable ZONE · eu-1✕')
      // Over All nodes, w3 has no ZONE: a warning names it, and a required parameter stops the run.
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: '_all' } })
      expect(within(dialog).getByTestId('run-param-PACKAGE-unset').textContent).toContain('Not set on w3')
      expect(within(dialog).getByTestId('run-blocked').textContent).toContain('w3: PACKAGE')
      fireEvent.change(within(dialog).getByTestId('run-fleet'), { target: { value: 'workers' } })
      fireEvent.click(within(dialog).getByTestId('run-start'))
      fireEvent.click(await screen.findByTestId('confirm-ok'))
      await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')).toBe(true))
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/automation/runs')!.body).toMatchObject({ params: { PACKAGE: 'var:ZONE' } })
    } finally {
      workers.params = { values: { PACKAGE: 'chrony' }, ceiling: 'full' }
    }
  })

  it('says on production which values wait for approval, and asks for it (FU-05)', async () => {
    fleets.production = true
    weigh = { needsApproval: true, changed: ['PACKAGE'], impact: [{ script: 'ensure-chrony', version: 1, decision: 'approval', why: 'PACKAGE is marked sensitive', by: 'rule' }] }
    started = { waiting: { id: 4, cluster: 'local', script: 'ensure-chrony', version: 1, fleet: 'workers', targets: ['w1'], params: { PACKAGE: 'ntp' }, by: { name: 'Pat' }, at: '', changed: ['PACKAGE'], impact: [], state: 'waiting' } }
    const p = props()
    try {
      render(<AutomationTab {...p} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const dialog = await screen.findByTestId('run-dialog')
      fireEvent.click(await within(dialog).findByTestId('run-param-PACKAGE-unbind'))
      const input = within(dialog).getByTestId('run-param-PACKAGE') as HTMLInputElement
      expect(input.disabled).toBe(false)
      fireEvent.change(input, { target: { value: 'ntp' } })
      const note = await within(dialog).findByTestId('run-weigh', {}, { timeout: 2000 })
      expect(note.textContent).toContain('need another admin’s approval before the run starts: PACKAGE (PACKAGE is marked sensitive)')
      const weighed = calls.find((c) => c.url === '/api/v1/automation/runs/weigh')!
      expect(weighed.body).toMatchObject({ params: { PACKAGE: 'ntp' }, dryRun: false })
      expect(within(dialog).getByTestId('run-start').textContent).toBe('Ask for approval')
      fireEvent.click(within(dialog).getByTestId('run-start'))
      expect((await screen.findByTestId('confirm')).textContent).toContain('need another admin’s approval')
      fireEvent.click(screen.getByTestId('confirm-ok'))
      await waitFor(() => expect(p.onToast).toHaveBeenCalledWith(expect.stringContaining('Run request 4 waits for another admin’s approval of PACKAGE')))
      expect(screen.queryByTestId('run-dialog')).toBeNull()
    } finally {
      fleets.production = false
      weigh = { needsApproval: false, changed: [], impact: [] }
      started = null
    }
  })

  it('offers a secret parameter Secrets by name and key only, and says why when none can be listed', async () => {
    header.params.push({ name: 'TOKEN', required: false, secret: true } as never)
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const dialog = await screen.findByTestId('run-dialog')
      const input = (await within(dialog).findByTestId('run-param-TOKEN')) as HTMLInputElement
      const kinds = within(dialog).getAllByTestId('run-param-type')
      expect(kinds.map((k) => k.dataset.kind)).toEqual(['variable', 'secret'])
      expect(kinds[1]!.textContent).toBe('Secret · optional')
      await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/automation/secret-refs')).toBe(true))
      fireEvent.focus(input)
      const options = () =>
        within(dialog)
          .queryAllByTestId('run-param-TOKEN-option')
          .map((o) => o.querySelector('.rk-mono')!.textContent)
      await waitFor(() => expect(options()).toEqual(['secret:web/db/password', 'secret:web/db/user', 'secret:ops/ntp/key']))
      fireEvent.change(input, { target: { value: 'ntp' } })
      expect(options()).toEqual(['secret:ops/ntp/key'])
      fireEvent.click(within(dialog).getAllByTestId('run-param-TOKEN-option')[0]!)
      expect(input.value).toBe('secret:ops/ntp/key')
      expect(within(dialog).getByTestId('run-param-TOKEN-badge').textContent).toBe('secret ref')
      cleanup()
      // The Secrets cannot be listed: the field still takes a reference typed by hand.
      secretRefs = null
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const again = await screen.findByTestId('run-dialog')
      const field = (await within(again).findByTestId('run-param-TOKEN')) as HTMLInputElement
      fireEvent.focus(field)
      await waitFor(() => expect(within(again).getByTestId('run-param-TOKEN-status').textContent).toBe('The Secrets could not be listed: Kubernetes said no. Type secret:namespace/name/key.'))
      fireEvent.change(field, { target: { value: 'secret:web/other/key' } })
      expect(field.value).toBe('secret:web/other/key')
    } finally {
      header.params.pop()
      secretRefs = {
        secrets: [
          { namespace: 'web', name: 'db', keys: ['password', 'user'] },
          { namespace: 'ops', name: 'ntp', keys: ['key'] },
        ],
      }
    }
  })

  it('filters the scripts by state, pending review among them', async () => {
    render(<AutomationTab {...props([], 'state=review')} />)
    expect(await screen.findByTestId('scripts-none')).toBeTruthy()
    expect(screen.queryByTestId('script-row')).toBeNull()
    cleanup()
    render(<AutomationTab {...props([], 'state=draft')} />)
    expect(await screen.findByTestId('script-row')).toBeTruthy()
  })

  it('refuses a full script on a fleet whose ceiling is none', async () => {
    fleets.fleets[0]!.params.ceiling = 'none'
    try {
      render(<AutomationTab {...props()} />)
      fireEvent.click(await screen.findByTestId('script-run'))
      const blocked = await screen.findByTestId('run-blocked')
      expect(blocked.textContent).toContain('ceiling of fleet')
      expect(blocked.textContent).toContain('nobody: read-only')
      expect((screen.getByTestId('run-start') as HTMLButtonElement).disabled).toBe(true)
    } finally {
      fleets.fleets[0]!.params.ceiling = 'full'
    }
  })

  it('shows a run’s nodes and output', async () => {
    render(<AutomationTab {...props(['runs'])} />)
    fireEvent.click(await screen.findByTestId('run-row'))
    const out = await screen.findByTestId('run-output')
    await waitFor(() => expect(out.textContent).toContain('hello from w1'))
    expect(screen.getByTestId('run-state').textContent).toBe('succeeded')
  })
})

describe('rowProblems', () => {
  it('checks names as the server does', () => {
    expect(rowProblems([{ k: 'GOOD', v: '' }])).toEqual([])
    expect(
      rowProblems([
        { k: 'bad-name', v: '' },
        { k: 'NODE', v: '' },
        { k: 'X', v: '' },
        { k: 'X', v: '' },
      ]),
    ).toEqual(['bad-name: a name is capitals, digits and underscores', 'NODE is set by the run', 'X is there twice'])
  })
})
