import { describe, expect, it } from 'vitest'
import { apiPath, clusterHref, clusterPageHref, href, listParam, parseHash, setRouteForTests, TABS, withParams } from './router'

describe('routes', () => {
  it('parses the tab, path and parameters', () => {
    expect(parseHash('')).toMatchObject({ view: 'home', path: [] })
    expect(parseHash('#/')).toMatchObject({ view: 'home' })
    expect(parseHash('#/home')).toMatchObject({ view: 'home' })
    expect(parseHash('#/nodes')).toMatchObject({ view: 'tabs', tab: 'nodes' })
    expect(parseHash('#/pods?node=a,b&ns=web')).toMatchObject({ tab: 'pods', path: [] })
    expect(listParam(parseHash('#/pods?node=a,b').params, 'node')).toEqual(['a', 'b'])
    expect(parseHash('#/namespaces/kube-system?kinds=Pod').path).toEqual(['kube-system'])
    expect(parseHash('#/helm/web/shop').path).toEqual(['web', 'shop'])
    expect(parseHash('#/bogus/x')).toMatchObject({ view: 'tabs', tab: 'alerts' })
    expect(parseHash('#/helm/a%2Fb').path).toEqual(['a/b'])
  })

  it('builds links, leaving empty parameters out', () => {
    expect(href('pods', [], { node: 'worker-1', ns: [] })).toBe('#/pods?node=worker-1')
    expect(href('pods', [], { ns: ['a', 'b'], q: '' })).toBe('#/pods?ns=a%2Cb')
    expect(href('helm', ['web', 'shop'])).toBe('#/helm/web/shop')
    expect(href('nodes')).toBe('#/nodes')
  })

  it('puts Alerts first, and keeps the home page’s parameters on it', () => {
    expect(TABS[0]!.id).toBe('alerts')
    expect(withParams(parseHash('#/'), { x: '1' })).toBe('#/?x=1')
  })
})

describe('clusters in routes', () => {
  it('reads the cluster prefix and the cluster page', () => {
    expect(parseHash('#/pods')).toMatchObject({ cluster: 'local', view: 'tabs', tab: 'pods' })
    expect(parseHash('#/c/analytics-dev/pods?ns=web')).toMatchObject({ cluster: 'analytics-dev', view: 'tabs', tab: 'pods' })
    expect(parseHash('#/c/analytics-dev/namespaces/web').path).toEqual(['web'])
    expect(parseHash('#/c/analytics-dev/cluster')).toMatchObject({ cluster: 'analytics-dev', view: 'cluster' })
    expect(parseHash('#/cluster')).toMatchObject({ cluster: 'local', view: 'cluster' })
    expect(parseHash('#/c/')).toMatchObject({ cluster: 'local', view: 'tabs', tab: 'alerts' })
    expect(parseHash('#/c/dev')).toMatchObject({ cluster: 'dev', view: 'tabs', tab: 'alerts' })
    expect(parseHash('#/c/a%2Fb/nodes').cluster).toBe('a/b')
  })

  it('builds links in the cluster open now, and in another', () => {
    expect(clusterHref('local', 'pods', [], { ns: 'web' })).toBe('#/pods?ns=web')
    expect(clusterHref('analytics-dev', 'helm', ['web', 'shop'])).toBe('#/c/analytics-dev/helm/web/shop')
    expect(clusterPageHref('local')).toBe('#/cluster')
    expect(clusterPageHref('analytics-dev')).toBe('#/c/analytics-dev/cluster')
    expect(apiPath('pods/web/api', 'local')).toBe('/api/v1/pods/web/api')
    expect(apiPath('events', 'analytics-dev')).toBe('/api/v1/clusters/analytics-dev/events')
    setRouteForTests('#/c/analytics-dev/pods')
    try {
      expect(href('nodes', ['cp-1'])).toBe('#/c/analytics-dev/nodes/cp-1')
      expect(apiPath('snapshot')).toBe('/api/v1/clusters/analytics-dev/snapshot')
      expect(withParams(parseHash('#/c/analytics-dev/pods?ns=web'), { status: 'Running' })).toBe('#/c/analytics-dev/pods?ns=web&status=Running')
    } finally {
      setRouteForTests('')
    }
    expect(href('nodes')).toBe('#/nodes')
  })
})
