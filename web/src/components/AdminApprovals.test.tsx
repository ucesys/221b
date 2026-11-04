import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { decideRequest, fetchApprovals, type ApprovalRow } from '../api/automation'
import { AdminApprovals } from './AdminApprovals'
import type { AdminProps } from './AdminPage'

vi.mock('../api/automation', async (original) => ({ ...(await original<typeof import('../api/automation')>()), fetchApprovals: vi.fn(), decideRequest: vi.fn() }))

const row = (over: Partial<ApprovalRow>): ApprovalRow => ({ kind: 'admin', state: 'pending', what: 'Prod: environment production → staging', requestedBy: 'Ann', requestedAt: '2026-11-03T10:00:00Z', id: '1', cluster: 'prod', clusterName: 'Prod', ...over })

afterEach(cleanup)

describe('a console change on Approvals', () => {
  beforeEach(() => {
    vi.mocked(fetchApprovals).mockReset()
    vi.mocked(decideRequest).mockReset().mockResolvedValue({})
  })

  it('is approved here, after a confirmation, and the list is read again', async () => {
    vi.mocked(fetchApprovals).mockResolvedValue({ items: [row({})] })
    render(<AdminApprovals {...({} as AdminProps)} />)
    fireEvent.click(await screen.findByTestId('approval-admin-approve'))
    expect(decideRequest).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(decideRequest).toHaveBeenCalledWith('1', true, ''))
    await waitFor(() => expect(fetchApprovals).toHaveBeenCalledTimes(2))
    expect(screen.getByTestId('approval-admin').textContent).toContain('Console change')
  })

  it('is only withdrawn, with a reason, by whoever asked', async () => {
    vi.mocked(fetchApprovals).mockResolvedValue({ items: [row({ own: true })] })
    render(<AdminApprovals {...({} as AdminProps)} />)
    const withdraw = await screen.findByTestId('approval-admin-reject')
    expect(screen.queryByTestId('approval-admin-approve')).toBeNull()
    expect(withdraw.textContent).toBe('Withdraw…')
    fireEvent.click(withdraw)
    expect((screen.getByTestId('confirm-ok') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByTestId('decide-comment'), { target: { value: 'not now' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(decideRequest).toHaveBeenCalledWith('1', false, 'not now'))
  })

  it('offers nothing once decided', async () => {
    vi.mocked(fetchApprovals).mockResolvedValue({ items: [row({ state: 'approved', approvedBy: 'Sam', approvedAt: '2026-11-03T11:00:00Z' })] })
    render(<AdminApprovals {...({} as AdminProps)} />)
    await screen.findByTestId('approval-admin')
    expect(screen.queryByTestId('approval-admin-approve')).toBeNull()
    expect(screen.queryByTestId('approval-admin-reject')).toBeNull()
  })
})
