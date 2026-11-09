import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ClusterInfo, Me } from '../api/types'
import { parseHash } from '../lib/router'
import type { InboxCounts } from './cases/useInbox'
import { inboxCounts } from './cases/useInbox'
import { suggestionRow, type InboxRow } from './cases/inboxRows'
import { bellOf, TopBar } from './TopBar'

// What the Inbox has pending for the person, as each test sets it.
let counts: InboxCounts
const none: InboxCounts = { total: 0, byKind: { case: 0, proposal: 0, approval: 0, known: 0, knowledge: 0, rule: 0 }, p0: 0, p1: 0 }

vi.mock('./cases/useInbox', async (original) => ({
  ...(await original<typeof import('./cases/useInbox')>()),
  useInboxCounts: () => counts,
}))
vi.mock('../lib/cases', async (original) => ({ ...(await original<typeof import('../lib/cases')>()), useIssueMe: () => null }))

const admin: Me = { mode: 'oidc', user: { subject: 's', username: 'ann', name: 'Ann' }, admin: true, roles: ['superAdmin'], method: 'oidc', sessionIdleSeconds: 0 }
// Signed in with no role: nothing in Cases is for them.
const nobody: Me = { mode: 'oidc', user: { subject: 'n', username: 'ned', name: 'Ned' }, admin: false, roles: [], method: 'oidc', sessionIdleSeconds: 0 }
const cluster = { id: 'local', name: 'Here', state: 'connected' } as unknown as ClusterInfo

const bar = (me: Me = admin, hash = '#/cases/inbox') =>
  render(<TopBar cluster={cluster} counts={{ alerts: 2 }} tones={{ alerts: 'err' }} tab={hash === '#/nodes' ? 'nodes' : null} stream="open" updatedAt={null} refreshedAt={null} settings={null} theme="light" onTheme={() => undefined} me={me} route={parseHash(hash)} onEditCluster={() => undefined} />)

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  counts = { total: 3, byKind: { case: 2, proposal: 1, approval: 0, known: 0, knowledge: 0, rule: 0 }, p0: 0, p1: 0 }
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('the top bar’s icons (CF-41, CF-80)', () => {
  it('puts Live first, then the bell, the gear and the account; no yellow i', () => {
    bar()
    const ids = ['live', 'inbox-bell', 'admin-link', 'account']
    const els = ids.map((id) => screen.getByTestId(id))
    // Each follows the one before it, side by side.
    for (let i = 1; i < els.length; i++) expect(els[i - 1]!.compareDocumentPosition(els[i]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const meta = els[0]!.parentElement!
    expect(meta.firstElementChild).toBe(els[0])
    expect(els[0]!.nextElementSibling).toBe(els[1])
    expect(screen.queryByTestId('admin-flag')).toBeNull()
    expect(screen.queryByTestId('alerts-bell')).toBeNull()
  })
})

describe('the bell is the Inbox’s (CF-80)', () => {
  it('is plain, with no number, when nothing waits', () => {
    counts = none
    bar()
    const bell = screen.getByTestId('inbox-bell')
    expect(bell.getAttribute('data-state')).toBe('none')
    expect(bell.style.color).toBe('')
    expect(bell.getAttribute('title')).toBe('Nothing waiting for you in Cases › Inbox')
    expect(bell.getAttribute('aria-label')).toBe('Nothing waiting for you in Cases › Inbox')
    expect(bell.textContent).toBe('')
    expect(bell.getAttribute('href')).toBe('#/cases/inbox')
  })

  it('is yellow while something waits, says how much, and opens the Inbox', () => {
    bar()
    const bell = screen.getByTestId('inbox-bell')
    expect(bell.getAttribute('data-state')).toBe('waiting')
    expect(bell.style.color).toBe('var(--warn)')
    expect(bell.getAttribute('title')).toBe('3 waiting for you: open Cases › Inbox')
    // No count on it; the Cases switch keeps its own.
    expect(bell.textContent).toBe('')
    expect(screen.getByTestId('cases-count').textContent).toBe('3')
    expect(bell.getAttribute('href')).toBe('#/cases/inbox')
  })

  it('is red while a P0 or P1 case waits, in every area', () => {
    counts = { ...counts, total: 12, p0: 1 }
    bar(admin, '#/nodes')
    let bell = screen.getByTestId('inbox-bell')
    expect(bell.getAttribute('data-state')).toBe('p01')
    expect(bell.style.color).toBe('var(--err)')
    expect(bell.getAttribute('title')).toBe('12 waiting for you, 1 P0: open Cases › Inbox')
    expect(bell.textContent).toBe('')
    cleanup()
    counts = { ...counts, total: 4, p0: 0, p1: 2 }
    bar()
    bell = screen.getByTestId('inbox-bell')
    expect(bell.getAttribute('data-state')).toBe('p01')
    expect(bell.getAttribute('title')).toBe('4 waiting for you, 2 P1: open Cases › Inbox')
  })

  it('is not there for whoever cannot see Cases', () => {
    bar(nobody, '#/nodes')
    expect(screen.getByTestId('live')).toBeTruthy()
    expect(screen.queryByTestId('inbox-bell')).toBeNull()
    expect(screen.queryByTestId('admin-flag')).toBeNull()
  })

  it('names both priorities when both wait', () => {
    expect(bellOf({ ...none, total: 5, p0: 1, p1: 3 })).toEqual({ state: 'p01', text: '5 waiting for you, 1 P0 and 3 P1: open Cases › Inbox' })
    expect(bellOf({ ...none, total: 1 })).toEqual({ state: 'waiting', text: '1 waiting for you: open Cases › Inbox' })
    expect(bellOf(none).state).toBe('none')
  })

  it('counts what the Inbox counts, and the P0 and P1 cases among it', () => {
    const row = (over: Partial<InboxRow>): InboxRow => ({ key: Math.random().toString(36), kind: 'case', open: ['case', '1'], ref: 'C-1', title: 't', status: 'New', tone: 'info', pending: true, since: '', assignable: true, mine: false, origin: 'people', ...over })
    const c = inboxCounts([
      row({ priority: 0 }),
      row({ priority: 1 }),
      row({ priority: 1 }),
      row({ priority: 2 }),
      // Closed: done, not waiting.
      row({ priority: 0, pending: false }),
      row({ kind: 'proposal', assignable: false }),
      row({ kind: 'approval', assignable: false, pending: false }),
    ])
    expect(c).toMatchObject({ total: 5, p0: 1, p1: 2 })
    expect(c.byKind).toMatchObject({ case: 4, proposal: 1, approval: 0 })
  })

  it('counts a suggested validation rule, which makes the bell yellow (CF-87)', () => {
    const c = inboxCounts([suggestionRow({ id: 'node.etcd-slow', says: 'etcd is slow', type: 'other', jobs: ['nightly-audit'], from: ['other.etcd-slow'], runs: 3, clusters: 1, created: '2026-11-09T03:30:00Z' })])
    expect(c).toMatchObject({ total: 1, p0: 0, p1: 0 })
    expect(c.byKind.rule).toBe(1)
    expect(bellOf(c)).toEqual({ state: 'waiting', text: '1 waiting for you: open Cases › Inbox' })
  })
})
