// A closed drawer gives the focus back to what opened it (WCAG 2.4.3).
import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { drive } from '../lib/tables'
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

  it('leaves focus in a field whose text the title follows', () => {
    Element.prototype.scrollTo = () => {}
    const form = (title: string) => (
      <Drawer label="Script" kicker="New script" title={title} onClose={() => {}}>
        <input aria-label="Name" />
      </Drawer>
    )
    const { rerender, getByLabelText } = render(form('new-script'))
    const name = getByLabelText('Name')
    name.focus()
    rerender(form('e'))
    rerender(form('en'))
    expect(document.activeElement).toBe(name)
  })

  // A button that disables itself, as Save does while it saves, or goes
  // away, as Submit does, must not leave Escape and Tab with nowhere to land.
  it('keeps focus in the drawer when the focused button is disabled or removed', () => {
    Element.prototype.scrollTo = () => {}
    const onClose = vi.fn()
    const form = (button: 'on' | 'off' | 'gone') => (
      <Drawer label="Script" kicker="Script" title="s" onClose={onClose}>
        {button !== 'gone' && <button disabled={button === 'off'}>Save</button>}
      </Drawer>
    )
    const { rerender, getByRole, getByText } = render(form('on'))
    const aside = getByRole('dialog')
    getByText('Save').focus()
    rerender(form('off'))
    expect(document.activeElement).toBe(aside)
    fireEvent.keyDown(aside, { key: 'Tab', shiftKey: true })
    expect(aside.contains(document.activeElement)).toBe(true)
    expect(document.activeElement).not.toBe(aside)
    aside.focus()
    fireEvent.keyDown(aside, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
    rerender(form('on'))
    getByText('Save').focus()
    rerender(form('gone'))
    expect(document.activeElement).toBe(aside)
  })

  it('leaves focus alone in a drawer Sherlock opened, which is not modal', () => {
    Element.prototype.scrollTo = () => {}
    const { rerender } = drive(() =>
      render(
        <Drawer label="Pod" kicker="Pod" title="a" onClose={() => {}}>
          <p>Body</p>
        </Drawer>,
      ),
    )
    ;(document.activeElement as HTMLElement).blur()
    rerender(
      <Drawer label="Pod" kicker="Pod" title="b" onClose={() => {}}>
        <p>Body</p>
      </Drawer>,
    )
    expect(document.activeElement).toBe(document.body)
  })
})
