import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as issues from '../api/issues'
import * as settingsApi from '../api/settings'
import type { Settings } from '../api/types'
import { parseHash } from '../lib/router'
import { sectionOf, SettingsPanel } from './SettingsPage'

vi.mock('../api/issues', async (original) => ({ ...(await original<typeof import('../api/issues')>()), fetchIssueSettings: vi.fn(), saveIssueSettings: vi.fn() }))
vi.mock('../api/settings', async (original) => ({ ...(await original<typeof import('../api/settings')>()), saveSettings: vi.fn() }))
vi.mock('../api/setup', async (original) => ({ ...(await original<typeof import('../api/setup')>()), useSetup: () => ({ view: null, error: '', reload: () => undefined, set: () => undefined }) }))
vi.mock('./EmailSettings', () => ({ EmailSettings: () => <div data-testid="email-settings">SMTP</div> }))

const issueSettings: issues.IssueSettings = {
  company: 'Example IT',
  matchThreshold: 80,
  targets: {},
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
const settings: Settings = { refreshSeconds: 10 } as Settings

describe('Settings (0.12.1)', () => {
  beforeEach(() => {
    vi.mocked(issues.fetchIssueSettings).mockResolvedValue(issueSettings)
    window.history.replaceState(null, '', '#/admin/settings')
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('lists SLA first under AIOps, and no Email page of its own', () => {
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    const ids = screen.getAllByTestId('settings-section').map((a) => a.getAttribute('data-section'))
    expect(ids.indexOf('sla')).toBe(ids.indexOf('issues') - 1)
    expect(ids).not.toContain('email')
  })

  it('shows Email as a section of General, and the old address leads there', async () => {
    expect(sectionOf('email')).toBe('general')
    window.history.replaceState(null, '', '#/admin/settings/email')
    render(<SettingsPanel route={parseHash('#/admin/settings/email')} settings={settings} refreshedAt={null} />)
    expect(screen.getByTestId('settings-email-section').textContent).toContain('SMTP')
    expect(screen.getByRole('heading', { level: 2 }).textContent).toBe('General')
    await waitFor(() => expect(window.location.hash).toBe('#/admin/settings'))
  })

  it('sets the company or IT name in General, kept in the issue settings with their other values', async () => {
    vi.mocked(settingsApi.saveSettings).mockResolvedValue(settings)
    vi.mocked(issues.saveIssueSettings).mockImplementation(async (s) => s)
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    const field = await screen.findByTestId('settings-company')
    expect((field as HTMLInputElement).value).toBe('Example IT')
    fireEvent.change(field, { target: { value: ' Acme IT ' } })
    fireEvent.click(screen.getByTestId('settings-save'))
    await waitFor(() => expect(issues.saveIssueSettings).toHaveBeenCalledWith({ ...issueSettings, company: 'Acme IT' }))
  })

  it('leaves the field out without the issue service', async () => {
    vi.mocked(issues.fetchIssueSettings).mockRejectedValue(new Error('not found'))
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    await waitFor(() => expect(issues.fetchIssueSettings).toHaveBeenCalled())
    expect(screen.queryByTestId('settings-company')).toBeNull()
  })
})
