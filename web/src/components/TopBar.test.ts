import { describe, expect, it } from 'vitest'
import { navPlan, secondsToRefresh } from './TopBar'
import type { Tab } from '../lib/router'

const all: Tab[] = ['nodes', 'namespaces', 'pods', 'helm', 'services']
const w = { nodes: 74, namespaces: 112, pods: 72, helm: 61, services: 86, more: 53, 'more:services': 94, 'more:helm': 81 }

describe('the tab bar', () => {
  it('shows every tab while they fit, with no More button', () => {
    // 405 px of tabs and 5 gaps of 22: 515.
    expect(navPlan(all, 'nodes', { avail: 515, gap: 22, w })).toEqual({ vis: all, moreAs: '' })
    expect(navPlan(all, 'services', { avail: 900, gap: 22, w })).toEqual({ vis: all, moreAs: '' })
    expect(navPlan(all, 'services', null)).toEqual({ vis: all, moreAs: '' })
  })

  it('moves tabs under More from the right as the bar narrows, keeping the open one', () => {
    // Room for four tabs beside More: Services goes under it.
    expect(navPlan(all, 'nodes', { avail: 505, gap: 22, w })).toEqual({ vis: ['nodes', 'namespaces', 'pods', 'helm'], moreAs: '' })
    // The open tab that does not fit rides on the More button, which reads as it.
    expect(navPlan(all, 'services', { avail: 505, gap: 22, w })).toEqual({ vis: ['nodes', 'namespaces', 'pods', 'helm'], moreAs: 'services' })
    // When that wider button needs the room, a tab drops from the right.
    expect(navPlan(all, 'services', { avail: 470, gap: 22, w })).toEqual({ vis: ['nodes', 'namespaces', 'pods'], moreAs: 'services' })
    // Narrower still: tabs drop from the right until the wide More button fits.
    expect(navPlan(all, 'services', { avail: 360, gap: 16, w })).toEqual({ vis: ['nodes', 'namespaces'], moreAs: 'services' })
    expect(navPlan(all, 'pods', { avail: 360, gap: 16, w })).toEqual({ vis: ['nodes', 'namespaces', 'pods'], moreAs: '' })
  })

  it('counts down to the next refresh', () => {
    const at = new Date('2026-10-13T12:00:00Z')
    expect(secondsToRefresh(at, 30, at.getTime())).toBe(30)
    expect(secondsToRefresh(at, 30, at.getTime() + 12_400)).toBe(18)
    expect(secondsToRefresh(at, 30, at.getTime() + 40_000)).toBe(0)
    expect(secondsToRefresh(null, 30, at.getTime())).toBeNull()
    expect(secondsToRefresh(at, 0, at.getTime())).toBeNull()
  })
})
