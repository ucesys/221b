import { describe, expect, it } from 'vitest'
import { isCommands, mergeItem, splitCommands, watsonSupports, type WatsonItem } from './watson'

describe('commands in a code block', () => {
  it('splits lines, joins continuations and drops comments', () => {
    expect(splitCommands('# first\nkubectl cordon node-1\nkubectl drain node-1 \\\n  --ignore-daemonsets  # then drain\n\nhelm list -A')).toEqual(['kubectl cordon node-1', 'kubectl drain node-1 --ignore-daemonsets', 'helm list -A'])
    expect(splitCommands("kubectl annotate pod x note='a # b'")).toEqual(["kubectl annotate pod x note='a # b'"])
  })
  it('knows a block of kubectl and helm commands from other code', () => {
    expect(isCommands('kubectl get pods\nhelm list')).toBe(true)
    expect(isCommands('apiVersion: v1\nkind: Pod')).toBe(false)
    expect(isCommands('')).toBe(false)
  })
})

describe('what Watson runs', () => {
  it('accepts its reads and changes, with flags before the verb', () => {
    for (const c of ['kubectl get pods -A', 'kubectl -n web describe pod api-0', 'kubectl auth can-i list pods', 'kubectl rollout undo deploy/api -n web', 'kubectl delete pod api-0', 'helm get values shop -n web', 'helm -n web rollback shop 3', 'kubectl label namespace web a=b'])
      expect(watsonSupports(c), c).toBe(true)
  })
  it('refuses the rest', () => {
    for (const c of ['kubectl apply -f x.yaml', 'kubectl delete deployment api', 'kubectl exec -it api -- sh', 'kubectl rollout status deploy/api', 'helm install x y', 'ls -la'])
      expect(watsonSupports(c), c).toBe(false)
  })
})

describe('merging an update', () => {
  const base = { command: 'kubectl get pods', kind: 'read', from: 'Typed', status: 'running', by: 'Ada', mine: true, exit: 0, canApprove: false, rev: 1 } as const
  it('appends the new lines after the ones already here', () => {
    const a: WatsonItem = { ...base, id: 'a', created: '2026-10-01T10:00:00Z', lines: [{ k: 'out', t: 'one' }], lineStart: 0 }
    const next: WatsonItem = { ...base, id: 'a', created: a.created, status: 'done', lines: [{ k: 'out', t: 'two' }], lineStart: 1, rev: 2 }
    const out = mergeItem([a], next)
    expect(out[0]!.lines!.map((l) => l.t)).toEqual(['one', 'two'])
    expect(out[0]!.status).toBe('done')
  })
  it('adds a new command in the order it was made', () => {
    const b: WatsonItem = { ...base, id: 'b', created: '2026-10-01T10:00:02Z', lineStart: 0 }
    const a: WatsonItem = { ...base, id: 'a', created: '2026-10-01T10:00:01Z', lineStart: 0 }
    expect(mergeItem([b], a).map((x) => x.id)).toEqual(['a', 'b'])
  })
})
