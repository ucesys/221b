import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../api/issues'
import { IssueSettings } from './IssueSettings'

vi.mock('../api/issues', async (original) => ({
  ...(await original<typeof import('../api/issues')>()),
  fetchIssueSettings: vi.fn(),
  saveIssueSettings: vi.fn(),
  fetchIssueTokens: vi.fn(),
  createIssueToken: vi.fn(),
  revokeIssueToken: vi.fn(),
}))

const settings: api.IssueSettings = {
  company: 'Example IT',
  statusPage: '',
  reportURL: '',
  matchThreshold: 80,
  targets: { P1: { responseMinutes: 15, resolveMinutes: 240 }, P2: { responseMinutes: 60, resolveMinutes: 1440 }, P3: { responseMinutes: 240, resolveMinutes: 7200 }, P4: { responseMinutes: 1440, resolveMinutes: 0 } },
  retentionDays: 365,
  ratePerHour: 10,
  reopenDays: 7,
  fixedDays: 3,
  needInfoDays: 5,
  duplicateHours: 6,
  bumpP2At: 3,
  bumpP1At: 10,
  webhook: { url: '' },
}

describe('Settings › Issues', () => {
  beforeEach(() => {
    vi.mocked(api.fetchIssueSettings).mockResolvedValue(settings)
    vi.mocked(api.fetchIssueTokens).mockResolvedValue([])
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('saves what was changed; the targets are kept as they are (Settings › SLA sets them)', async () => {
    vi.mocked(api.saveIssueSettings).mockImplementation(async (s) => s)
    render(<IssueSettings open />)
    const threshold = await screen.findByLabelText('Link to a known issue at')
    fireEvent.change(threshold, { target: { value: '70' } })
    fireEvent.click(screen.getByTestId('issue-settings-save'))
    await waitFor(() => expect(api.saveIssueSettings).toHaveBeenCalled())
    const sent = vi.mocked(api.saveIssueSettings).mock.calls[0]![0]
    expect(sent.matchThreshold).toBe(70)
    expect(sent.targets.P1).toEqual({ responseMinutes: 15, resolveMinutes: 240 })
    expect(screen.getByTestId('issue-settings-sla').textContent).toContain('Settings › SLA')
    expect(sent.company).toBe('Example IT')
  })

  it('shows a new token once, and revokes one', async () => {
    vi.mocked(api.createIssueToken).mockResolvedValue({ token: { id: 'ab12', name: 'Alerts', account: 'alertmanager', scopes: ['reports:create'], created: '2026-11-05T10:00:00Z', createdBy: { id: 'sam' } }, secret: '221bis_ab12_secret' })
    render(<IssueSettings open />)
    await screen.findByText('No tokens.')
    fireEvent.change(screen.getByLabelText('Token name'), { target: { value: 'Alerts' } })
    fireEvent.change(screen.getByLabelText('Account'), { target: { value: 'alertmanager' } })
    vi.mocked(api.fetchIssueTokens).mockResolvedValue([{ id: 'ab12', name: 'Alerts', account: 'alertmanager', scopes: ['reports:create'], created: '2026-11-05T10:00:00Z', createdBy: { id: 'sam' } }])
    fireEvent.click(screen.getByRole('button', { name: 'Make token' }))
    await waitFor(() => expect(screen.getByTestId('issue-token-secret').textContent).toContain('221bis_ab12_secret'))
    expect(api.createIssueToken).toHaveBeenCalledWith('Alerts', 'alertmanager', ['reports:create'])
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByTestId('issue-token-secret')).toBeNull()
    vi.mocked(api.revokeIssueToken).mockResolvedValue({ revoked: true })
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(api.revokeIssueToken).toHaveBeenCalledWith('ab12'))
  })
})
