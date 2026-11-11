import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InPlace } from './InPlace'

// A pencil on every field edited in place (build 14): beside the value,
// also when the field is empty; the value opens it too, unless it shows a
// link of its own.
describe('InPlace', () => {
  afterEach(() => cleanup())
  const save = vi.fn(() => Promise.resolve(true))

  it('shows a pencil beside a value, and both open the field', () => {
    render(<InPlace value="The checkout is slow" label="Title" testId="t" onSave={save} />)
    const pencil = screen.getByTestId('t-edit')
    expect(pencil.className).toBe('rk-icon-btn')
    expect(pencil.getAttribute('aria-label')).toBe('Edit title')
    expect(pencil.querySelector('svg')).toBeTruthy()
    expect(screen.getByTestId('t-value').textContent).toBe('The checkout is slow')
    fireEvent.click(pencil)
    expect((screen.getByTestId('t-input') as HTMLInputElement).value).toBe('The checkout is slow')
    fireEvent.keyDown(screen.getByTestId('t-input'), { key: 'Escape' })
    fireEvent.click(screen.getByTestId('t-value'))
    expect(screen.getByTestId('t-input')).toBeTruthy()
  })

  it('keeps the pencil when the field is empty, beside what stands for nothing', () => {
    render(<InPlace value="" label="What it is about" testId="t" empty={<span>nothing named</span>} onSave={save} />)
    expect(screen.getByTestId('t-edit').getAttribute('aria-label')).toBe('Edit what it is about')
    expect(screen.getByTestId('t-value').textContent).toBe('nothing named')
    fireEvent.click(screen.getByTestId('t-edit'))
    expect((screen.getByTestId('t-input') as HTMLInputElement).value).toBe('')
  })

  it('shows only the pencil as the opener when the value is a link of its own', () => {
    render(
      <InPlace value="shop/Deployment/web" label="What it is about" testId="t" onSave={save}>
        <a href="#/x">web</a>
      </InPlace>,
    )
    expect(screen.getByTestId('t-edit')).toBeTruthy()
    expect(screen.queryByTestId('t-value')).toBeNull()
    expect(screen.getByText('web').getAttribute('href')).toBe('#/x')
  })
})
