import { describe, expect, it } from 'vitest'
import { ALL_NODES, ceilingText, privilegeText, scriptKind, type AutomationFleet } from '../api/automation'
import { nameProblems, ownValues, variableSaves, scopeRows, scriptsUsing, valueChanges } from './variables'

const node = (name: string) => ({ name, paired: true })
const world = (): AutomationFleet[] => [
  {
    id: ALL_NODES,
    name: 'All nodes',
    nodes: [node('n1'), node('n2'), node('n3')],
    params: {
      values: { PORT: '80', ZONE: 'a' },
      nodes: { n1: { PORT: '81' } },
    },
  },
  {
    id: 'web',
    name: 'web',
    nodes: [node('n1'), node('n2')],
    params: { values: { ZONE: 'b' }, ceiling: 'full', sensitive: ['ZONE'] },
  },
  {
    id: 'db',
    name: 'db',
    nodes: [node('n2'), node('n3')],
    params: { values: { DISK: 'ssd' } },
  },
]

describe('the table of a fleet', () => {
  it('shows what the fleet gets, its own and what it inherits', () => {
    const rows = scopeRows(world(), { fleet: 'web', node: null })
    expect(rows.map((r) => [r.name, r.value, r.from, r.own])).toEqual([
      ['PORT', '80', 'All nodes', false],
      ['ZONE', 'b', 'web', true],
    ])
  })
  it('says which nodes get something else and what it overwrites', () => {
    const rows = scopeRows(world(), { fleet: 'web', node: null })
    expect(rows[0]!.over).toEqual([{ node: 'n1', value: '81', from: 'its own value', own: true }])
    expect(rows[1]!.overwrites).toEqual({ from: 'All nodes', value: 'a' })
    expect(rows[1]!.sensitive).toBe(true)
  })
  it('counts a fleet that beats All nodes as different on its nodes', () => {
    const zone = scopeRows(world(), { fleet: ALL_NODES, node: null }).find((r) => r.name === 'ZONE')!
    expect(zone.over.map((o) => [o.node, o.value, o.from])).toEqual([
      ['n1', 'b', 'web'],
      ['n2', 'b', 'web'],
    ])
  })
  it("names a conflict among a node's fleets", () => {
    const f = world()
    f[2]!.params.values!.ZONE = 'c'
    const zone = scopeRows(f, { fleet: 'web', node: null }).find((r) => r.name === 'ZONE')!
    expect(zone.over).toEqual([{ node: 'n2', from: '', own: false, conflict: ['db', 'web'] }])
  })
})

describe('the table of a node', () => {
  it('shows what it gets and flags what it overwrites', () => {
    const rows = scopeRows(world(), { fleet: ALL_NODES, node: 'n1' })
    expect(rows.map((r) => [r.name, r.value, r.from])).toEqual([
      ['PORT', '81', 'This node'],
      ['ZONE', 'b', 'web'],
    ])
    expect(rows[0]!.overwrites).toEqual({ from: 'All nodes', value: '80' })
    expect(rows[1]!.overwrites).toEqual({ from: 'All nodes', value: 'a' })
  })
  it('counts the values it sets itself', () => {
    expect(ownValues(world(), 'n1')).toBe(1)
    expect(ownValues(world(), 'n3')).toBe(0)
  })
})

describe('saving a parameter', () => {
  const web = { fleet: 'web', node: null }
  it('keeps the rest of the fleet', () => {
    const [s] = variableSaves(world(), web, { name: 'NEW', value: '1' })
    expect(s!.fleet).toBe('web')
    expect(s!.params.values).toEqual({ ZONE: 'b', NEW: '1' })
    expect(s!.params.ceiling).toBe('full')
    expect(s!.params.sensitive).toEqual(['ZONE'])
  })
  it('renames, and the mark goes with it', () => {
    const [s] = variableSaves(world(), web, {
      old: 'ZONE',
      name: 'AREA',
      value: 'b',
      sensitive: true,
    })
    expect(s!.params.values).toEqual({ AREA: 'b' })
    expect(s!.params.sensitive).toEqual(['AREA'])
  })
  it('deletes with no name', () => {
    const [s] = variableSaves(world(), web, { old: 'ZONE' })
    expect(s!.params.values).toEqual({})
    expect(s!.params.sensitive).toEqual([])
  })
  it("puts a node's new value under All nodes", () => {
    const saves = variableSaves(world(), { fleet: ALL_NODES, node: 'n2' }, { name: 'PORT', value: '82' })
    expect(saves.map((s) => s.fleet)).toEqual([ALL_NODES])
    expect(saves[0]!.params.nodes).toEqual({
      n1: { PORT: '81' },
      n2: { PORT: '82' },
    })
  })
  it("changes a node's older value where it is kept, and leaves it in one place", () => {
    const f = world()
    f[1]!.params.nodes = { n1: { PORT: '83' } }
    const saves = variableSaves(f, { fleet: ALL_NODES, node: 'n1' }, { old: 'PORT', name: 'PORT', value: '84' })
    expect(saves.map((s) => [s.fleet, s.params.nodes?.n1])).toEqual([
      [ALL_NODES, { PORT: '84' }],
      ['web', undefined],
    ])
  })
  it("drops a node's entry with its last value", () => {
    const [s] = variableSaves(world(), { fleet: ALL_NODES, node: 'n1' }, { old: 'PORT' })
    expect(s!.params.nodes).toEqual({})
  })
})

describe("the drawer's checks", () => {
  const rows = scopeRows(world(), { fleet: 'web', node: null })
  it('refuses a name already set here, not one only inherited', () => {
    expect(nameProblems(rows, 'ZONE')).toEqual(['ZONE is already set here'])
    expect(nameProblems(rows, 'ZONE', 'ZONE')).toEqual([])
    expect(nameProblems(rows, 'PORT')).toEqual([])
  })
  it('lists the scripts that read a parameter', () => {
    const s = (name: string, params: string[]) => ({ name, params: params.map((p) => ({ name: p, required: true })) }) as never
    expect(scriptsUsing([s('a', ['PORT']), s('b', ['ZONE'])], 'PORT').map((x) => x.script)).toEqual(['a'])
  })
})

describe('what a change does', () => {
  it('lists values, node values, the ceiling and the mark', () => {
    expect(
      valueChanges(
        { values: { A: '1', B: '2' }, sensitive: ['A'], ceiling: 'none' },
        {
          values: { A: '9' },
          nodes: { n1: { C: '3' } },
          sensitive: [],
          ceiling: 'full',
        },
      ),
    ).toEqual([
      { what: 'A', before: '1', after: '9' },
      { what: 'B', before: '2', after: undefined },
      { what: 'C on n1', before: undefined, after: '3' },
      { what: 'Privilege ceiling', before: 'none', after: 'full' },
      { what: 'A: sensitive', before: 'yes', after: 'no' },
    ])
  })
})

describe('the words a person reads', () => {
  it("names privileges and ceilings alike, and a script's kind", () => {
    expect(privilegeText('none')).toBe('nobody')
    expect(privilegeText('full')).toBe('elevated')
    expect(privilegeText('')).toBe('—')
    expect(ceilingText('full')).toBe('elevated')
    expect(ceilingText(undefined)).toBe('nobody')
    expect(scriptKind('')).toBe('Shell')
    expect(scriptKind('kubernetes')).toBe('K8S')
    expect(scriptKind('python')).toBe('Python')
  })
})
