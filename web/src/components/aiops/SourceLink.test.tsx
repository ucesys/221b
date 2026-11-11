import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { isWebUrl, sourceTestText } from '../../api/knowledge'
import { SourceLink } from './SourceLink'

describe('a source’s link back (KN-25)', () => {
  afterEach(cleanup)

  it('links a web address, and names a bucket’s object by its s3:// address with nothing to click', () => {
    render(
      <ul>
        <li data-testid="web">
          <SourceLink title="Shop" url="https://wiki.example.com/shop" />
        </li>
        <li data-testid="s3">
          <SourceLink title="db.md" url="s3://ops-docs/ops/db.md" />
        </li>
        <li data-testid="none">
          <SourceLink title="A note" />
        </li>
      </ul>,
    )
    expect(screen.getByTestId('web').querySelector('a')?.getAttribute('href')).toBe('https://wiki.example.com/shop')
    expect(screen.getByTestId('s3').querySelector('a')).toBeNull()
    expect(screen.getByTestId('s3').textContent).toBe('db.md s3://ops-docs/ops/db.md')
    expect(screen.getByTestId('none').textContent).toBe('A note')
    expect(isWebUrl('javascript:alert(1)')).toBe(false)
  })

  it('says a Test’s result in words (KN-26)', () => {
    expect(sourceTestText({ ok: true, items: 1, sample: ['a.md'] })).toBe('Reached: 1 item, e.g. a.md.')
    expect(sourceTestText({ ok: true, items: 7, sample: ['a.md', 'b.md'], skipped: 1, skippedWhy: '1 not a kind Mycroft reads (.png)' })).toBe('Reached: 7 items, e.g. a.md, b.md. 1 skipped: 1 not a kind Mycroft reads (.png).')
    expect(sourceTestText({ ok: true, items: 0 })).toBe('Reached: 0 items.')
    expect(sourceTestText({ ok: false, error: 'the bucket "x" refused the credentials' })).toBe('the bucket "x" refused the credentials')
  })
})
