import { describe, expect, it } from 'vitest'
import { objectLink, parsePeek, peekOf, peekParam, type Peek } from './peek'
import { setRouteForTests } from './router'

describe('peek', () => {
  it('reads back what it writes', () => {
    const all: Peek[] = [
      { kind: 'node', name: 'worker-1' },
      { kind: 'pod', namespace: 'web', name: 'api-0' },
      { kind: 'service', namespace: 'web', name: 'api' },
      { kind: 'object', type: 'Ingress', namespace: 'web', name: 'shop' },
      { kind: 'policy', key: 'np:web/deny' },
    ]
    for (const p of all) expect(parsePeek(peekParam(p))).toEqual(p)
    expect(parsePeek('pod:web')).toBeNull()
    expect(parsePeek('object:Ingress/web')).toBeNull()
    expect(parsePeek('nothing')).toBeNull()
  })

  it('opens one object in place and goes to the tab for several', () => {
    expect(peekOf('nodes', [], { open: 'w1' })).toEqual({ kind: 'node', name: 'w1' })
    expect(peekOf('namespaces', ['web'], { open: 'Pod/api-0' })).toEqual({ kind: 'pod', namespace: 'web', name: 'api-0' })
    expect(peekOf('namespaces', ['web'], { open: 'Secret/tls' })).toEqual({ kind: 'object', type: 'Secret', namespace: 'web', name: 'tls' })
    expect(peekOf('pods', [], { node: 'w1' })).toBeNull()
    setRouteForTests('#/fleets?fleet=gpu')
    expect(objectLink('nodes', [], { open: 'w1' })).toEqual({ href: '#/fleets?fleet=gpu&peek=node%3Aw1', 'data-peek': 'node:w1' })
    expect(objectLink('pods', [], { node: 'w1' })).toEqual({ href: '#/pods?node=w1' })
  })
})
