import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { CardAlert } from '../WatsonAlert'
import { SelfReleaseCallout, ValuesOriginNote } from './SelfRelease'

describe("the console's own release and adopted values", () => {
  afterEach(cleanup)

  it('says the release runs this console', () => {
    render(<SelfReleaseCallout />)
    const c = screen.getByTestId('app-self')
    expect(c.getAttribute('data-tone')).toBe('err')
    expect(c.textContent).toContain('This is the release that runs this console: if it fails, the console goes down.')
  })

  it('says the values came from the release, and how masked ones save', () => {
    render(<ValuesOriginNote app={{ adoptedRevision: 7, valuesMasked: ['db.password'] }} />)
    const n = screen.getByTestId('app-values-origin')
    expect(n.textContent).toContain('These values came from the release')
    expect(n.textContent).toContain('revision 7')
    expect(n.textContent).toContain('masked (db.password)')
  })

  it('says nothing for values typed in the console', () => {
    const { container } = render(<ValuesOriginNote app={{}} />)
    expect(container.textContent).toBe('')
  })

  it("shows a card's alert first, and nothing without one", () => {
    const { rerender } = render(<CardAlert plan={{ ready: true, dryRunExit: 0, tone: 'danger', alert: 'This is the release that runs this console: if it fails, the console goes down.' }} />)
    expect(screen.getByTestId('watson-alert').textContent).toContain('runs this console')
    rerender(<CardAlert plan={{ ready: true, dryRunExit: 0, tone: 'warn' }} />)
    expect(screen.queryByTestId('watson-alert')).toBeNull()
  })
})
