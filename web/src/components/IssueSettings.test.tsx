import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../api/issues'
import { IssueSettings } from './IssueSettings'

vi.mock('../api/issues', async (original) => ({
  ...(await original<typeof import('../api/issues')>()),
  fetchIssueSettings: vi.fn(),
  saveIssueSettings: vi.fn(),
}))

const settings: api.IssueSettings = {
  company: 'Example IT',
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

describe('Administration › AI › Issues', () => {
  beforeEach(() => {
    vi.mocked(api.fetchIssueSettings).mockResolvedValue(settings)
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('saves what was changed; the targets are kept as they are (Administration › AI › SLA sets them)', async () => {
    vi.mocked(api.saveIssueSettings).mockImplementation(async (s) => s)
    render(<IssueSettings open />)
    const threshold = await screen.findByLabelText('Link to a known issue at')
    fireEvent.change(threshold, { target: { value: '70' } })
    fireEvent.click(screen.getByTestId('issue-settings-save'))
    await waitFor(() => expect(api.saveIssueSettings).toHaveBeenCalled())
    const sent = vi.mocked(api.saveIssueSettings).mock.calls[0]![0]
    expect(sent.matchThreshold).toBe(70)
    expect(sent.targets.P1).toEqual({ responseMinutes: 15, resolveMinutes: 240 })
    expect(screen.getByTestId('issue-settings-sla').textContent).toContain('Administration › AI › SLA')
    expect(sent.company).toBe('Example IT')
  })

  it('saves only its own fields over the settings as they are now: the company and targets changed elsewhere meanwhile stay', async () => {
    vi.mocked(api.saveIssueSettings).mockImplementation(async (s) => s)
    render(<IssueSettings open />)
    const threshold = await screen.findByLabelText('Link to a known issue at')
    // General and SLA save after this page loaded.
    vi.mocked(api.fetchIssueSettings).mockResolvedValue({ ...settings, company: 'New IT', targets: { P1: { responseMinutes: 5, resolveMinutes: 60 } } })
    fireEvent.change(threshold, { target: { value: '70' } })
    fireEvent.click(screen.getByTestId('issue-settings-save'))
    await waitFor(() => expect(api.saveIssueSettings).toHaveBeenCalled())
    const sent = vi.mocked(api.saveIssueSettings).mock.calls[0]![0]
    expect(sent.matchThreshold).toBe(70)
    expect(sent.company).toBe('New IT')
    expect(sent.targets).toEqual({ P1: { responseMinutes: 5, resolveMinutes: 60 } })
  })

  it('has no company, status page, report page address, API tokens or Confluence any more (0.12.1)', async () => {
    render(<IssueSettings open />)
    await screen.findByLabelText('Link to a known issue at')
    for (const gone of ['Company or IT name', 'All known problems', 'Report page address', 'Token name']) expect(screen.queryByLabelText(gone)).toBeNull()
    expect(screen.queryByTestId('issue-tokens')).toBeNull()
    expect(screen.queryByTestId('confluence-settings')).toBeNull()
    expect(screen.getByTestId('issue-settings').textContent).toContain('issues.reportURL')
  })
})
