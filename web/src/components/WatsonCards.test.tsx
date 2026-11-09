import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from '../api/cards'
import { NotificationChannels } from './NotificationChannels'
import { StandingApprovals } from './StandingApprovals'

vi.mock('../api/cards', async (original) => ({
  ...(await original<typeof import('../api/cards')>()),
  fetchProposals: vi.fn(),
  fetchProposal: vi.fn(),
  fetchStanding: vi.fn(),
  saveStanding: vi.fn(),
  deleteStanding: vi.fn(),
  fetchNotifications: vi.fn(),
  saveNotifications: vi.fn(),
  testChannel: vi.fn(),
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('Settings › Watson: standing approvals', () => {
  it('adds one with its class, clusters and rate', async () => {
    vi.mocked(api.fetchStanding).mockResolvedValue([])
    vi.mocked(api.saveStanding).mockImplementation(async (st) => ({ ...st, id: 's1' }))
    render(<StandingApprovals />)
    fireEvent.click(await screen.findByTestId('standing-add'))
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Restart crash-looping pods in development' } })
    fireEvent.change(screen.getByLabelText('Clusters'), { target: { value: 'dev-1, staging' } })
    fireEvent.change(screen.getByLabelText('Actions'), { target: { value: 'kubectl rollout restart, kubectl delete pod' } })
    fireEvent.change(screen.getByLabelText('Condition'), { target: { value: 'alert-firing' } })
    fireEvent.change(screen.getByLabelText('At most, an hour'), { target: { value: '3' } })
    fireEvent.click(screen.getByTestId('standing-save'))
    await waitFor(() => expect(api.saveStanding).toHaveBeenCalled())
    expect(vi.mocked(api.saveStanding).mock.calls[0]![0]).toMatchObject({ name: 'Restart crash-looping pods in development', clusters: ['dev-1', 'staging'], actions: ['kubectl rollout restart', 'kubectl delete pod'], condition: 'alert-firing', perHour: 3, on: true })
    await waitFor(() => expect(api.fetchStanding).toHaveBeenCalledTimes(2))
  })

  it('says why one is refused, such as on production', async () => {
    vi.mocked(api.fetchStanding).mockResolvedValue([{ id: 's1', name: 'Labels', on: true, clusters: ['dev-1'], actions: ['host.label'], perHour: 5, by: { key: 'sam', name: 'Sam' } }])
    vi.mocked(api.saveStanding).mockRejectedValue(new Error('prod-eu is a production cluster, where a standing approval is never allowed'))
    render(<StandingApprovals />)
    expect((await screen.findByTestId('standing-row')).textContent).toContain('5 an hour')
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    fireEvent.click(screen.getByTestId('standing-save'))
    expect((await screen.findByTestId('standing-error')).textContent).toContain('production')
    vi.mocked(api.deleteStanding).mockResolvedValue({ removed: true })
    fireEvent.click(screen.getByTestId('standing-remove'))
    await waitFor(() => expect(api.deleteStanding).toHaveBeenCalledWith('s1'))
  })
})

describe('Settings › Watson: notifications', () => {
  it('never shows an address, sends one only when typed, and tests a channel', async () => {
    const saved: api.Notifications = { link: 'https://221b.example.com', inSecret: true, channels: [{ id: 'n1', kind: 'slack', name: 'ops', on: true, urlSet: true, lastAt: '2026-11-05T03:12:00Z' }] }
    vi.mocked(api.fetchNotifications).mockResolvedValue(saved)
    vi.mocked(api.saveNotifications).mockImplementation(async (link, chans) => ({ link, inSecret: true, channels: chans.map((c, i) => ({ id: c.id ?? `n${i + 2}`, kind: c.kind, name: c.name, on: c.on, urlSet: true })) }))
    vi.mocked(api.testChannel).mockResolvedValue({ sent: true })
    render(<NotificationChannels />)
    const url = (await screen.findByTestId('notify-url')) as HTMLInputElement
    expect(url.value).toBe('')
    expect(url.type).toBe('password')
    expect(screen.getByTestId('notify-channels').textContent).toContain('(set; type to replace it)')
    fireEvent.click(screen.getByTestId('notify-test'))
    await waitFor(() => expect(api.testChannel).toHaveBeenCalledWith('n1'))
    fireEvent.click(screen.getByTestId('notify-add'))
    const names = screen.getAllByLabelText('Channel name')
    fireEvent.change(names[1]!, { target: { value: 'alerts' } })
    fireEvent.change(screen.getAllByLabelText('Kind')[1]!, { target: { value: 'teams' } })
    fireEvent.change(screen.getAllByTestId('notify-url')[1]!, { target: { value: 'https://example.webhook.office.com/x' } })
    fireEvent.click(screen.getByTestId('notify-save'))
    await waitFor(() => expect(api.saveNotifications).toHaveBeenCalled())
    const [link, chans] = vi.mocked(api.saveNotifications).mock.calls[0]!
    expect(link).toBe('https://221b.example.com')
    expect(chans[0]).toEqual({ id: 'n1', kind: 'slack', name: 'ops', on: true, url: undefined, clearUrl: undefined })
    expect(chans[1]).toMatchObject({ kind: 'teams', name: 'alerts', on: true, url: 'https://example.webhook.office.com/x' })
    await waitFor(() => expect((screen.getAllByTestId('notify-url')[1] as HTMLInputElement).value).toBe(''))
  })
})
