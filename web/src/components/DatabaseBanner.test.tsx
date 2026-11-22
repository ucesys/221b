import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { call, CallError } from '../api/call'
import * as database from '../api/database'
import { forgetSharedForTests, reloadShared } from '../lib/cases'
import { AWAY_EVERY, DATABASE } from '../lib/database'
import { DATABASE_BANNER_DETAIL, DatabaseBanner, databaseBanner } from './DatabaseBanner'

vi.mock('../api/database', async (original) => ({ ...(await original<typeof import('../api/database')>()), fetchDatabase: vi.fn() }))

const there: database.DatabaseState = { configured: true, available: true, since: '2026-11-20T08:00:00Z' }
const away: database.DatabaseState = { configured: true, available: false, since: '2026-11-20T09:15:00Z' }
const SENTENCE = 'the database is unavailable; sign-in and the pages read live from the clusters still work'

/** Lets the promises a reading left settle. */
const settle = () => act(async () => void (await Promise.resolve()))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
  forgetSharedForTests()
})

describe('the banner while the database is away (DB-15)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  it('says what still works and what does not, for a state that says the database is away', () => {
    const b = databaseBanner(away)
    expect(b?.title).toBe('The database is unavailable')
    expect(b?.detail).toContain('Sign-in and the pages read live from the clusters still work.')
    expect(b?.detail).toContain('cannot be read or changed until it is back: the Inbox, cases and reports, jobs and their runs, Mycroft, Automation, applications, credentials, the audit log and the settings.')
    expect(b?.detail).toContain('Unavailable since ')
    expect(b?.detail.endsWith('This banner goes when the database is back.')).toBe(true)
    // Why, as the server words it, which it gives to a SuperAdmin only.
    expect(databaseBanner({ ...away, error: 'the database refuses connections' })?.detail).toContain(' Why: the database refuses connections. ')
    expect(databaseBanner(away)?.detail).not.toContain('Why:')
  })

  it('has no banner while the database is there, before the first answer, or for a console with no database', () => {
    expect(databaseBanner(there)).toBeNull()
    expect(databaseBanner(null)).toBeNull()
    expect(databaseBanner({ configured: false, available: false })).toBeNull()
  })

  it('shows on the page while GET /api/v1/database says the database is away, as a polite status, and goes by itself when it is back', async () => {
    vi.mocked(database.fetchDatabase).mockResolvedValue(away)
    render(<DatabaseBanner />)
    await settle()
    const banner = screen.getByTestId('database-banner')
    expect(banner.textContent).toContain('The database is unavailable')
    expect(banner.textContent).toContain(DATABASE_BANNER_DETAIL)
    // As the other banners: a status read out once, nothing to focus or dismiss.
    const status = banner.closest('[role=status]')
    expect(status?.getAttribute('aria-live')).toBe('polite')
    expect(banner.querySelector('button, a, [tabindex]')).toBeNull()
    expect(database.fetchDatabase).toHaveBeenCalledTimes(1)
    // Away, the state is asked again every ten seconds, not in a loop.
    await act(async () => void (await vi.advanceTimersByTimeAsync(AWAY_EVERY - 1)))
    expect(database.fetchDatabase).toHaveBeenCalledTimes(1)
    vi.mocked(database.fetchDatabase).mockResolvedValue({ ...there, since: '2026-11-20T09:20:00Z' })
    await act(async () => void (await vi.advanceTimersByTimeAsync(1)))
    expect(database.fetchDatabase).toHaveBeenCalledTimes(2)
    // Back: the banner is gone, with nobody doing anything.
    expect(screen.queryByTestId('database-banner')).toBeNull()
    // And from then on at the app's usual rate alone: every half minute from when the page opened, no longer every ten seconds.
    await act(async () => void (await vi.advanceTimersByTimeAsync(AWAY_EVERY * 2 - 1)))
    expect(database.fetchDatabase).toHaveBeenCalledTimes(2)
    await act(async () => void (await vi.advanceTimersByTimeAsync(1)))
    expect(database.fetchDatabase).toHaveBeenCalledTimes(3)
    await act(async () => void (await vi.advanceTimersByTimeAsync(AWAY_EVERY * 2)))
    expect(database.fetchDatabase).toHaveBeenCalledTimes(3)
  })

  it('shows nothing while the database is there, and keeps what it last knew when the state cannot be read', async () => {
    vi.mocked(database.fetchDatabase).mockResolvedValue(there)
    render(<DatabaseBanner />)
    await settle()
    expect(screen.queryByTestId('database-banner')).toBeNull()
    vi.mocked(database.fetchDatabase).mockRejectedValue(new Error('The server could not be reached.'))
    await act(async () => reloadShared(DATABASE))
    await settle()
    expect(screen.queryByTestId('database-banner')).toBeNull()
  })

  it('appears as soon as any answer says the database is unavailable, without waiting for the half minute', async () => {
    vi.mocked(database.fetchDatabase).mockResolvedValue(there)
    render(<DatabaseBanner />)
    await settle()
    expect(screen.queryByTestId('database-banner')).toBeNull()
    // A page's call is refused: a 503 with the mark, as the tenth cluster test saw on every page that needs the database.
    vi.mocked(database.fetchDatabase).mockResolvedValue(away)
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ error: `reading the credentials: ${SENTENCE}` }), { status: 503, headers: { 'X-221b-Database': 'unavailable' } }))
    let refused: unknown
    await act(async () => {
      refused = await call('/api/v1/credentials').catch((e: unknown) => e)
    })
    await settle()
    expect(refused).toBeInstanceOf(CallError)
    expect((refused as CallError).status).toBe(503)
    expect((refused as CallError).databaseAway).toBe(true)
    expect(database.fetchDatabase).toHaveBeenCalledTimes(2)
    expect(screen.getByTestId('database-banner')).toBeTruthy()
    // More refusals do not ask again: the state already says so.
    await act(async () => void (await call('/api/v1/jobs').catch(() => undefined)))
    expect(database.fetchDatabase).toHaveBeenCalledTimes(2)
  })

  it('does not take a 503 without the mark (a part that is off) or the same words under another status for the database', async () => {
    vi.mocked(database.fetchDatabase).mockResolvedValue(there)
    render(<DatabaseBanner />)
    await settle()
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ error: 'Cases are off on this console.' }), { status: 503 }))
    const off = (await call('/api/v1/aiops/cases').catch((e: unknown) => e)) as CallError
    expect(off.status).toBe(503)
    expect(off.databaseAway).toBe(false)
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ error: SENTENCE }), { status: 500 }))
    expect(((await call('/api/v1/jobs').catch((e: unknown) => e)) as CallError).databaseAway).toBe(false)
    await settle()
    expect(database.fetchDatabase).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('database-banner')).toBeNull()
  })
})
