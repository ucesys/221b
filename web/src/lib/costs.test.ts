import { describe, expect, it } from 'vitest'
import { hideAmounts } from './costs'

describe('hideAmounts (CF-40)', () => {
  it('takes the dollar amounts out of a server’s text while costs are hidden', () => {
    expect(hideAmounts('not started: the day’s budget of $10.00 is spent', false)).toBe('not started: the day’s budget is spent')
    expect(hideAmounts('stopped at $4 today; about US$1,200.50 this month', false)).toBe('stopped today; this month')
    expect(hideAmounts('the day’s budget of $10.00 is spent', true)).toBe('the day’s budget of $10.00 is spent')
  })
})
