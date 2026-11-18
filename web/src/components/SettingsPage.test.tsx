import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as issues from '../api/issues'
import * as mailApi from '../api/mail'
import * as settingsApi from '../api/settings'
import type { Settings } from '../api/types'
import { parseHash } from '../lib/router'
import { sectionOf, SettingsPanel } from './SettingsPage'

vi.mock('../api/issues', async (original) => ({ ...(await original<typeof import('../api/issues')>()), fetchIssueSettings: vi.fn(), saveIssueSettings: vi.fn() }))
vi.mock('../api/settings', async (original) => ({ ...(await original<typeof import('../api/settings')>()), saveSettings: vi.fn() }))
vi.mock('../api/setup', async (original) => ({ ...(await original<typeof import('../api/setup')>()), useSetup: () => ({ view: null, error: '', reload: () => undefined, set: () => undefined }) }))
vi.mock('../api/mail', async (original) => ({ ...(await original<typeof import('../api/mail')>()), fetchMail: vi.fn(), saveMail: vi.fn(), testMail: vi.fn() }))

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
const mail: mailApi.MailSettings = { host: 'smtp.example.com', port: 587, security: 'starttls', username: '221b', from: '221b@example.com', passwordSet: true, inSecret: true, state: 'on', on: true, link: '', you: 'sam@corp.example' }

describe('Settings (0.12.1)', () => {
  beforeEach(() => {
    vi.mocked(issues.fetchIssueSettings).mockResolvedValue(issueSettings)
    vi.mocked(mailApi.fetchMail).mockResolvedValue(mail)
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
    expect(await screen.findByTestId('email-host')).toBeTruthy()
    expect(screen.getByTestId('settings-email-section').textContent).toContain('SMTP server')
    expect(screen.getByRole('heading', { level: 2 }).textContent).toBe('General')
    await waitFor(() => expect(window.location.hash).toBe('#/admin/settings'))
  })

  // Settings › Repositories (0.12.1, RP-01): Applications before; its old address leads there.
  it('names Repositories, and the old address of Applications leads there', async () => {
    expect(sectionOf('apps')).toBe('repositories')
    window.history.replaceState(null, '', '#/admin/settings/apps')
    render(<SettingsPanel route={parseHash('#/admin/settings/apps')} settings={settings} refreshedAt={null} />)
    expect(screen.getByRole('heading', { level: 2 }).textContent).toBe('Repositories')
    expect(screen.getAllByTestId('settings-section').map((a) => a.getAttribute('data-section'))).not.toContain('apps')
    await waitFor(() => expect(window.location.hash).toBe('#/admin/settings/repositories'))
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

  it('has one Save for the page, saving only what changed, the email with the rest (CU-16)', async () => {
    vi.mocked(issues.saveIssueSettings).mockImplementation(async (s) => s)
    vi.mocked(mailApi.saveMail).mockImplementation(async (m) => ({ ...mail, ...m, state: 'notTested', on: false }))
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    fireEvent.change(await screen.findByTestId('settings-company'), { target: { value: 'Acme IT' } })
    expect(screen.getAllByTestId('save-bar')).toHaveLength(1)
    expect(screen.queryByTestId('email-save')).toBeNull()
    fireEvent.click(screen.getByTestId('settings-save'))
    await waitFor(() => expect(issues.saveIssueSettings).toHaveBeenCalledTimes(1))
    expect(settingsApi.saveSettings).not.toHaveBeenCalled()
    expect(mailApi.saveMail).not.toHaveBeenCalled()
    // The email alone: its password goes only when one is typed.
    fireEvent.change(await screen.findByTestId('email-host'), { target: { value: 'smtp2.example.com' } })
    expect(screen.getByTestId('email-test').hasAttribute('disabled')).toBe(true)
    expect(screen.getByTestId('email-test').getAttribute('title')).toBe('Save first')
    fireEvent.click(screen.getByTestId('settings-save'))
    await waitFor(() => expect(mailApi.saveMail).toHaveBeenCalledTimes(1))
    expect(vi.mocked(mailApi.saveMail).mock.calls[0]![0]).not.toHaveProperty('password')
    expect(issues.saveIssueSettings).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('Saved. Send a test email to turn email on.')).toBeTruthy()
  })

  it('keeps what saved when a part fails, names the part, and Save again tries only it; Undo reverts all (CU-16)', async () => {
    vi.mocked(settingsApi.saveSettings).mockResolvedValue(settings)
    vi.mocked(mailApi.saveMail)
      .mockRejectedValueOnce(new Error('the server is a host name or an IP address'))
      .mockImplementation(async (m) => ({ ...mail, ...m }))
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    fireEvent.click(screen.getByRole('button', { name: '30 s' }))
    fireEvent.change(await screen.findByTestId('email-host'), { target: { value: 'https://smtp' } })
    fireEvent.change(screen.getByTestId('email-password'), { target: { value: 'hunter2' } })
    fireEvent.click(screen.getByTestId('settings-save'))
    expect((await screen.findByTestId('save-error')).textContent).toBe('Email not saved: the server is a host name or an IP address')
    expect(settingsApi.saveSettings).toHaveBeenCalledTimes(1)
    expect((screen.getByTestId('email-host') as HTMLInputElement).value).toBe('https://smtp')
    fireEvent.change(screen.getByTestId('email-host'), { target: { value: 'smtp3.example.com' } })
    fireEvent.click(screen.getByTestId('settings-save'))
    await waitFor(() => expect(mailApi.saveMail).toHaveBeenCalledTimes(2))
    expect(vi.mocked(mailApi.saveMail).mock.calls[1]![0]).toMatchObject({ host: 'smtp3.example.com', password: 'hunter2' })
    expect(settingsApi.saveSettings).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.queryByTestId('save-error')).toBeNull())
    // Undo puts back every part.
    fireEvent.change(screen.getByTestId('settings-company'), { target: { value: 'Acme IT' } })
    fireEvent.change(screen.getByTestId('email-from'), { target: { value: 'x@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Undo changes' }))
    expect((screen.getByTestId('settings-company') as HTMLInputElement).value).toBe('Example IT')
    expect((screen.getByTestId('email-from') as HTMLInputElement).value).toBe('221b@example.com')
  })

  it('asks Save or discard? on leaving the page with changes (CU-16)', async () => {
    vi.mocked(issues.saveIssueSettings).mockImplementation(async (s) => s)
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    fireEvent.change(await screen.findByTestId('settings-company'), { target: { value: 'Acme IT' } })
    const sla = () => screen.getAllByTestId('settings-section').find((a) => a.getAttribute('data-section') === 'sla')!
    fireEvent.click(sla())
    const confirm = await screen.findByTestId('confirm')
    expect(confirm.textContent).toContain('Save or discard?')
    expect(screen.getByTestId('leave-unsaved').textContent).toContain('Settings › General has changes not saved')
    expect(window.location.hash).toBe('#/admin/settings')
    // Cancel stays; Save saves, then leaves.
    fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('confirm')).toBeNull()
    fireEvent.click(sla())
    fireEvent.click(await screen.findByTestId('leave-save'))
    await waitFor(() => expect(window.location.hash).toBe('#/admin/settings/sla'))
    expect(issues.saveIssueSettings).toHaveBeenCalledTimes(1)
  })

  it('leaves without asking when nothing changed, and Discard leaves the changes behind', async () => {
    render(<SettingsPanel route={parseHash('#/admin/settings')} settings={settings} refreshedAt={null} />)
    const field = await screen.findByTestId('settings-company')
    const to = (id: string) => screen.getAllByTestId('settings-section').find((a) => a.getAttribute('data-section') === id)!
    fireEvent.click(to('jobs'))
    expect(window.location.hash).toBe('#/admin/settings/jobs')
    window.history.replaceState(null, '', '#/admin/settings')
    fireEvent.change(field, { target: { value: 'Acme IT' } })
    fireEvent.click(to('jobs'))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    expect(window.location.hash).toBe('#/admin/settings/jobs')
    expect(issues.saveIssueSettings).not.toHaveBeenCalled()
  })
})
