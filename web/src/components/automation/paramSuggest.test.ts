import { describe, expect, it } from 'vitest'
import { bindValue, boundTo, constantOf, escapeConstant, missingParams, resolveParams, type AutomationFleet, type Run } from '../../api/automation'
import { chipDetail, chipTitle, hiddenVariable, paramKind, setInScope, paramTypeText, secretSuggestions, spreadText, uniformValue, variableSpread, variableSuggestions } from './paramSuggest'

// --- FU-03: run parameters typed with suggestions ---

const fleets: AutomationFleet[] = [
  { id: 'workers', name: 'Workers', nodes: [{ name: 'w1', paired: true }], params: { values: { PKG: 'chrony' }, nodes: { w1: { PKG: 'chrony-4' } } } },
  { id: 'db', name: 'Databases', nodes: [{ name: 'd1', paired: true }], params: { values: { PKG: 'ntp', TOKEN: 'secret:ops/ntp/key' } } },
  {
    id: '_all',
    name: 'All nodes',
    nodes: [
      { name: 'w1', paired: true },
      { name: 'd1', paired: true },
    ],
    params: { values: {} },
  },
] as AutomationFleet[]

const run = (id: number, script: string, overrides?: Record<string, string>) => ({ id, script, overrides }) as Run

describe('a run parameter’s kind and suggestions (FU-03)', () => {
  it('says the kind first, then whether it is required, then the script’s words', () => {
    expect(paramKind({})).toBe('variable')
    expect(paramKind({ secret: true })).toBe('secret')
    expect(paramTypeText({ required: true, desc: 'The package' })).toBe('Variable · required · The package')
    expect(paramTypeText({ required: false, secret: true })).toBe('Secret · optional')
  })

  it('searches every variable by name and value, its own first, then this script’s earlier runs, and binds by name (FU-04)', () => {
    const runs = [run(3, 'other', { PKG: 'x' }), run(4, 's', { PKG: 'chrony-5' }), run(9, 's', { PKG: 'var:ZONE' }), run(10, 's', {})]
    const all = variableSuggestions('PKG', fleets, ['w1', 'd1'], runs, 's', '')
    expect(all.map((x) => [x.label ?? x.value, x.detail, x.note])).toEqual([
      ['PKG', 'varies: 2 values', 'fleet Workers, node w1 +1'],
      ['TOKEN', 'value hidden, set on 1 of 2 nodes', 'fleet Databases'],
      ['chrony-5', undefined, 'run #4'],
    ])
    expect(all[0]).toMatchObject({ value: 'var:PKG', bind: 'PKG' })
    // A name, any case.
    expect(variableSuggestions('X', fleets, ['w1'], [], 's', 'pk').map((x) => x.bind)).toEqual(['PKG'])
    // A value finds its variable; a hidden one is never searched by value.
    expect(variableSuggestions('X', fleets, ['w1'], [], 's', 'NTP').map((x) => x.bind)).toEqual(['PKG'])
    expect(variableSuggestions('X', fleets, ['w1'], [], 's', 'ops/ntp')).toEqual([])
    // Typed text an earlier run gave is offered as the text itself.
    expect(variableSuggestions('PKG', fleets, ['w1'], runs, 's', 'chrony-').map((x) => [x.value, x.bind])).toEqual([
      ['var:PKG', 'PKG'],
      ['chrony-5', undefined],
    ])
  })

  it('says what a variable is on the run’s nodes, and fills a box only when every node gets the same (FU-04)', () => {
    expect(uniformValue(fleets, ['w1'], 'PKG')).toBe('chrony-4')
    expect(uniformValue(fleets, ['w1', 'd1'], 'PKG')).toBeUndefined()
    expect(uniformValue(fleets, [], 'PKG')).toBeUndefined()
    expect(uniformValue(fleets, ['w1', 'd1'], 'NONE')).toBeUndefined()
    const spread = variableSpread(fleets, ['w1', 'd1'], 'TOKEN')
    expect(spread).toEqual({ values: ['secret:ops/ntp/key'], set: 1, unset: ['w1'], conflict: [] })
    expect(spreadText(spread, 2, false)).toBe('secret:ops/ntp/key · not set on 1 node')
    expect(spreadText(variableSpread(fleets, ['w1', 'd1'], 'PKG'), 2, false)).toBe('varies: 2 values')
    expect(spreadText(variableSpread(fleets, ['w1'], 'NONE'), 1, false)).toBe('not set on this run’s nodes')
    // Sensitive, named like a secret, or holding a Secret's reference: the name alone.
    const marked = [{ ...fleets[0]!, params: { ...fleets[0]!.params, sensitive: ['PKG'] } }, ...fleets.slice(1)]
    expect(hiddenVariable(marked, 'PKG')).toBe(true)
    expect(hiddenVariable(fleets, 'PKG')).toBe(false)
    expect(hiddenVariable(fleets, 'DB_PASSWORD')).toBe(true)
    expect(hiddenVariable(fleets, 'TOKEN')).toBe(true)
    expect(variableSuggestions('X', marked, ['w1'], [], 's', '')[0]).toMatchObject({ label: 'PKG', detail: 'value hidden' })
  })

  it('resolves a binding per node, and keeps a constant that starts like one as text (FU-04)', () => {
    const params = [{ name: 'TARGET', required: true }]
    expect(resolveParams(params, fleets, 'w1', { TARGET: bindValue('PKG') })[0]).toMatchObject({ value: 'chrony-4', source: 'variable', bound: 'PKG' })
    expect(resolveParams(params, fleets, 'd1', { TARGET: 'var:PKG' })[0]).toMatchObject({ value: 'ntp', bound: 'PKG' })
    // A node without the variable has no value: required, it is missing.
    expect(missingParams(params, fleets, ['w1', 'd1'], { TARGET: 'var:TOKEN' })).toEqual(['w1: TARGET'])
    expect(resolveParams(params, fleets, 'w1', { TARGET: escapeConstant('var:PKG') })[0]).toMatchObject({ value: 'var:PKG', source: 'run' })
    expect(escapeConstant('var:PKG')).toBe('\\var:PKG')
    expect(escapeConstant('plain')).toBe('plain')
    expect(constantOf(escapeConstant('\\var:x'))).toBe('\\var:x')
    expect(boundTo('var:PKG')).toBe('PKG')
    expect(boundTo('var:pkg')).toBeUndefined()
    // A secret's value never binds.
    expect(resolveParams([{ name: 'S', required: false, secret: true }], fleets, 'w1', { S: 'var:PKG' })[0]).toMatchObject({ value: 'var:PKG', source: 'run' })
  })

  it('offers a secret every key of the Secrets by reference, never a value, matched with or without secret:', () => {
    const refs = [
      { namespace: 'web', name: 'db', keys: ['password', 'user'] },
      { namespace: 'ops', name: 'ntp', keys: ['key'] },
    ]
    expect(secretSuggestions('TOKEN', fleets, 'd1', refs, '').map((s) => [s.value, s.note])).toEqual([
      ['secret:ops/ntp/key', 'applies to d1, from Databases'],
      ['secret:web/db/password', 'key password of Secret web/db'],
      ['secret:web/db/user', 'key user of Secret web/db'],
    ])
    expect(secretSuggestions('TOKEN', fleets, '', refs, 'secret:web/db/p').map((s) => s.value)).toEqual(['secret:web/db/password'])
    expect(secretSuggestions('TOKEN', fleets, '', refs, 'USER').map((s) => s.value)).toEqual(['secret:web/db/user'])
    expect(secretSuggestions('TOKEN', fleets, '', refs, 'secret:web/db/user')).toEqual([])
    expect(secretSuggestions('TOKEN', fleets, '', refs, '', 2)).toHaveLength(2)
  })

  it('says what a bound variable’s chip shows: how many values, its one value or none, and each node’s in its tooltip (FU-06)', () => {
    expect(setInScope(fleets, ['w1', 'd1'], 'PKG')).toBe(true)
    expect(setInScope(fleets, ['w1'], 'TOKEN')).toBe(false)
    expect(setInScope(fleets, [], 'PKG')).toBe(false)
    expect(chipDetail(variableSpread(fleets, ['w1', 'd1'], 'PKG'), false)).toBe('2 values')
    expect(chipDetail(variableSpread(fleets, ['w1'], 'PKG'), false)).toBe('chrony-4')
    expect(chipDetail(variableSpread(fleets, ['d1'], 'TOKEN'), true)).toBe('value hidden')
    expect(chipDetail(variableSpread(fleets, ['w1'], 'TOKEN'), true)).toBe('not set')
    expect(chipTitle(fleets, ['w1', 'd1'], 'PKG', false)).toBe('w1: chrony-4\nd1: ntp\nSet on fleet Workers, node w1, fleet Databases.')
    expect(chipTitle(fleets, ['w1', 'd1'], 'TOKEN', true)).toBe('Set on fleet Databases.')
    // A script on the cluster has no node to name: where it is set alone.
    expect(chipTitle(fleets, [''], 'PKG', false)).toBe('Set on fleet Workers, node w1, fleet Databases.')
  })
})
