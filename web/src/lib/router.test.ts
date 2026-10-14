import { describe, expect, it } from 'vitest'
import { href, listParam, parseHash } from './router'

describe('routes', () => {
  it('parses the tab, path and parameters', () => {
    expect(parseHash('')).toMatchObject({ tab: 'nodes', path: [] })
    expect(parseHash('#/pods?node=a,b&ns=web')).toMatchObject({ tab: 'pods', path: [] })
    expect(listParam(parseHash('#/pods?node=a,b').params, 'node')).toEqual(['a', 'b'])
    expect(parseHash('#/namespaces/kube-system?kinds=Pod').path).toEqual(['kube-system'])
    expect(parseHash('#/helm/web/shop').path).toEqual(['web', 'shop'])
    expect(parseHash('#/bogus/x').tab).toBe('nodes')
    expect(parseHash('#/helm/a%2Fb').path).toEqual(['a/b'])
  })

  it('builds links, leaving empty parameters out', () => {
    expect(href('pods', [], { node: 'worker-1', ns: [] })).toBe('#/pods?node=worker-1')
    expect(href('pods', [], { ns: ['a', 'b'], q: '' })).toBe('#/pods?ns=a%2Cb')
    expect(href('helm', ['web', 'shop'])).toBe('#/helm/web/shop')
    expect(href('nodes')).toBe('#/nodes')
  })
})
