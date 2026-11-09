import { afterEach, describe, expect, it } from 'vitest'
import { errorSummary, forgetHandoffForTests, HANDOFF_TTL, reportBase, reportHref, sherlockSummary, takeHandoff } from './reportLink'

afterEach(() => {
  localStorage.clear()
  forgetHandoffForTests()
  window.history.replaceState(null, '', '/')
})

const stored = () => Object.keys(localStorage).filter((k) => k.startsWith('221b.report.handoff.'))

describe('links into the report page (CS-15 to CS-19, ADR-89)', () => {
  it('goes to the settings’ address, /report by default, and to the person’s own reports', () => {
    expect(reportBase('')).toBe('/report')
    expect(reportBase('https://help.example.com/report/')).toBe('https://help.example.com/report')
    expect(reportHref('')).toBe('/report')
    expect(reportHref('/report', { mine: true })).toBe('/report/mine')
    expect(reportHref('/report', { forUser: 'bob smith' })).toBe('/report?for=bob%20smith')
  })

  it('hands text over in this browser, the fragment naming it, never in the query', () => {
    const url = reportHref('/report', { text: 'Checkout hangs', forUser: 'bob' })
    expect(url).toMatch(/^\/report\?for=bob#handoff=[\w-]+$/)
    expect(url).not.toContain('Checkout')
    expect(stored()).toHaveLength(1)
  })

  it('puts the text in the fragment for a report page on another origin', () => {
    const url = reportHref('https://help.example.com/report', { text: 'Checkout hangs' })
    expect(url).toBe('https://help.example.com/report#text=Checkout%20hangs')
    expect(url.split('#')[0]).not.toContain('Checkout')
    expect(stored()).toHaveLength(0)
  })

  it('is read once on the report page, removed, and the fragment taken off the address', () => {
    const url = reportHref('/report', { text: 'Checkout hangs after Pay' })
    const hash = url.slice(url.indexOf('#'))
    window.history.replaceState(null, '', '/report' + hash)
    expect(takeHandoff()).toBe('Checkout hangs after Pay')
    expect(stored()).toHaveLength(0)
    expect(window.location.hash).toBe('')
    // The same fragment read again in the same page (strict mode) gives the same text.
    expect(takeHandoff(hash)).toBe('Checkout hangs after Pay')
    forgetHandoffForTests()
    expect(takeHandoff(hash)).toBe('')
  })

  it('throws away a handover nobody took in time', () => {
    const now = Date.now()
    const url = reportHref('/report', { text: 'old' }, now - HANDOFF_TTL - 1)
    expect(takeHandoff(url.slice(url.indexOf('#')), now)).toBe('')
    // A later link sweeps what was left behind.
    reportHref('/report', { text: 'stale' }, now - HANDOFF_TTL - 1)
    reportHref('/report', { text: 'new' }, now)
    expect(stored()).toHaveLength(1)
  })

  it('reads #text= too', () => {
    expect(takeHandoff('#text=It%20broke')).toBe('It broke')
  })
})

describe('what is handed over', () => {
  it('summarises Sherlock’s last question and answer in words, with no code', () => {
    const s = sherlockSummary('prod-eu', 'Why is `shop` crash-looping?', 'The pods exit.\n```\nkubectl logs web-1\npassword=hunter2\n```\nSee [the runbook](https://x).')
    expect(s).toContain('I asked Sherlock about prod-eu: Why is shop crash-looping?')
    expect(s).toContain('The pods exit.')
    expect(s).toContain('See the runbook.')
    expect(s).not.toContain('kubectl')
    expect(s).not.toContain('hunter2')
    expect(s).toContain('It is not fixed yet')
  })

  it('names the screen, the error, the page and the time for an error screen', () => {
    const s = errorSummary('the console', 'The server answered 500.', '#/pods', new Date('2026-11-05T10:00:00Z'))
    expect(s).toBe('221B showed an error on the console.\nError: The server answered 500.\nPage: #/pods\nWhen: 2026-11-05T10:00:00.000Z\n\nWhat I was doing:')
  })
})
