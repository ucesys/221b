import { describe, expect, it } from 'vitest'
import { fixCommand, has, missing, newId, nextColor, parseReq, reqText, withLabel } from './fleets'
import type { Fleet } from '../api/types'

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
