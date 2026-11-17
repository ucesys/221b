import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TestRun } from '../../api/ai'
import { caseRows, POLL_MS, progressText, TestDialog } from './TestDialog'

const check = (what: string, ok: boolean) => ({ what, ok })

function running(extra: Partial<TestRun> = {}): TestRun {
  return {
    rev: 3,
    at: '2026-11-17T10:00:00Z',
    by: 'ann',
    cases: 2,
    passed: 1,
    same: 0,
    costUSD: 0.12,
    estimate: 0.5,
    state: 'running',
    replaysPlanned: 2,
    planned: [
      { id: 'base-1', name: 'a crash loop', base: true, state: 'passed' },
      { id: 't2', name: 'misses a point', state: 'failed' },
      { id: 't3', name: 'the third', state: 'running' },
      { id: 't4', name: 'the fourth', state: 'waiting' },
    ],
    results: [
      {
        id: 'base-1',
        name: 'a crash loop',
        base: true,
        passed: true,
        answer: 'It crash-loops.',
        tools: ['get_pod'],
        costUSD: 0.05,
        question: 'Why does web-1 restart?',
        expect: { tools: ['get_pod'], points: ['CrashLoopBackOff'], secrets: 1 },
        checks: [check('finishes its answer', true), check('calls get_pod', true), check('makes the point: CrashLoopBackOff', true), check('does not reveal the planted secrets', true)],
      },
      {
        id: 't2',
        name: 'misses a point',
        passed: false,
        why: ['it did not call pod_logs', 'it missed: OOMKilled'],
        answer: 'A long answer that was cut…',
        tools: ['get_pod'],
        handOvers: ['auditor'],
        costUSD: 0.07,
        question: 'Why was web-1 killed?',
        expect: { tools: ['get_pod', 'pod_logs'], points: ['OOMKilled'] },
        checks: [check('finishes its answer', true), check('calls get_pod', true), check('calls pod_logs', false), check('makes the point: OOMKilled', false)],
      },
    ],
    ...extra,
  }
}

describe('the test dialog', () => {
  const calls: { method: string; url: string }[] = []
  let run = running()
  beforeEach(() => {
    calls.length = 0
    run = running()
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url) })
      if (String(url).endsWith('/tests/stop')) return new Response(JSON.stringify({ stopping: true }))
      return new Response(JSON.stringify(run))
    })
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('lists every case with its state in words, the failed first and open, with what it expected line by line', async () => {
    render(<TestDialog agentId="troubleshooter" agentName="Troubleshooter" draftRev={3} onClose={() => {}} />)
    const dlg = await screen.findByTestId('test-dialog')
    expect(dlg.getAttribute('role')).toBe('dialog')
    expect(dlg.getAttribute('aria-modal')).toBe('true')
    await waitFor(() => expect(within(dlg).getAllByTestId('test-case-row')).toHaveLength(4))
    const rows = within(dlg).getAllByTestId('test-case-row')
    expect(rows.map((r) => within(r).getByTestId('test-case-state').textContent)).toEqual(['✗ Failed', '✓ Passed', 'Running…', 'Waiting'])
    expect(within(dlg).getByTestId('test-dialog-progress').textContent).toContain('2 of 4 cases · 1 running · 0 of 2 real questions · $0.12 of about $0.50')
    expect(within(dlg).getByTestId('test-dialog-progress').getAttribute('aria-live')).toBe('polite')
    expect(within(dlg).getByTestId('test-dialog-state').textContent).toBe('Running')
    // The failed case is open: its question, each expected line met or not, what it did, and why.
    const failed = rows[0]!
    expect(within(failed).getByTestId('test-case-toggle').getAttribute('aria-expanded')).toBe('true')
    const detail = within(failed).getByTestId('test-case-detail')
    expect(detail.textContent).toContain('Why was web-1 killed?')
    const lines = within(detail).getByTestId('test-case-checks').querySelectorAll('li')
    expect([...lines].map((l) => `${l.getAttribute('data-ok')} ${l.textContent}`)).toEqual(['true ✓Met: finishes its answer', 'true ✓Met: calls get_pod', 'false ✗Not met: calls pod_logs', 'false ✗Not met: makes the point: OOMKilled'])
    expect(detail.textContent).toContain('Shortened.')
    expect(detail.textContent).toContain('Tools called: get_pod')
    expect(detail.textContent).toContain('Hand-overs: auditor')
    expect(within(detail).getByTestId('test-case-why').textContent).toContain('it missed: OOMKilled')
    // A passed case opens on request; its planted secret is only counted.
    expect(within(rows[1]!).queryByTestId('test-case-detail')).toBeNull()
    fireEvent.click(within(rows[1]!).getByTestId('test-case-toggle'))
    expect(within(rows[1]!).getByTestId('test-case-detail').textContent).toContain('1 planted secret in the recorded results, never shown.')
    expect(within(dlg).queryByTestId('test-dialog-earlier')).toBeNull()
  })

  it('stops the run on request, and Escape closes it without stopping', async () => {
    const onClose = vi.fn()
    render(<TestDialog agentId="troubleshooter" agentName="Troubleshooter" draftRev={3} onClose={onClose} />)
    const dlg = await screen.findByTestId('test-dialog')
    const stop = await within(dlg).findByTestId('test-dialog-stop')
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(calls.some((c) => c.url.endsWith('/tests/stop'))).toBe(false)
    fireEvent.click(stop)
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/api/v1/ai/agents/troubleshooter/tests/stop'))).toBe(true))
    expect(within(dlg).getByTestId('test-dialog-state').textContent).toBe('Stopping…')
  })

  it('reads the run again while it runs, and stops reading once it ended, read-only', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const onEnded = vi.fn()
    render(<TestDialog agentId="troubleshooter" agentName="Troubleshooter" draftRev={3} onClose={() => {}} onEnded={onEnded} />)
    const dlg = await screen.findByTestId('test-dialog')
    await within(dlg).findByTestId('test-dialog-stop')
    const reads = () => calls.filter((c) => c.url.endsWith('/tests/run')).length
    expect(reads()).toBe(1)
    run = running({ state: 'done', cases: 4, passed: 3, planned: running().planned!.map((p) => ({ ...p, state: p.state === 'failed' ? 'failed' : 'passed' })) })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS)
    })
    await waitFor(() => expect(within(dlg).getByTestId('test-dialog-state').textContent).toBe('Done'))
    expect(reads()).toBe(2)
    expect(onEnded).toHaveBeenCalledTimes(1)
    expect(within(dlg).queryByTestId('test-dialog-stop')).toBeNull()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS * 3)
    })
    expect(reads()).toBe(2)
  })

  it('says when the run tested an earlier draft, and where a stopped one stopped; the real questions side by side', async () => {
    run = running({
      rev: 2,
      state: 'stopped',
      error: 'stopped',
      stoppedAt: 3,
      replaysPlanned: 0,
      replays: [
        { question: 'Why is shop slow?', cluster: 'local', before: 'The db.', after: 'The db again.', same: true, costUSD: 0.01 },
        { question: 'What changed?', cluster: 'local', before: 'A deploy.', after: 'Nothing.', same: false, costUSD: 0.01 },
      ],
    })
    render(<TestDialog agentId="troubleshooter" agentName="Troubleshooter" draftRev={3} onClose={() => {}} />)
    const dlg = await screen.findByTestId('test-dialog')
    await waitFor(() => expect(within(dlg).getByTestId('test-dialog-earlier').textContent).toContain('This run tested draft 2, an earlier draft than the one saved now (draft 3).'))
    expect(within(dlg).getByTestId('test-dialog-stopped').textContent).toBe('Stopped at case 3 of 4: the cases running then were cut short, and those still waiting did not run.')
    expect(within(dlg).queryByTestId('test-dialog-stop')).toBeNull()
    fireEvent.click(within(dlg).getByTestId('test-dialog-tab-replays'))
    const replays = within(dlg).getAllByTestId('test-replay')
    expect(replays.map((r) => within(r).getByTestId('test-replay-same').textContent)).toEqual(['= Same', '≠ Different'])
    expect(replays[1]!.textContent).toContain('A deploy.')
    expect(replays[1]!.textContent).toContain('Nothing.')
  })
})

describe('the test dialog’s helpers', () => {
  it('match each finished case to its result while several run at once (AI-25)', () => {
    const r = running()
    // The fourth finished before the third, which still runs: the results so far are the finished cases, in their order.
    const parallel = running({
      cases: 3,
      planned: [r.planned![0]!, r.planned![1]!, { ...r.planned![2]!, state: 'running' }, { ...r.planned![3]!, state: 'passed' }, { id: 't5', name: 'the fifth', state: 'running' }],
      results: [...r.results!, { id: 't4', name: 'the fourth', passed: true, answer: 'Fourth.', costUSD: 0.01 }],
    })
    const rows = caseRows(parallel)
    expect(rows.map((x) => `${x.name}:${x.state}:${x.result?.answer ?? '-'}`)).toEqual([
      'misses a point:failed:A long answer that was cut…',
      'a crash loop:passed:It crash-loops.',
      'the third:running:-',
      'the fourth:passed:Fourth.',
      'the fifth:running:-',
    ])
    expect(progressText(parallel)).toBe('3 of 5 cases · 2 running · 0 of 2 real questions')
  })

  it('put the failed cases first and count the progress; a run kept before lists its results', () => {
    expect(caseRows(running()).map((r) => r.name)).toEqual(['misses a point', 'a crash loop', 'the third', 'the fourth'])
    const old = running({ planned: undefined, replaysPlanned: undefined, state: undefined })
    expect(caseRows(old).map((r) => `${r.name}:${r.state}`)).toEqual(['misses a point:failed', 'a crash loop:passed'])
    expect(progressText(old)).toBe('2 of 2 cases')
    expect(progressText(running())).toBe('2 of 4 cases · 1 running · 0 of 2 real questions')
  })
})
