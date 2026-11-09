import { describe, expect, it } from 'vitest'
import type { IssueMe, OpKnown } from '../api/issues'
import type { Me } from '../api/types'
import { casesAccess, inboxOrder, knownDrafts, knownPending, knownReopened, mayReport } from './cases'

const me = (over: Partial<Me> = {}): Me => ({ mode: 'oidc', user: { subject: 's', username: 'ann' }, admin: false, roles: ['viewer'], method: 'oidc', sessionIdleSeconds: 0, ...over })
const im = (over: Partial<IssueMe> = {}): IssueMe => ({ person: { id: 'ann' }, report: false, operator: false, admin: false, company: '', statusPage: '', reportURL: '/report', ...over })

describe('who sees Cases (CS-01, CS-02, ADR-88)', () => {
  it('offers a SuperAdmin who operates every tab', () => {
    const a = casesAccess(me({ admin: true, roles: ['superAdmin'] }), im({ report: true, operator: true, admin: true }))
    expect(a).toMatchObject({ show: true, approver: true, operator: true, reporter: true, console: true })
    expect(a.tabs).toEqual(['inbox', 'jobs', 'mycroft'])
  })

  it('offers an operator who is no SuperAdmin the Inbox and Mycroft', () => {
    expect(casesAccess(me({ roles: ['admin'] }), im({ report: true, operator: true })).tabs).toEqual(['inbox', 'mycroft'])
  })

  it('offers someone who may only report the Inbox, for their own cases, and the known issues', () => {
    const a = casesAccess(me(), im({ report: true }))
    expect(a.tabs).toEqual(['inbox', 'mycroft'])
    expect(a.approver).toBe(false)
  })

  it('offers nothing to someone with nothing there, and nothing while the issue service is off but to SuperAdmins', () => {
    expect(casesAccess(me(), im()).show).toBe(false)
    expect(casesAccess(me(), null).show).toBe(false)
    expect(casesAccess(me({ admin: true, roles: ['superAdmin'] }), null).tabs).toEqual(['inbox', 'jobs', 'mycroft'])
    expect(casesAccess(null, null).show).toBe(false)
  })

  it('lets operators and holders of the Report permission report', () => {
    expect(mayReport(im({ report: true }))).toBe(true)
    expect(mayReport(im({ operator: true }))).toBe(true)
    expect(mayReport(im())).toBe(false)
    expect(mayReport(null)).toBe(false)
  })
})

describe('what waits in the Inbox (CS-03, CS-04)', () => {
  it('orders every group alike: what waits, then P0 first, then the oldest (IB-03)', () => {
    const items = [
      { id: 'decided', waiting: false, priority: 1, since: '2026-11-01T00:00:00Z' },
      { id: 'p3-old', waiting: true, priority: 3, since: '2026-11-01T00:00:00Z' },
      { id: 'p1-new', waiting: true, priority: 1, since: '2026-11-05T00:00:00Z' },
      { id: 'none', waiting: true, since: '2026-10-01T00:00:00Z' },
      { id: 'p1-old', waiting: true, priority: 1, since: '2026-11-02T00:00:00Z' },
      { id: 'p0', waiting: true, priority: 0, since: '2026-11-06T00:00:00Z' },
    ]
    expect([...items].sort(inboxOrder).map((x) => x.id)).toEqual(['p0', 'p1-old', 'p1-new', 'p3-old', 'none', 'decided'])
  })

  it('lists known issues an agent proposed', () => {
    const k = (id: string, over: Partial<OpKnown> = {}): OpKnown => ({ id, title: id, status: 'investigating', kind: 'normal', updated: '', following: false, source: 'agent', created: '', ...over })
    const ks = [k('KI-1', { draft: true }), k('KI-2'), k('KI-3', { draft: true, owner: { id: 'ann' } })]
    expect(knownDrafts(ks, 'ann').map((x) => x.id)).toEqual(['KI-1', 'KI-3'])
    expect(knownDrafts(ks, 'ann', 'mine').map((x) => x.id)).toEqual(['KI-3'])
    expect(knownDrafts(ks, 'ann', 'unassigned').map((x) => x.id)).toEqual(['KI-1'])
  })

  it('lists a known issue back for its owner: investigating again because the agents still see it (wave 6)', () => {
    const k = (id: string, over: Partial<OpKnown> = {}): OpKnown => ({ id, title: id, status: 'investigating', kind: 'normal', updated: '', following: false, source: 'operator', created: '', ...over })
    const back = k('KI-4', { owner: { id: 'ann' }, stillPresent: { count: 1, last: '2026-11-06T00:00:00Z' } })
    expect(knownReopened(back, 'ann')).toBe(true)
    expect(knownReopened(back, 'cy')).toBe(false)
    expect(knownReopened({ ...back, status: 'identified' }, 'ann')).toBe(false)
    expect(knownReopened({ ...back, stillPresent: undefined }, 'ann')).toBe(false)
    expect(knownPending([k('KI-1', { draft: true }), back, k('KI-5')], 'ann').map((x) => x.id)).toEqual(['KI-1', 'KI-4'])
  })
})
