import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import { RunDetail } from './RunDetail'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchRun: vi.fn(),
}))

// A run of a source's Wiki sync job (KN-28), as the server keeps it: no
// findings (it is not an audit), its facts, and a report with the batch
// Mycroft's answer to was cut and split (KN-27).
const sync: api.Run = {
  id: 'run-40',
  job: 'wiki-sync-e2e-docs-bucket',
  kind: 'wiki-sync-e2e-docs-bucket',
  label: 'Wiki sync: E2E docs bucket',
  agent: 'mycroft',
  trigger: { kind: 'manual', what: 'Source added by Ann', by: 'Ann' },
  clusters: ['local'],
  model: 'claude-opus-5-5',
  started: '2026-11-10T09:00:00Z',
  ended: '2026-11-10T09:01:30Z',
  status: 'ok',
  report:
    "Mycroft read 2 changed items; 0 were unchanged, 1 skipped (1 not a kind Mycroft reads (.png)). 2 page changes wait for review in [AIOps › Inbox](#/cases/inbox/knowledge/r-20261110-0900-7) (run r-20261110-0900-7).\n\n**Mycroft’s batches**\n\n1. “backup-windows.csv”, “restart-the-wombat-cache.md” — split in two and asked again: its answer was cut at the most Mycroft may write (16000 tokens)\n2. “backup-windows.csv” — answered, 1 page drafted\n3. “restart-the-wombat-cache.md” — answered, 1 page drafted",
  findings: [],
  routed: { opened: 0, seen: 0, known: 0, excepted: 0, reopened: 0, regression: 0, verified: 0, capped: 0, invalid: 0 },
  tokens: { in: 4100, out: 900 },
  costUSD: 0.12,
  noFindings: true,
  facts: [
    { label: 'Source', text: 'E2E docs bucket (S3 bucket)', href: '#/admin/settings/knowledge' },
    { label: 'Read', text: 'Every item' },
    { label: 'Items read', text: '2 changed items' },
    { label: 'Unchanged', text: '0 items' },
    { label: 'Gone', text: '0 items' },
    { label: 'Skipped', text: '1 item: 1 not a kind Mycroft reads (.png)' },
    { label: 'Pages drafted', text: '2 page changes wait for review in AIOps › Inbox', href: '#/cases/inbox/knowledge/r-20261110-0900-7' },
    { label: 'Proposals', text: 'None' },
    { label: 'Mycroft’s batches', text: '3 requests, 1 cut and split in two', tone: 'warn' },
  ],
}

describe('the run drawer for a Wiki sync (KN-28)', () => {
  beforeAll(() => {
    Element.prototype.scrollTo = () => {}
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('says what the sync did, links its drafts, and shows no findings or proposed changes', async () => {
    vi.mocked(api.fetchRun).mockResolvedValue(sync)
    render(<RunDetail id="run-40" />)
    const facts = await screen.findByTestId('run-facts')
    const fact = (label: string) => within(facts).getAllByTestId('run-fact').find((f) => f.dataset.label === label)!
    expect(fact('Source').textContent).toBe('E2E docs bucket (S3 bucket)')
    expect(fact('Items read').textContent).toBe('2 changed items')
    expect(fact('Skipped').textContent).toBe('1 item: 1 not a kind Mycroft reads (.png)')
    expect(within(fact('Pages drafted')).getByRole('link').getAttribute('href')).toBe('#/cases/inbox/knowledge/r-20261110-0900-7')
    expect(fact('Mycroft’s batches').dataset.tone).toBe('warn')
    expect(screen.getByTestId('run-trigger').textContent).toBe('Source added by Ann')
    expect(screen.getByTestId('run-cost').textContent).toBe('$0.12')
    const report = screen.getByTestId('run-report')
    expect(report.textContent).toContain('split in two and asked again: its answer was cut at the most Mycroft may write (16000 tokens)')
    expect(within(report).getByText('AIOps › Inbox').getAttribute('href')).toBe('#/cases/inbox/knowledge/r-20261110-0900-7')
    // Not an audit: no findings and no proposed changes.
    expect(screen.queryByTestId('run-routed')).toBeNull()
    expect(screen.queryByText('Findings')).toBeNull()
    expect(screen.queryByText('Proposed changes')).toBeNull()
  })

  it('keeps the findings sections for a run that makes findings', async () => {
    vi.mocked(api.fetchRun).mockResolvedValue({ ...sync, noFindings: false, facts: undefined, kind: 'nightly-audit', label: 'Nightly audit' })
    render(<RunDetail id="run-40" />)
    expect(await screen.findByTestId('run-routed')).toBeTruthy()
    expect(screen.queryByTestId('run-facts')).toBeNull()
    expect(screen.getByText('Proposed changes')).toBeTruthy()
  })
})
