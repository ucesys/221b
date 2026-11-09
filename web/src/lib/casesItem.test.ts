import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { casesItemOf, currentCasesItem, forgetCasesItemsForTests, useCasesItem, useOpenCasesItem } from './casesItem'
import { parseHash } from './router'
import { viewState } from './uiControl'

afterEach(() => forgetCasesItemsForTests())

describe('the item open in Cases (IB-11)', () => {
  it('reads what the address names', () => {
    const of = (h: string) => casesItemOf(parseHash(h))
    // The Inbox's drawers (IN-06), and the addresses that moved there.
    expect(of('#/cases/inbox/case/12')).toEqual({ kind: 'case', id: 'C-12' })
    expect(of('#/cases/inbox/case/new')).toBeNull()
    // Findings are not Inbox items since wave 6.
    expect(of('#/cases/inbox/finding/r-1.f2')).toBeNull()
    expect(of('#/cases/inbox/proposal/PC-4')).toEqual({ kind: 'proposal', id: 'PC-4' })
    // A proposed change's c<n> of before is its PC-<n> (CF-56).
    expect(of('#/cases/inbox/proposal/c4')).toEqual({ kind: 'proposal', id: 'PC-4' })
    expect(of('#/cases/inbox/approval/script-x-v2')).toEqual({ kind: 'approval', id: 'script-x-v2' })
    expect(of('#/cases/inbox/known/KI-3')).toEqual({ kind: 'known', id: 'KI-3' })
    expect(of('#/cases/inbox/knowledge/d1')).toEqual({ kind: 'knowledge', id: 'd1' })
    expect(of('#/cases/inbox/report/7')).toEqual({ kind: 'case', id: 'C-7' })
    expect(of('#/cases/cases/12')).toEqual({ kind: 'case', id: 'C-12' })
    expect(of('#/cases/reports/7')).toEqual({ kind: 'case', id: 'C-7' })
    expect(of('#/cases/known/KI-3')).toEqual({ kind: 'known', id: 'KI-3' })
    // Mycroft's (wave 5).
    expect(of('#/cases/mycroft/exceptions/EX-2')).toEqual({ kind: 'exception', id: 'EX-2' })
    // No RCAs of their own since wave 6e: an old RCA's address names nothing.
    expect(of('#/cases/mycroft/rcas/RCA-1')).toBeNull()
    expect(of('#/cases/mycroft/wiki/pages/runbooks/dns.md')).toEqual({ kind: 'page', id: 'runbooks/dns.md' })
    expect(of('#/cases/mycroft/wiki/drafts')).toBeNull()
    expect(of('#/cases/inbox?proposal=c4')).toEqual({ kind: 'proposal', id: 'PC-4' })
    expect(of('#/cases/inbox?report=7')).toEqual({ kind: 'case', id: 'C-7' })
    expect(of('#/cases/jobs/runs/run-9')).toEqual({ kind: 'run', id: 'run-9' })
    expect(of('#/cases/jobs/auditor-daily')).toEqual({ kind: 'job', id: 'auditor-daily' })
    expect(of('#/cases/jobs/runs')).toBeNull()
    expect(of('#/cases/inbox')).toBeNull()
    expect(of('#/pods')).toBeNull()
  })

  it('prefers what a drawer says, the one on top, while it is open', () => {
    const route = parseHash('#/cases/inbox?proposal=c4')
    const seen = renderHook(() => useOpenCasesItem())
    const a = renderHook(() => useCasesItem({ kind: 'proposal', id: 'PC-4', cluster: 'prod', summary: 'Restart shop/web · Waiting · 0 of 2' }))
    expect(currentCasesItem(route)).toEqual({ kind: 'proposal', id: 'PC-4', cluster: 'prod', summary: 'Restart shop/web · Waiting · 0 of 2' })
    expect(seen.result.current?.cluster).toBe('prod')
    const b = renderHook(() => useCasesItem({ kind: 'case', id: 'C-3', cluster: 'dev' }))
    expect(currentCasesItem(route)?.id).toBe('C-3')
    act(() => b.unmount())
    expect(currentCasesItem(route)?.id).toBe('PC-4')
    act(() => a.unmount())
    expect(currentCasesItem(route)).toEqual({ kind: 'proposal', id: 'PC-4' })
    expect(seen.result.current).toBeNull()
  })

  it('goes to Sherlock with the view: the page, its filters and the item', () => {
    renderHook(() => useCasesItem({ kind: 'case', id: 'C-12', cluster: 'prod', summary: 'shop/web crash-loops · P1 · New' }))
    const v = viewState(parseHash('#/c/prod/cases/inbox/case/12?who=mine'))
    expect(v).toMatchObject({ cluster: 'prod', view: 'cases', path: ['inbox', 'case', '12'], filters: { who: 'mine' }, item: { kind: 'case', id: 'C-12', cluster: 'prod', summary: 'shop/web crash-loops · P1 · New' } })
  })
})
