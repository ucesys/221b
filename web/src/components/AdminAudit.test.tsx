import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchAudit, filterAudit, type AuditEvent, type AuditPage } from '../api/admin'
import { download } from '../lib/xlsx'
import { AdminAudit } from './AdminAudit'
import type { AdminProps } from './AdminPage'

vi.mock('../lib/xlsx', async (original) => ({ ...(await original<typeof import('../lib/xlsx')>()), download: vi.fn() }))
vi.mock('../api/admin', async (original) => ({ ...(await original<typeof import('../api/admin')>()), fetchAudit: vi.fn() }))

const event = (action: string, over: Partial<AuditEvent> = {}): AuditEvent => ({ time: '2026-10-30T10:00:00Z', action, who: { name: 'Ada', email: 'ada@example.com' }, outcome: 'ok', ...over })
const events = [event('settings.update', { detail: 'refresh 4s' }), event('backup.export', { who: { subject: 'bob' }, target: 'Backup-1' }), event('backup.restore', { outcome: 'denied' }), event('session.end', { cluster: 'edge', from: '10.0.0.9' })]
const page: AuditPage = { events, total: events.length, stored: true, dropped: 0, actions: ['settings.update', 'backup.export', 'backup.restore', 'session.end'] }

describe('the audit filter', () => {
  const none = { q: '', action: '', outcome: '' }
  const actions = (f: Partial<typeof none>) => filterAudit(events, { ...none, ...f }).map((e) => e.action)

  it('finds every word, in any field and any case', () => {
    expect(actions({ q: '  ' })).toHaveLength(4)
    expect(actions({ q: 'ADA refresh' })).toEqual(['settings.update'])
    expect(actions({ q: 'backup-1 bob' })).toEqual(['backup.export'])
    expect(actions({ q: '10.0.0.9 edge' })).toEqual(['session.end'])
    expect(actions({ q: 'ada bob' })).toEqual([])
  })

  it('narrows by an action, or the family it heads, and an outcome', () => {
    expect(actions({ action: 'backup' })).toEqual(['backup.export', 'backup.restore'])
    expect(actions({ action: 'backup.export' })).toEqual(['backup.export'])
    expect(actions({ action: 'back' })).toEqual([])
    expect(actions({ action: 'backup', outcome: 'denied' })).toEqual(['backup.restore'])
  })
})

describe('the Audit log page', () => {
  const rows = () => screen.queryAllByRole('row').length - 1
  beforeEach(() => vi.mocked(fetchAudit).mockResolvedValue(page))
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('downloads what it shows from the Export menu every table has', async () => {
    const onToast = vi.fn()
    render(<AdminAudit {...({ onToast } as unknown as AdminProps)} />)
    await waitFor(() => expect(rows()).toBe(4))
    fireEvent.change(screen.getByTestId('audit-search'), { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: 'Export' }))
    fireEvent.click(screen.getByRole('button', { name: /CSV/ }))
    const [text, file, type] = vi.mocked(download).mock.calls[0]!
    expect(file).toMatch(/^221b-audit-log-.*\.csv$/)
    expect(type).toContain('text/csv')
    expect(text).toContain('When,Action,Who,Cluster,Target,Outcome,Detail,From')
    expect(text).toContain('backup.export,bob,,Backup-1,ok')
    expect(text).not.toContain('settings.update')
    expect(onToast).toHaveBeenCalledWith(expect.stringContaining('221b-audit-log-'))
    for (const name of [/Excel/, /JSON/]) {
      fireEvent.click(screen.getByRole('button', { name: 'Export' }))
      fireEvent.click(screen.getByRole('button', { name }))
    }
    expect(vi.mocked(download).mock.calls.map((c) => String(c[1]).split('.').pop())).toEqual(['csv', 'xlsx', 'json'])
  })

  it('reads the log once however the search changes, and again on Refresh', async () => {
    render(<AdminAudit {...({} as AdminProps)} />)
    await waitFor(() => expect(rows()).toBe(4))
    fireEvent.change(screen.getByTestId('audit-search'), { target: { value: 'b' } })
    fireEvent.change(screen.getByTestId('audit-search'), { target: { value: 'bo' } })
    fireEvent.change(screen.getByTestId('audit-search'), { target: { value: 'bob' } })
    fireEvent.change(screen.getByTestId('audit-outcome'), { target: { value: 'ok' } })
    expect(rows()).toBe(1)
    expect(fetchAudit).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByTestId('audit-refresh'))
    await waitFor(() => expect(fetchAudit).toHaveBeenCalledTimes(2))
  })

  it('marks an event that came back from a backup, and only that one', async () => {
    vi.mocked(fetchAudit).mockResolvedValue({ ...page, events: [event('settings.update', { restored: '2026-10-29T08:00:00Z' }), event('backup.export')] })
    render(<AdminAudit {...({} as AdminProps)} />)
    await waitFor(() => expect(rows()).toBe(2))
    const marks = screen.getAllByTestId('audit-restored')
    expect(marks).toHaveLength(1)
    expect(marks[0]!.textContent).toMatch(/^Restored from the backup of /)
  })

  it('says when nothing matches, and shows the newest 500 at most', async () => {
    const many = Array.from({ length: 600 }, (_, i) => event('settings.update', { detail: `change ${i}` }))
    vi.mocked(fetchAudit).mockResolvedValue({ ...page, events: many, total: many.length })
    render(<AdminAudit {...({} as AdminProps)} />)
    await waitFor(() => expect(rows()).toBe(500))
    expect(screen.getByTestId('admin-audit')).toHaveProperty('textContent', expect.stringContaining('500 of 600 kept events shown (the newest 500'))
    fireEvent.change(screen.getByTestId('audit-search'), { target: { value: 'change 599' } })
    expect(rows()).toBe(1)
    fireEvent.change(screen.getByTestId('audit-search'), { target: { value: 'nothing-like-this' } })
    expect(screen.getByTestId('admin-audit').textContent).toContain('No event matches.')
  })
})
