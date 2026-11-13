import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../api/mail'
import { EmailSettings, mailStatus } from './EmailSettings'

vi.mock('../api/mail', async (original) => ({
  ...(await original<typeof import('../api/mail')>()),
  fetchMail: vi.fn(),
  saveMail: vi.fn(),
  testMail: vi.fn(),
}))

const off: api.MailSettings = { host: '', port: 587, security: 'starttls', username: '', from: '', passwordSet: false, inSecret: true, state: 'notConfigured', on: false, link: '', you: 'sam@corp.example' }
const saved: api.MailSettings = { ...off, host: 'smtp.example.com', username: '221b', from: '221b@example.com', passwordSet: true, state: 'notTested', link: 'https://221b.example.com' }

describe('Settings › Email (EM-01 to EM-04)', () => {
  beforeEach(() => {
    vi.mocked(api.fetchMail).mockResolvedValue(off)
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('says each state: not configured, not tested, tested by whom and when, the last error', () => {
    expect(mailStatus(off).text).toContain('Not configured')
    expect(mailStatus(saved)).toMatchObject({ tone: 'warn', text: expect.stringContaining('not tested') })
    const tested = { at: '2026-11-13T10:00:00Z', by: 'Sam', to: 'sam@corp.example', ok: true }
    expect(mailStatus({ ...saved, state: 'on', on: true, tested })).toMatchObject({ tone: 'ok', text: expect.stringContaining('by Sam, to sam@corp.example') })
    expect(mailStatus({ ...saved, state: 'failed', tested: { ...tested, ok: false, error: '535 authentication failed' } })).toMatchObject({ tone: 'err', text: expect.stringContaining('failed: 535 authentication failed') })
  })

  it('saves the server with a password typed once, never shown; then tests it to the admin’s own address by default', async () => {
    vi.mocked(api.saveMail).mockResolvedValue(saved)
    vi.mocked(api.testMail).mockResolvedValue({ ...saved, state: 'on', on: true, tested: { at: '2026-11-13T10:00:00Z', by: 'Sam', to: 'sam@corp.example', ok: true } })
    render(<EmailSettings />)
    await screen.findByText(/Not configured/)
    expect((screen.getByTestId('email-test') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByTestId('email-host'), { target: { value: ' smtp.example.com ' } })
    fireEvent.click(screen.getByRole('button', { name: 'TLS' }))
    expect((screen.getByTestId('email-port') as HTMLInputElement).value).toBe('465')
    fireEvent.click(screen.getByRole('button', { name: 'STARTTLS' }))
    fireEvent.change(screen.getByTestId('email-username'), { target: { value: '221b' } })
    fireEvent.change(screen.getByTestId('email-password'), { target: { value: 'hunter2' } })
    fireEvent.change(screen.getByTestId('email-from'), { target: { value: '221b@example.com' } })
    fireEvent.click(screen.getByTestId('email-save'))
    await waitFor(() => expect(api.saveMail).toHaveBeenCalled())
    expect(vi.mocked(api.saveMail).mock.calls[0]![0]).toEqual({ host: 'smtp.example.com', port: 587, security: 'starttls', username: '221b', from: '221b@example.com', password: 'hunter2' })
    await screen.findByText(/Configured, not tested/)
    const pw = screen.getByTestId('email-password') as HTMLInputElement
    expect(pw.value).toBe('')
    expect(pw.placeholder).toBe('••••••••')
    expect(screen.getByTestId('email-link').textContent).toContain('https://221b.example.com')
    expect((screen.getByTestId('email-test-to') as HTMLInputElement).value).toBe('sam@corp.example')
    fireEvent.click(screen.getByTestId('email-test'))
    await waitFor(() => expect(api.testMail).toHaveBeenCalledWith('sam@corp.example'))
    await screen.findByText(/A test email went to sam@corp.example: email is on/)
    expect(screen.getByTestId('email-status').textContent).toContain('On: tested')
  })

  it('keeps the password unless asked to forget it, and tests only what is saved', async () => {
    vi.mocked(api.fetchMail).mockResolvedValue(saved)
    vi.mocked(api.saveMail).mockResolvedValue({ ...saved, passwordSet: false })
    render(<EmailSettings />)
    await screen.findByText(/Configured, not tested/)
    expect((screen.getByTestId('email-test') as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(screen.getByTestId('email-forget-password'))
    expect((screen.getByTestId('email-test') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByTestId('email-save'))
    await waitFor(() => expect(api.saveMail).toHaveBeenCalled())
    expect(vi.mocked(api.saveMail).mock.calls[0]![0]).toMatchObject({ clearPassword: true })
    expect(vi.mocked(api.saveMail).mock.calls[0]![0]).not.toHaveProperty('password')
  })

  it('says what the server refused', async () => {
    vi.mocked(api.saveMail).mockRejectedValue(new Error('not valid: the server is a host name or an IP address'))
    render(<EmailSettings />)
    await screen.findByText(/Not configured/)
    fireEvent.change(screen.getByTestId('email-host'), { target: { value: 'https://smtp' } })
    fireEvent.click(screen.getByTestId('email-save'))
    await screen.findByText(/the server is a host name/)
  })
})
