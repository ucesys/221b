// The one Reject… and Withdraw… dialog (I-4): the same words in every
// place, the reason required unless the place keeps it optional.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DecideDialog } from './DecideDialog'

afterEach(cleanup)

const ok = () => screen.getByTestId('confirm-ok') as HTMLButtonElement

describe('DecideDialog (I-4)', () => {
  it('rejects only with a reason, by default', () => {
    const onConfirm = vi.fn()
    render(
      <DecideDialog how="reject" what="ensure-chrony v3" onCancel={() => {}} onConfirm={onConfirm}>
        It never runs.
      </DecideDialog>,
    )
    expect(screen.getByRole('alertdialog').getAttribute('aria-label')).toBe('Reject ensure-chrony v3?')
    expect(ok().textContent).toBe('Reject')
    expect(screen.getByText('Why (required)')).toBeTruthy()
    expect(ok().disabled).toBe(true)
    fireEvent.change(screen.getByTestId('decide-comment'), { target: { value: '  not now ' } })
    fireEvent.click(ok())
    expect(onConfirm).toHaveBeenCalledWith('not now')
  })

  it('says Withdraw to whoever asked, with the same reason field', () => {
    render(
      <DecideDialog how="withdraw" what="PC-4" onCancel={() => {}} onConfirm={() => {}}>
        You asked for this.
      </DecideDialog>,
    )
    expect(screen.getByRole('alertdialog').getAttribute('aria-label')).toBe('Withdraw PC-4?')
    expect(ok().textContent).toBe('Withdraw')
    expect(screen.getByTestId('decide-comment')).toBeTruthy()
    expect(ok().disabled).toBe(true)
  })

  it('takes the reason optionally where the place keeps it so', () => {
    const onConfirm = vi.fn()
    render(
      <DecideDialog how="reject" what="the request" reasonRequired={false} onCancel={() => {}} onConfirm={onConfirm}>
        Ann reads your reason.
      </DecideDialog>,
    )
    expect(screen.getByText('Why (optional)')).toBeTruthy()
    expect(ok().disabled).toBe(false)
    fireEvent.click(ok())
    expect(onConfirm).toHaveBeenCalledWith('')
  })

  it('asks no reason to approve', () => {
    render(
      <DecideDialog how="approve" what="change #2" onCancel={() => {}} onConfirm={() => {}}>
        It takes effect at once.
      </DecideDialog>,
    )
    expect(ok().textContent).toBe('Approve')
    expect(screen.queryByTestId('decide-comment')).toBeNull()
    expect(ok().disabled).toBe(false)
  })
})
