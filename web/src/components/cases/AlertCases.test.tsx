// Alerts and cases (Phase 12, wave 6): an alert an open case covers shows
// the case in place of New Case (CF-77); a job's acknowledgement links its
// run and the case (CF-79); Settings › Cases says when a critical alert
// opens a case by itself (CF-78). Since CF-82 a job's acknowledgement may
// say the alert was diagnosed as expected, or an exception covers it; since
// CF-84 New Case names the alert by its reason.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/casework'
import type { AlertCovers } from '../../api/casework'
import type { AlertRow } from '../../api/types'
import { forgetSharedForTests } from '../../lib/cases'
import { CasesSettings } from '../CasesSettings'
import { AlertAckTag, AlertCase } from './AlertReports'

vi.mock('../../api/casework', async (original) => {
  const real = await original<typeof import('../../api/casework')>()
  return { ...real, fetchCases: vi.fn(), fetchAlertCovers: vi.fn(), createCase: vi.fn(), fetchCasesSettings: vi.fn(), saveCasesSettings: vi.fn() }
})

const txt = (el: Element) => el.textContent ?? ''

const row = (over: Partial<AlertRow> = {}): AlertRow => ({ key: 'pod:shop/web-1', kind: 'pod', severity: 'failed', name: 'web-1', namespace: 'shop', status: 'CrashLoopBackOff', statusTone: 'err', events: [], ...over }) as AlertRow

const cover = (number: number, title = `Case ${number}`) => ({ id: `C-${number}`, number, title, priority: 0 })

const COVERS: AlertCovers = {
  objects: { 'shop/pod/web-1': [cover(9, 'web-1 is down'), cover(4)] },
  alerts: { 'pod:shop/web-2': [cover(12)], 'pod:shop/web-1': [cover(4)] },
}

function works(may: boolean, covers: AlertCovers = COVERS) {
  vi.mocked(api.fetchCases).mockResolvedValue({ cases: [], clusters: [], mayChange: may, me: 'olivia' })
  vi.mocked(api.fetchAlertCovers).mockResolvedValue(covers)
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  forgetSharedForTests()
})

describe('the case on an alert (CF-77)', () => {
  it('shows the newest open case that covers the alert by its object, with how many more, in place of New Case', async () => {
    works(true)
    render(<AlertCase row={row()} cluster="prod" />)
    const link = await screen.findByTestId('alert-case-link')
    expect(txt(link)).toBe('C-9+1')
    expect(link.getAttribute('href')).toBe('#/cases/inbox/case/9')
    expect(link.getAttribute('data-case')).toBe('C-9')
    expect(link.getAttribute('title')).toContain('web-1 is down')
    expect(link.getAttribute('title')).toContain('1 more open case covers it too')
    expect(screen.queryByTestId('alert-new-case')).toBeNull()
    expect(api.fetchAlertCovers).toHaveBeenCalledWith('prod', false)
  })

  it('shows the case the alert’s own diagnosis run sent its findings to', async () => {
    works(true)
    render(<AlertCase row={row({ key: 'pod:shop/web-2', name: 'web-2' })} cluster="prod" />)
    const link = await screen.findByTestId('alert-case-link')
    expect(txt(link)).toBe('C-12')
  })

  it('offers New Case when no open case covers the alert', async () => {
    works(true)
    vi.mocked(api.createCase).mockResolvedValue({ id: 'C-13', number: 13 } as Awaited<ReturnType<typeof api.createCase>>)
    render(<AlertCase row={row({ key: 'node:n1', kind: 'node', name: 'n1', namespace: undefined })} cluster="prod" />)
    await waitFor(() => expect(api.fetchAlertCovers).toHaveBeenCalled())
    fireEvent.click(await screen.findByTestId('alert-new-case'))
    await waitFor(() => expect(api.createCase).toHaveBeenCalledWith({ fromAlert: expect.objectContaining({ cluster: 'prod', object: 'Node/n1', severity: 'critical' }) }))
    expect(screen.queryByTestId('alert-case-link')).toBeNull()
  })

  it('shows neither New Case nor a case to who does not work the cases, and reads nothing for them', async () => {
    works(false)
    const { container } = render(<AlertCase row={row()} cluster="prod" />)
    await waitFor(() => expect(api.fetchCases).toHaveBeenCalled())
    expect(container.textContent).toBe('')
    expect(api.fetchAlertCovers).not.toHaveBeenCalled()
  })

  it('finds the cases of one alert, newest first, each once', () => {
    expect(api.coversOf(COVERS, 'pod:shop/web-1', 'shop/Pod/web-1').map((c) => c.id)).toEqual(['C-9', 'C-4'])
    expect(api.coversOf(COVERS, 'pod:shop/web-3', 'shop/Pod/web-3')).toEqual([])
    expect(api.coversOf(null, 'pod:shop/web-1', 'shop/Pod/web-1')).toEqual([])
  })
})

describe('a job’s acknowledgement (CF-79)', () => {
  it('says the job and links its run and the case', () => {
    render(<AlertAckTag ack={{ by: 'Alert diagnosis', at: '2026-11-09T10:00:00Z', run: 'run-7', case: 'C-3' }} />)
    const tag = screen.getByTestId('alert-acked')
    expect(txt(tag)).toContain('Acknowledged by Alert diagnosis')
    expect(tag.getAttribute('data-job')).toBe('true')
    expect(screen.getByTestId('alert-ack-run').getAttribute('href')).toBe('#/cases/jobs/runs/run-7')
    expect(screen.getByTestId('alert-ack-case').getAttribute('href')).toBe('#/cases/inbox/case/3')
  })

  it('says a person’s as before, without links', () => {
    render(<AlertAckTag ack={{ by: 'ann@corp.example', at: '2026-11-09T10:00:00Z' }} />)
    expect(txt(screen.getByTestId('alert-acked'))).toContain('Acknowledged by ann@corp.example')
    expect(screen.getByTestId('alert-acked').getAttribute('data-job')).toBeNull()
    expect(screen.queryByTestId('alert-ack-run')).toBeNull()
    expect(screen.queryByTestId('alert-ack-case')).toBeNull()
  })
})

describe('what a job concluded on, and the alert’s reason (CF-82, CF-84)', () => {
  it('says a job diagnosed the alert as expected, with the agent’s reason and its run', () => {
    render(<AlertAckTag ack={{ by: 'Alert diagnosis', at: '2026-11-09T10:00:00Z', run: 'run-17', expected: true, why: 'r620-worker is annotated nodepower/status: asleep.' }} />)
    const tag = screen.getByTestId('alert-acked')
    expect(txt(tag)).toBe('Acknowledged by Alert diagnosis: expected — r620-worker is annotated nodepower/status: asleep., run-17')
    expect(tag.getAttribute('data-variant')).toBe('expected')
    expect(tag.getAttribute('title')).toContain('the system working as intended')
    expect(screen.getByTestId('alert-ack-run').getAttribute('href')).toBe('#/cases/jobs/runs/run-17')
    expect(screen.queryByTestId('alert-ack-case')).toBeNull()
  })

  it('says expected without a reason for what was below the job’s lowest priority', () => {
    render(<AlertAckTag ack={{ by: 'Alert diagnosis', at: '2026-11-09T10:00:00Z', run: 'run-18', expected: true }} />)
    const tag = screen.getByTestId('alert-acked')
    expect(txt(tag)).toBe('Acknowledged by Alert diagnosis: expected, run-18')
    expect(tag.getAttribute('title')).toContain('below the job’s lowest priority')
  })

  it('cuts a long reason, whole in the hover', () => {
    const why = 'x'.repeat(150)
    render(<AlertAckTag ack={{ by: 'Alert diagnosis', at: '2026-11-09T10:00:00Z', run: 'run-18', expected: true, why }} />)
    const tag = screen.getByTestId('alert-acked')
    expect(txt(tag)).toContain('x'.repeat(99) + '…')
    expect(tag.getAttribute('title')).toContain(why)
  })

  it('says an exception covers what the job found, linking it', () => {
    render(<AlertAckTag ack={{ by: 'Alert diagnosis', at: '2026-11-09T10:00:00Z', run: 'run-19', exception: 'EX-3' }} />)
    const tag = screen.getByTestId('alert-acked')
    expect(txt(tag)).toBe('Acknowledged by Alert diagnosis: covered by EX-3, run-19')
    expect(tag.getAttribute('data-variant')).toBe('exception')
    expect(screen.getByTestId('alert-ack-exception').getAttribute('href')).toBe('#/cases/mycroft/exceptions/EX-3')
  })

  it('opens New Case named by the alert’s reason, with its key', async () => {
    works(true, { objects: {}, alerts: {} })
    vi.mocked(api.createCase).mockResolvedValue({ id: 'C-19', number: 19 } as Awaited<ReturnType<typeof api.createCase>>)
    render(<AlertCase row={row({ key: 'pod:kube-system/rke2-canal-n6fkh', name: 'rke2-canal-n6fkh', namespace: 'kube-system', status: 'Unknown', node: 'r620-worker' })} cluster="prod" />)
    fireEvent.click(await screen.findByTestId('alert-new-case'))
    await waitFor(() =>
      expect(api.createCase).toHaveBeenCalledWith({
        fromAlert: expect.objectContaining({ cluster: 'prod', key: 'pod:kube-system/rke2-canal-n6fkh', name: 'Pod status Unknown: node r620-worker stopped reporting', object: 'kube-system/Pod/rke2-canal-n6fkh', severity: 'critical' }),
      }),
    )
  })
})

describe('Settings › Cases: the safety net’s minutes (CF-78)', () => {
  const base = { keepAgentClosedDays: 90, lookBackDays: 90, exceptionStaleDays: 90, exceptionsPerAgent: 50, newCasesPerRun: 50 }

  it('shows 15 when the server says none, takes 0 to turn it off, and refuses past 1440', async () => {
    vi.mocked(api.fetchCasesSettings).mockResolvedValue(base)
    vi.mocked(api.saveCasesSettings).mockImplementation(async (x) => x)
    render(<CasesSettings />)
    const field = (await screen.findByTestId('cases-criticalAlertMinutes')) as HTMLInputElement
    expect(field.value).toBe('15')
    expect(txt(screen.getByTestId('cases-setting-criticalAlertMinutes'))).toContain('Open a case for a critical alert still going after')
    fireEvent.change(field, { target: { value: '1441' } })
    expect(txt(screen.getByTestId('cases-setting-criticalAlertMinutes'))).toContain('From 0 to 1440.')
    fireEvent.change(field, { target: { value: '0' } })
    expect(txt(screen.getByTestId('cases-setting-criticalAlertMinutes'))).toContain('0 turns this off')
    fireEvent.click(screen.getByTestId('cases-save'))
    await waitFor(() => expect(api.saveCasesSettings).toHaveBeenCalledWith({ ...base, criticalAlertMinutes: 0 }))
  })
})
