// A closed drawer gives the focus back to what opened it (WCAG 2.4.3).
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Drawer } from './Drawer'

describe('Drawer', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('returns focus to its opener, which it read before taking focus itself', () => {
    vi.stubGlobal('requestAnimationFrame', (f: FrameRequestCallback) => (f(0), 0))
    Element.prototype.scrollTo = () => {}
    const opener = document.body.appendChild(document.createElement('button'))
    opener.focus()
    const { unmount, getAllByRole } = render(
      <Drawer label="Pod details" kicker="Pod" title="web-0" onClose={() => {}}>
        <p>Body</p>
      </Drawer>,
    )
    expect(document.activeElement).toBe(getAllByRole('button', { name: 'Close pod details' })[1])
    unmount()
    expect(document.activeElement).toBe(opener)
    opener.remove()
  })
})
