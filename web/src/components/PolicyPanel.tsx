import { useState, type ReactNode } from 'react'
import { fmtAge, fmtTs, nWord } from '../lib/format'
import { shortReasons, type PolicyModel } from '../lib/policies'
import { href } from '../lib/router'
import { Card, Drawer, Facts } from './Drawer'
import { WarnIcon } from './Icons'
import { YamlView } from './YamlView'

interface Props {
  keyOf: string
  m: PolicyModel
  now: number
  onClose: () => void
  onToast: (msg: string) => void
}

function List({ items, empty }: { items: { main: string; sub?: string; tone?: string; status?: string; href?: string }[]; empty: string }) {
  if (!items.length) return <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>{empty}</p>
  return (
    <div>
      {items.map((it) => (
        <div key={it.main} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '8px 0', borderTop: '1px solid var(--line)' }} data-testid="panel-item">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0, flex: '1 1 auto' }}>
            <span className="rk-mono" style={{ fontSize: 12.5, overflowWrap: 'anywhere' }}>
              {it.main}
            </span>
            {it.sub && <span style={{ fontSize: 11.5, color: 'var(--fg3)', overflowWrap: 'anywhere' }}>{it.sub}</span>}
          </div>
          {it.status && (
            <span className="rk-pill" data-tone={it.tone}>
              <span className="rk-dot" />
              {it.status}
            </span>
          )}
          {it.href && (
            <a className="rk-link" href={it.href} style={{ fontSize: 12.5, textDecoration: 'none', whiteSpace: 'nowrap' }}>
              Open pod
            </a>
          )}
        </div>
      ))}
    </div>
  )
}

/** The drawer of a policy object (P4-04 to P4-06): what it says, what it applies to, and its YAML. */
export function PolicyPanel({ keyOf, m, now, onClose, onToast }: Props) {
  const [tab, setTab] = useState<'overview' | 'yaml'>('overview')
  const i = keyOf.indexOf(':')
  const prefix = keyOf.slice(0, i)
  const rest = keyOf.slice(i + 1)
  const [ns = '', name = ''] = rest.includes('/') ? rest.split('/') : ['', rest]
  let kind = ''
  let yaml: { kind: string; namespace?: string; name: string } | null = null
  let title = name
  let facts: ReactNode = null
  let body: ReactNode = null
  let gone = false
  let namespaced = true

  if (prefix === 'np') {
    kind = 'NetworkPolicy'
    yaml = { kind: 'networkpolicies', namespace: ns, name }
    const r = m.netpols.find((x) => x.key === keyOf)
    gone = !r
    if (r) {
      facts = <span className="rk-pill" data-tone="ok"><span className="rk-dot" />{nWord(r.pods.length, 'pod selected', 'pods selected')}</span>
      body = (
        <>
          <Card title="NetworkPolicy">
            <Facts items={[['Applies to', r.selector === 'every pod' ? `every pod in ${r.namespace}` : r.selector], ['Restricts', [r.ingress && 'Ingress', r.egress && 'Egress'].filter(Boolean).join(' and ') + (!r.egress ? ' (egress isn’t restricted)' : '')], ...(r.allows ? ([['Allows in', r.allows]] as [string, string][]) : []), ...(r.allowsOut ? ([['Allows out', r.allowsOut]] as [string, string][]) : [])]} />
          </Card>
          <Card title="Pods it selects">
            <List items={r.pods.map((p) => ({ main: p, href: href('pods', [], { open: `${r.namespace}/${p}` }) }))} empty="No running pod has these labels." />
          </Card>
        </>
      )
    }
  } else if (prefix === 'pdb') {
    kind = 'PodDisruptionBudget'
    yaml = { kind: 'poddisruptionbudgets', namespace: ns, name }
    const r = m.pdbs.find((x) => x.key === keyOf)
    gone = !r
    if (r) {
      facts = <span className="rk-pill" data-tone={r.disruptionsAllowed > 0 ? 'ok' : 'warn'}><span className="rk-dot" />{nWord(r.disruptionsAllowed, 'disruption allowed', 'disruptions allowed')}</span>
      const nodes = [...new Set(r.pods.map((p) => p.node).filter(Boolean))]
      body = (
        <>
          {r.overlap.length > 0 && (
            <div className="rk-callout" data-tone="warn">
              <WarnIcon />
              <span style={{ fontSize: 12.5 }}>{nWord(r.overlap.length, 'pod has', 'pods have')} another budget too, so the eviction API refuses {r.overlap.length === 1 ? 'it' : 'them'}: {r.overlap.join(', ')}.</span>
            </div>
          )}
          <Card title="PodDisruptionBudget">
            <Facts items={[['Selector', r.selector], ['Rule', r.rule], ['Current healthy', String(r.currentHealthy)], ['Desired healthy', String(r.desiredHealthy)], ['Expected pods', String(r.expectedPods)], ['Disruptions allowed', String(r.disruptionsAllowed)], ['Unhealthy pods', r.unhealthyPolicy === 'AlwaysAllow' ? 'always evictable' : 'evictable while the budget is healthy'], ...(nodes.length ? ([['Constrains the drain of', nodes.join(', ')]] as [string, string][]) : [])]} />
          </Card>
          <Card title="Pods it selects">
            <List items={r.pods.map((p) => ({ main: p.name, sub: p.node ?? 'no node', status: p.ready ? 'ready' : 'not ready', tone: p.ready ? 'ok' : 'warn', href: href('pods', [], { open: `${r.namespace}/${p.name}` }) }))} empty="It selects no running pod." />
          </Card>
        </>
      )
    }
  } else if (prefix === 'quota') {
    kind = 'ResourceQuota'
    yaml = { kind: 'resourcequotas', namespace: ns, name }
    const r = m.quotas.find((x) => x.key === keyOf)
    gone = !r
    if (r) {
      const worst = Math.max(0, ...r.items.map((x) => x.pct))
      const stalls = m.rollouts.filter((x) => x.namespace === r.namespace && x.stalls && x.short.some((s) => s.quota === r.name))
      facts = <span className="rk-pill" data-tone={worst >= 90 || stalls.length ? 'warn' : 'ok'}><span className="rk-dot" />up to {Math.round(worst)}% used</span>
      body = (
        <>
          {stalls.map((s) => (
            <div key={s.key} className="rk-callout" data-tone="warn">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>A rolling update of {s.name} would stall here</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>Its surge {s.surge === 1 ? 'pod needs' : 'pods need'} {s.short.filter((x) => x.quota === r.name).map((x) => `${x.need} of ${x.resource} (${x.left} left)`).join(', ')}.</span>
              </div>
            </div>
          ))}
          <Card title="ResourceQuota">
            <Facts items={[...r.items.map((x): [string, string] => [x.resource, `${x.used} used of ${x.hard}${x.hardValue ? ` (${x.pct}%)` : ''}`]), ...(r.scopes.length ? ([['Scopes', r.scopes.join(', ')]] as [string, string][]) : [])]} />
          </Card>
        </>
      )
    }
  } else if (prefix === 'limits') {
    kind = 'LimitRange'
    yaml = { kind: 'limitranges', namespace: ns, name }
    const r = m.limits.find((x) => x.key === keyOf)
    gone = !r
    if (r) {
      const t = (x?: Record<string, string>) => (x ? Object.entries(x).map(([k, v]) => `${k} ${v}`).join(' · ') : '—')
      body = (
        <>
          {r.items.map((it, n) => (
            <Card key={n} title={`${it.type} limits`}>
              <Facts items={[['Default request', t(it.defaultRequest)], ['Default limit', t(it.default)], ['Maximum', t(it.max)], ['Minimum', t(it.min)]]} />
            </Card>
          ))}
          <Card title="Containers at the defaults">
            <List items={r.atDefaults.map((x) => ({ main: x }))} empty="No running container has exactly these requests and limits." />
          </Card>
        </>
      )
    }
  } else if (prefix === 'hook') {
    namespaced = false
    const [k = '', config = '', webhook = ''] = rest.split('/')
    kind = `${k}WebhookConfiguration`
    title = config
    yaml = { kind: k === 'Mutating' ? 'mutatingwebhookconfigurations' : 'validatingwebhookconfigurations', name: config }
    const hooks = m.hooks.filter((x) => x.config === config && x.kind === k)
    gone = !hooks.length
    const r = hooks.find((x) => x.webhook === webhook) ?? hooks[0]
    if (r) {
      facts = <span className="rk-pill" data-tone={r.reach.tone}><span className="rk-dot" />{r.reach.text}</span>
      body = (
        <>
          {hooks.map((h) => (
            <Card key={h.key} title={`Webhook ${h.webhook}`}>
              <Facts
                items={[
                  ['Backend', h.service ?? h.url ?? '—'],
                  ['Reachable', h.reach.text],
                  ['Failure policy', h.failurePolicy === 'Fail' ? 'Fail — requests are rejected while the backend can’t answer' : 'Ignore — requests go through unchecked while the backend can’t answer'],
                  ['Timeout', `${h.timeout}s`],
                  ['Rules', h.rules.join('\n')],
                  ['Namespaces', h.namespaces],
                  ['Objects', h.objects],
                  ['Side effects', h.sideEffects ?? '—'],
                  ['CA bundle', h.caBundle ? 'set' : 'not set: the API server must trust the backend’s certificate another way'],
                ]}
              />
              <List items={h.reach.pods.map((p) => ({ main: p.name, sub: p.node, status: p.ready ? 'ready' : 'not ready', tone: p.ready ? 'ok' : 'warn', href: h.serviceNamespace ? href('pods', [], { open: `${h.serviceNamespace}/${p.name}` }) : undefined }))} empty="No pod behind it." />
            </Card>
          ))}
        </>
      )
    }
  } else if (prefix === 'pc') {
    namespaced = false
    kind = 'PriorityClass'
    title = rest
    yaml = { kind: 'priorityclasses', name: rest }
    const r = m.priorities.find((x) => x.key === keyOf)
    gone = !r
    if (r) {
      facts = <span className="rk-pill"><span className="rk-dot" />{nWord(r.pods, 'pod', 'pods')}</span>
      body = (
        <Card title="PriorityClass">
          <Facts items={[['Value', String(r.value)], ['Global default', r.globalDefault ? 'yes' : 'no'], ['Preemption', r.preemption], ['Description', r.description ?? '—'], ['Used by', r.workloads.join(', ') || '—']]} />
        </Card>
      )
    }
  } else if (prefix === 'ns') {
    namespaced = false
    kind = 'Namespace'
    title = rest
    yaml = { kind: 'namespaces', name: rest }
    const r = m.namespaces.find((x) => x.name === rest)
    gone = !r
    if (r) {
      facts = <span className="rk-pill" data-tone={r.enforce === 'restricted' ? 'ok' : r.enforce === 'baseline' ? 'info' : 'muted'}><span className="rk-dot" />Pod Security {r.enforce || 'not enforced'}</span>
      const lv = (l?: string, v?: string) => (l ? l + (v ? `:${v}` : '') : '—')
      body = (
        <>
          <Card title="Pod Security">
            <Facts items={[['Enforce', r.enforce ? lv(r.enforce, r.enforceVersion) : 'none — the cluster’s default applies, usually privileged'], ['Warn', lv(r.warn, r.warnVersion)], ['Audit', lv(r.audit, r.auditVersion)], ['Pods that fail baseline', r.baseline.fail.length ? `${r.baseline.fail.length} of ${r.pods}` : 'none'], ['Pods that fail restricted', r.restricted.fail.length ? `${r.restricted.fail.length} of ${r.pods}` : 'none']]} />
          </Card>
          {(['baseline', 'restricted'] as const).map((lvl) => (
            <Card key={lvl} title={`If it enforced ${lvl}`}>
              {r[lvl].warnings.length ? (
                <>
                  <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
                    The running pods keep running; kubectl label --dry-run=server would warn:
                  </p>
                  <pre className="rk-mono" style={{ margin: 0, padding: '10px 12px', fontSize: 11.5, whiteSpace: 'pre-wrap', background: 'var(--surface2)', border: '1px solid var(--line)', borderRadius: 8 }} data-testid={`psa-warnings-${lvl}`}>
                    {r[lvl].warnings.map((w) => `Warning: ${w}`).join('\n')}
                  </pre>
                  <List items={r[lvl].fail.map((p) => ({ main: p.name, sub: p.detail, href: href('pods', [], { open: `${r.name}/${p.name}` }) }))} empty="" />
                </>
              ) : (
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }} data-testid={`psa-warnings-${lvl}`}>
                  Every pod there passes {lvl}{r.pods ? '' : ' (it has none)'}.
                </p>
              )}
              {r[lvl].fail.length > 0 && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>Reasons: {shortReasons(r[lvl].fail)}</span>}
            </Card>
          ))}
        </>
      )
    }
  }

  return (
    <Drawer
      label={`${kind} details`}
      testId="policy-panel"
      kicker={
        <>
          <span className="rk-tag" style={{ fontSize: 11, padding: '1px 7px' }}>
            {kind}
          </span>
          {namespaced && ns ? (
            <a className="rk-link rk-mono" href={href('namespaces', [ns])} style={{ fontSize: 11.5 }}>
              {ns}
            </a>
          ) : (
            <span>cluster-scoped</span>
          )}
        </>
      }
      title={title}
      facts={
        <>
          {facts}
          {(() => {
            const c = [...m.netpols, ...m.pdbs, ...m.quotas, ...m.limits, ...m.priorities].find((x) => x.key === keyOf)?.created
            return c ? <span title={fmtTs(c)}>Created {fmtAge(c, now)} ago</span> : null
          })()}
        </>
      }
      tabs={[
        { id: 'overview', label: 'Overview' },
        { id: 'yaml', label: 'YAML' },
      ]}
      tab={tab}
      onTab={(t) => setTab(t as 'overview' | 'yaml')}
      onClose={onClose}
      notice={
        gone && (
          <div className="rk-callout" data-tone="warn" style={{ margin: '12px 20px 0' }}>
            <WarnIcon />
            <span style={{ fontSize: 12.5 }}>This {kind || 'object'} isn’t in the cluster any more, or isn’t yours to see.</span>
          </div>
        )
      }
    >
      {tab === 'overview' && <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>{body}</div>}
      {tab === 'yaml' && yaml && <YamlView kind={yaml.kind} namespace={yaml.namespace} name={yaml.name} onToast={onToast} />}
    </Drawer>
  )
}
