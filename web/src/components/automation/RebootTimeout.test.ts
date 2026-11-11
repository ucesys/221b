import { describe, expect, it } from 'vitest'
import type { AutomationFleets } from '../../api/automation'
import { fleetRebootMinutes, nodeRebootMinutes, parseMinutes } from './RebootTimeout'

const fleets: AutomationFleets = {
  cluster: 'local',
  production: false,
  conflicts: [],
  fleets: [
    { id: 'a', name: 'A', nodes: [{ name: 'n1', paired: true }], params: { rebootMinutes: 5 } },
    {
      id: 'b',
      name: 'B',
      nodes: [
        { name: 'n1', paired: true },
        { name: 'n2', paired: true },
      ],
      params: { rebootMinutes: 20 },
    },
    {
      id: '_all',
      name: 'All nodes',
      nodes: [
        { name: 'n1', paired: true },
        { name: 'n2', paired: true },
        { name: 'n3', paired: true },
      ],
      params: { nodeReboot: { n2: 3 } },
    },
  ],
} as AutomationFleets

describe('reboot timeout', () => {
  it('gives a node the longest of its fleets, and its own over all of them', () => {
    expect(fleetRebootMinutes(fleets, 'n1')).toBe(20)
    expect(nodeRebootMinutes(fleets, 'n1')).toBe(20)
    expect(nodeRebootMinutes(fleets, 'n2')).toBe(3)
    expect(nodeRebootMinutes(fleets, 'n3')).toBe(0)
  })
  it('takes whole minutes in range, or blank for none', () => {
    expect(parseMinutes('')).toBeNull()
    expect(parseMinutes(' 15 ')).toBe(15)
    expect(parseMinutes('0')).toBe('bad')
    expect(parseMinutes('2.5')).toBe('bad')
    expect(parseMinutes('241')).toBe('bad')
  })
})
