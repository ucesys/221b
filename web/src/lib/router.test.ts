import { describe, expect, it } from 'vitest'
import { adminHref, apiPath, casesHref, clusterHref, clusterPageHref, href, listParam, parseHash, setRouteForTests, TABS, withParams } from './router'
import { casesItemOf } from './casesItem'

describe('routes', () => {
  it('parses the tab, path and parameters', () => {
    expect(parseHash('')).toMatchObject({ view: 'home', path: [] })
    // A malformed escape, as in a link Sherlock wrote, is kept as written.
    expect(parseHash('#/namespaces/50%').path).toEqual(['50%'])
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

describe('the Administration area', () => {
  it('reads its pages, with or without a cluster', () => {
    expect(parseHash('#/admin')).toMatchObject({ view: 'admin', cluster: 'local', path: [] })
    expect(parseHash('#/admin/roles/admin')).toMatchObject({ view: 'admin', path: ['roles', 'admin'] })
    expect(parseHash('#/c/dev/admin/permissions')).toMatchObject({ view: 'admin', cluster: 'dev', path: ['permissions'] })
    expect(parseHash('#/admin/settings/watson').path).toEqual(['settings', 'watson'])
  })

  it('sends an old settings link to the page under Administration (RB-47)', () => {
    expect(parseHash('#/settings')).toMatchObject({ view: 'admin', path: ['settings'] })
    expect(parseHash('#/settings/sherlock')).toMatchObject({ view: 'admin', path: ['settings', 'sherlock'] })
  })

  it('sends an old Sessions or Audit log link to Logs', () => {
    expect(parseHash('#/admin/sessions').path).toEqual(['logs', 'sessions'])
    expect(parseHash('#/c/dev/admin/audit')).toMatchObject({ cluster: 'dev', path: ['logs', 'audit'] })
  })

  it('builds links and keeps the page when a parameter changes', () => {
    expect(adminHref('roles')).toBe('#/admin/roles')
    expect(adminHref('users', ['bob'])).toBe('#/admin/users/bob')
    expect(adminHref('permissions', [], 'dev')).toBe('#/c/dev/admin/permissions')
    expect(adminHref('users', [], 'local', { q: 'ldap' })).toBe('#/admin/users?q=ldap')
    expect(withParams(parseHash('#/admin/users/bob?q=a'), { q: 'b' })).toBe('#/admin/users/bob?q=b')
    expect(withParams(parseHash('#/c/dev/admin/roles'), { x: '1' })).toBe('#/c/dev/admin/roles?x=1')
  })
})

describe("renamed Automation pages", () => {
  it("open from the names a saved link still uses", () => {
    expect(parseHash("#/parameters?fleet=web").tab).toBe("variables");
    expect(parseHash("#/parameters?fleet=web").params.get("fleet")).toBe("web");
    expect(parseHash("#/configurations").tab).toBe("run-profiles");
    expect(parseHash("#/c/dev/executions").tab).toBe("run-history");
    expect(parseHash("#/c/dev/executions").cluster).toBe("dev");
    expect(parseHash("#/automation/params").tab).toBe("variables");
  });
});

describe('the Cases area (CS-01, CS-06)', () => {
  it('reads its tabs and what follows them', () => {
    expect(parseHash('#/cases')).toMatchObject({ view: 'cases', cluster: 'local', path: ['inbox'] })
    expect(parseHash('#/cases/inbox?kind=reports')).toMatchObject({ view: 'cases', path: ['inbox'] })
    expect(parseHash('#/cases/reports/12')).toMatchObject({ view: 'cases', path: ['inbox', 'report', '12'] })
    expect(parseHash('#/cases/cases/7')).toMatchObject({ view: 'cases', path: ['inbox', 'case', '7'] })
    // No carry-over marks since wave 6: the address is the case's own number.
    expect(parseHash('#/cases/cases/7').params.get('was')).toBeNull()
    expect(parseHash('#/cases/cases')).toMatchObject({ view: 'cases', path: ['inbox'] })
    expect(parseHash('#/cases/known/KI-3')).toMatchObject({ view: 'cases', path: ['mycroft', 'known', 'KI-3'] })
    expect(parseHash('#/cases/knowledge/apps/shop')).toMatchObject({ path: ['mycroft', 'wiki', 'apps', 'shop'] })
    // Mycroft's own tabs (Phase 12, wave 5, MY-01): exceptions left the wiki.
    expect(parseHash('#/cases/knowledge/exceptions/EX-3')).toMatchObject({ view: 'cases', path: ['mycroft', 'exceptions', 'EX-3'] })
    // Add moved to Settings › Knowledge (MY-11).
    expect(parseHash('#/cases/mycroft/wiki/add')).toMatchObject({ view: 'admin', path: ['settings', 'knowledge', 'added'] })
    expect(parseHash('#/c/dev/cases/runs/run-4')).toMatchObject({ view: 'cases', cluster: 'dev', path: ['jobs', 'runs', 'run-4'] })
    expect(parseHash('#/cases/bogus')).toMatchObject({ view: 'cases', path: ['inbox'] })
  })

  it('opens the old RCAs addresses on the Inbox showing all, closed cases too: an RCA is part of its case (wave 6e)', () => {
    for (const hash of ['#/cases/mycroft/rcas', '#/cases/mycroft/rcas/RCA-4', '#/cases/knowledge/rcas', '#/cases/knowledge/rcas/RCA-4', '#/c/dev/cases/mycroft/rcas/RCA-4?q=disk']) {
      const r = parseHash(hash)
      expect(r, hash).toMatchObject({ view: 'cases', path: ['inbox'] })
      expect(r.params.get('show'), hash).toBe('all')
    }
    // The cluster stays; an old RCA's id opens nothing, and what else the address asked is kept.
    expect(parseHash('#/c/dev/cases/mycroft/rcas/RCA-4?q=disk')).toMatchObject({ cluster: 'dev' })
    expect(parseHash('#/c/dev/cases/mycroft/rcas/RCA-4?q=disk').params.get('q')).toBe('disk')
    expect(casesItemOf(parseHash('#/cases/mycroft/rcas/RCA-4'))).toBeNull()
  })

  it('opens the old Mycroft › Validation rules addresses in Jobs › Validation rules (CF-89)', () => {
    expect(parseHash('#/cases/mycroft/rules')).toMatchObject({ view: 'cases', path: ['jobs', 'rules'] })
    expect(parseHash('#/cases/mycroft/rules/pod.privileged')).toMatchObject({ view: 'cases', path: ['jobs', 'rules', 'pod.privileged'] })
    const r = parseHash('#/c/dev/cases/mycroft/rules/pod.privileged/findings?q=x')
    expect(r).toMatchObject({ view: 'cases', cluster: 'dev', path: ['jobs', 'rules', 'pod.privileged', 'findings'] })
    // Its address in Jobs, which the page shows once it has opened it.
    expect(withParams(r, {})).toBe('#/c/dev/cases/jobs/rules/pod.privileged/findings?q=x')
    expect(parseHash('#/cases/jobs/rules/pod.privileged')).toMatchObject({ view: 'cases', path: ['jobs', 'rules', 'pod.privileged'] })
  })

  it('opens the old Administration › Approvals link on the Inbox, filtered to approvals', () => {
    const r = parseHash('#/admin/approvals')
    expect(r).toMatchObject({ view: 'cases', path: ['inbox'] })
    expect(r.params.get('kind')).toBe('approvals')
    expect(parseHash('#/c/dev/admin/approvals')).toMatchObject({ view: 'cases', cluster: 'dev' })
  })

  it('builds its links in the cluster open now, and keeps parameters', () => {
    setRouteForTests('#/')
    expect(casesHref()).toBe('#/cases/inbox')
    expect(casesHref('reports', ['12'], { open: 'true' })).toBe('#/cases/reports/12?open=true')
    setRouteForTests('#/c/dev/pods')
    expect(casesHref('known')).toBe('#/c/dev/cases/known')
    expect(withParams(parseHash('#/cases/mycroft/known?open=true'), { q: 'pay' })).toBe('#/cases/mycroft/known?open=true&q=pay')
    setRouteForTests('#/')
  })
})
