import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from './ConfirmDialog'

describe('ConfirmDialog', () => {
  afterEach(cleanup)
  it('keeps Tab inside, starts on Cancel, and cancels on Escape', () => {
    const onCancel = vi.fn()
    render(
      <ConfirmDialog title="Delete pod api-1?" confirm="Delete pod" danger onConfirm={() => undefined} onCancel={onCancel}>
        <p>Its controller starts a new pod in its place.</p>
      </ConfirmDialog>,
    )
    const cancel = screen.getByText('Cancel', { selector: 'button' })
    const ok = screen.getByTestId('confirm-ok')
    expect(document.activeElement).toBe(cancel)
    fireEvent.keyDown(ok, { key: 'Tab' })
    expect(document.activeElement).toBe(cancel)
    fireEvent.keyDown(cancel, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(ok)
    fireEvent.keyDown(ok, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('renders at the end of the document, outside the row or cell that opened it, and its text wraps', () => {
    const rowKeys = vi.fn()
    const rowClicks = vi.fn()
    render(
      <div role="row" onKeyDown={rowKeys} onClick={rowClicks} style={{ whiteSpace: 'nowrap' }} data-testid="row">
        <ConfirmDialog title="Delete pod api-1?" confirm="Delete pod" onConfirm={() => undefined} onCancel={() => undefined}>
          <p>In web. Its controller, if it has one, starts a new pod in its place.</p>
        </ConfirmDialog>
      </div>,
    )
    const dialog = screen.getByTestId('confirm')
    expect(screen.getByTestId('row').contains(dialog)).toBe(false)
    expect(dialog.parentElement?.style.whiteSpace).toBe('normal')
    // The row's own handlers never hear the dialog's keys or clicks.
    fireEvent.keyDown(screen.getByText('Cancel', { selector: 'button' }), { key: 'ArrowDown' })
    fireEvent.click(screen.getByText(/Its controller/))
    expect(rowKeys).not.toHaveBeenCalled()
    expect(rowClicks).not.toHaveBeenCalled()
  })
})
