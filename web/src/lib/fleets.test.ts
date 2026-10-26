import { describe, expect, it } from 'vitest'
import { fixCommand, fleetOf, has, joinCommands, joinNotes, labelValueOf, leaveCommands, leaveNotes, missing, newId, nextColor, ownLabels, parseReq, reqText, withLabel } from './fleets'
import type { Fleet, FleetRow } from '../api/types'

const fleet = (id: string, color = 0): Fleet => ({ id, name: id, color, labels: [], taints: [] })
const gpu = { labels: ['pool=gpu'], taints: ['nvidia.com/gpu=true:NoSchedule'] }

describe('fleets', () => {
  it('takes a node with every label and taint, and nothing less', () => {
    const taint = { key: 'nvidia.com/gpu', value: 'true', effect: 'NoSchedule' }
    expect(has(gpu, { labels: { pool: 'gpu', zone: 'a' }, taints: [taint] })).toBe(true)
    expect(has(gpu, { labels: { pool: 'gpu' }, taints: [] })).toBe(false)
    expect(has(gpu, { labels: { pool: 'cpu' }, taints: [taint] })).toBe(false)
    // "key=" asks for the key with an empty value, not for any value.
    expect(has({ labels: ['edge='], taints: [] }, { labels: { edge: '' }, taints: [] })).toBe(true)
    expect(has({ labels: ['edge='], taints: [] }, { labels: { edge: 'x' }, taints: [] })).toBe(false)
  })

  it('says what a node lacks and the command that would give it', () => {
    const m = missing(gpu, { labels: { pool: 'cpu' }, taints: [] })
    expect(m.map((x) => x.id)).toEqual(['pool=gpu', 'nvidia.com/gpu=true:NoSchedule'])
    expect(m[0]!.conflict).toBe('cpu')
    expect(fixCommand('w1', m[0]!)).toBe('kubectl label nodes w1 pool=gpu --overwrite')
    expect(fixCommand('w1', m[1]!)).toBe('kubectl taint nodes w1 nvidia.com/gpu=true:NoSchedule --overwrite')
    expect(fixCommand('w1', missing({ labels: ['pool=gpu'], taints: [] }, { labels: {}, taints: [] })[0]!)).toBe('kubectl label nodes w1 pool=gpu')
  })

  it('takes a list’s nodes by name, whatever their labels', () => {
    const list: Fleet = { id: 'batch', name: 'Batch', color: 0, labels: [], taints: [], kind: 'nodes', nodes: ['w1', 'w3'] }
    expect(has(list, { name: 'w1', labels: {}, taints: [] })).toBe(true)
    expect(has(list, { name: 'w2', labels: { pool: 'gpu' }, taints: [] })).toBe(false)
    expect(reqText(list)).toBe('a list of 2 nodes')
    const row: FleetRow = { key: 'fleet:batch', kind: 'fleet', id: 'batch', name: 'Batch', color: 0, labels: [], taints: [], by: 'nodes', listed: ['w1', 'gone'], order: 0, members: ['w1'], stats: { nodes: 1, cpu: 0, memory: 0, cpuReq: 0, memoryReq: 0, pods: 0, cordoned: 0, errors: 0, warnings: 0 } }
    expect(fleetOf(row)).toMatchObject({ kind: 'nodes', nodes: ['w1', 'gone'] })
  })

  it('brings picked nodes into a fleet and out of it with one command per label and taint', () => {
    const t = { key: 'nvidia.com/gpu', value: 'true', effect: 'NoSchedule' }
    const w1 = { name: 'w1', labels: { pool: 'cpu' }, taints: [] }
    const w2 = { name: 'w2', labels: {}, taints: [] }
    const w3 = { name: 'w3', labels: { pool: 'gpu' }, taints: [t] }
    expect(joinCommands(gpu, [w1, w2, w3])).toEqual(['kubectl label nodes w1 w2 pool=gpu --overwrite', 'kubectl taint nodes w1 w2 nvidia.com/gpu=true:NoSchedule'])
    expect(joinCommands(gpu, [w3])).toEqual([])
    // Another value of the same taint is replaced.
    expect(joinCommands(gpu, [{ name: 'w4', labels: { pool: 'gpu' }, taints: [{ ...t, value: 'false' }] }])).toEqual(['kubectl taint nodes w4 nvidia.com/gpu=true:NoSchedule --overwrite'])
    expect(leaveCommands(gpu, [w1, w3])).toEqual(['kubectl label nodes w3 pool-', 'kubectl taint nodes w3 nvidia.com/gpu:NoSchedule-'])
  })

  it('says what else changes: fleets the nodes leave, taints that keep pods off', () => {
    const row = (id: string, labels: string[], members: string[]): FleetRow => ({ key: id, kind: 'fleet', id, name: id, color: 0, labels, taints: [], order: 0, members, stats: { nodes: 0, cpu: 0, memory: 0, cpuReq: 0, memoryReq: 0, pods: 0, cordoned: 0, errors: 0, warnings: 0 } })
    const rows = [row('gpu', ['pool=gpu'], []), row('cpu', ['pool=cpu'], ['w1']), row('big', ['pool=gpu', 'size=l'], ['w3'])]
    const notes = joinNotes({ id: 'gpu', ...gpu }, [{ name: 'w1', labels: { pool: 'cpu' }, taints: [] }], rows)
    expect(notes[0]).toBe('w1: pool changes from cpu to gpu, so it leaves cpu.')
    expect(notes[1]).toContain('new pods that do not tolerate it are not scheduled')
    expect(leaveNotes({ id: 'gpu', ...gpu }, [{ name: 'w3' }], rows)[0]).toBe('big needs some of the same labels or taints, so the node leaves it too.')
    expect(leaveNotes({ id: 'cp', labels: ['node-role.kubernetes.io/control-plane='], taints: [] }, [{ name: 'w3' }], rows)[0]).toContain('the kubelet can put it back')
  })

  it('finds the labels only the picked nodes have, and names a label value after a fleet', () => {
    const all = [
      { name: 'a', labels: { pool: 'db', zone: '1', 'kubernetes.io/hostname': 'a' } },
      { name: 'b', labels: { pool: 'db', zone: '1', 'kubernetes.io/hostname': 'b' } },
      { name: 'c', labels: { pool: 'web', zone: '1', 'kubernetes.io/hostname': 'c' } },
    ]
    expect(ownLabels(all.slice(0, 2), all)).toEqual(['pool=db'])
    // One node: its hostname is its own, after any other.
    expect(ownLabels([all[2]!], all)).toEqual(['pool=web', 'kubernetes.io/hostname=c'])
    expect(ownLabels(all, all)).toEqual(['zone=1'])
    expect(labelValueOf('GPU nodes (A100)!')).toBe('gpu-nodes-a100')
    expect(labelValueOf('***')).toBe('fleet')
  })

  it('reads typed requirements', () => {
    expect(parseReq('pool=gpu')).toEqual({ label: 'pool=gpu' })
    expect(parseReq(' edge ')).toEqual({ label: 'edge=' })
    expect(parseReq('nvidia.com/gpu=true:NoSchedule')).toEqual({ taint: 'nvidia.com/gpu=true:NoSchedule' })
    expect(parseReq('dedicated:NoExecute')).toEqual({ taint: 'dedicated:NoExecute' })
    expect(parseReq('=gpu').error).toBeTruthy()
    expect(parseReq('pool=g pu').error).toBeTruthy()
    expect(parseReq('pool=gpu:Sometimes').error).toMatch(/NoSchedule, PreferNoSchedule or NoExecute/)
    expect(parseReq('-pool=gpu').error).toMatch(/valid label or taint key/)
  })

  it('keeps one value per label key', () => {
    expect(withLabel(['pool=cpu', 'zone=a'], 'pool=gpu')).toEqual(['zone=a', 'pool=gpu'])
  })

  it('names new fleets uniquely and picks unused colours', () => {
    expect(newId('GPU workers!', [])).toBe('gpu-workers')
    expect(newId('GPU workers', [fleet('gpu-workers')])).toBe('gpu-workers-2')
    expect(newId('***', [])).toBe('fleet')
    expect(nextColor([fleet('a', 0), fleet('b', 1)])).toBe(2)
    expect(reqText(gpu)).toContain('taint nvidia.com/gpu=true:NoSchedule')
  })
})
