import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useView } from '../api/live'
import type { NetCheckResult, PodRow, PolicyNSRow, PolicyRow, QuotaRow, ServiceRow } from '../api/types'
import { COVERAGE_COLUMNS, DRAIN_COLUMNS, drainTerms, HOOK_COLUMNS, hookTerms, ISOLATION_COLUMNS, LIMITS_COLUMNS, limitsTerms, NETPOL_COLUMNS, netpolTerms, nsTerms, PDB_COLUMNS, pdbTerms, PRIORITY_COLUMNS, priorityTerms, PSA_COLUMNS, ROLLOUT_COLUMNS, rolloutTerms } from '../columns/policies'
import { fmtAge, nWord } from '../lib/format'
import { appNamespaces, enforced, findings, flagOf, modelOf, openNamespaces, qtyNice, SECTIONS, sectionSub, countsText, type PolicyFinding, type PolicyModel, type Section } from '../lib/policies'
import { listText, SEV_TEXT, SEV_TONE } from '../lib/rbac'
import { apiPath, href, navigate, withParams } from '../lib/router'
import type { ColumnDef, Term } from '../lib/table'
import { useTable } from '../lib/useTable'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { CopyIcon, WarnIcon } from './Icons'
import { PolicyPanel } from './PolicyPanel'
import { Select } from './Select'
import type { TabProps } from './tabs'
import { objectLink } from '../lib/peek'

function copy(text: string, onToast: (m: string) => void, what = 'Copied') {
  void navigator.clipboard?.writeText(text).then(
    () => onToast(what),
    () => onToast('Could not copy: the clipboard is not available.'),
  )
}

/** The Policies tab (P4-04 to P4-06). */
export function PoliciesTab({ cluster, now, onToast, route }: TabProps) {
  const view = useView<PolicyRow>('policies')
  const m = useMemo(() => modelOf(view.rows), [view.rows])
  const fs = useMemo(() => findings(m), [m])
  const sel: Section = SECTIONS.some(([s]) => s === route.path[0]) ? (route.path[0] as Section) : 'overview'
  const set = (changes: Record<string, string | undefined>) => navigate(withParams(route, changes), true)
  const open = route.params.get('open') ?? ''
  const go = (s: Section) => navigate(href('policies', s === 'overview' ? [] : [s]))
  const common = { m, fs, now, set, onToast, cluster: cluster?.name, loaded: view.loaded, route }
  return (
    <>
      <aside className="rk-pane" aria-label="Kinds of policy" data-testid="policies-aside" style={{ flex: '1 1 320px', maxWidth: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)' }}>
        <div style={{ padding: '14px 16px 12px', borderBottom: '1px solid var(--line)' }}>
          <h2 className="rk-h">Policies</h2>
        </div>
        <div style={{ flex: '1 1 auto' }}>
          {SECTIONS.map(([s, name]) => {
            const mine = s === 'overview' ? fs : fs.filter((f) => f.section === s)
            const flag = flagOf(mine)
            return (
              <a key={s} className="rk-rrow" href={href('policies', s === 'overview' ? [] : [s])} data-sel={sel === s} aria-current={sel === s ? 'page' : undefined} data-section={s} style={{ display: 'flex', flexDirection: 'column', gap: 3, padding: '11px 16px', borderBottom: '1px solid var(--line)', textDecoration: 'none', color: 'inherit' }}>
                <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ fontSize: 13, fontWeight: 500 }}>{name}</span>
                  <span style={{ flex: '1 1 auto' }} />
                  {flag.count > 0 && (
                    <span className="rk-t" data-tone={flag.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11.5, fontWeight: 500 }}>
                      <span className="rk-dot" />
                      {flag.count}
                    </span>
                  )}
                </span>
                <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{view.loaded ? sectionSub(s, m, fs) : ''}</span>
              </a>
            )
          })}
        </div>
        <p style={{ margin: 'auto 0 0', padding: '10px 16px 14px', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>Policies decide what may run, connect and be disrupted. 221B reads them and runs nothing; commands are shown to copy.</p>
      </aside>
      <section className="rk-pane" aria-label={SECTIONS.find(([s]) => s === sel)![1]} data-testid="policies-main" style={{ flex: '999 1 640px', minWidth: 0, padding: '20px 24px 32px', display: 'flex', flexDirection: 'column', gap: 16 }}>
        {sel === 'overview' && <Overview {...common} go={go} />}
        {sel === 'netpol' && <NetPolicies {...common} />}
        {sel === 'pdb' && <Budgets {...common} />}
        {sel === 'quota' && <Quotas {...common} />}
        {sel === 'psa' && <PodSecurity {...common} />}
        {sel === 'webhooks' && <Webhooks {...common} />}
        {sel === 'prio' && <Priorities {...common} />}
      </section>
      {open && <PolicyPanel keyOf={open} m={m} now={now} onClose={() => set({ open: undefined })} onToast={onToast} />}
    </>
  )
}

interface SecProps {
  m: PolicyModel
  fs: PolicyFinding[]
  now: number
  set: (c: Record<string, string | undefined>) => void
  onToast: (m: string) => void
  cluster?: string
  loaded: boolean
  route: TabProps['route']
}

function Head({ title, summary, intro }: { title: string; summary: string; intro: ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600 }}>{title}</h1>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="policies-summary">
          {summary}
        </span>
      </div>
      <p style={{ margin: 0, maxWidth: '112ch', fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg2)' }}>{intro}</p>
    </div>
  )
}

function Review({ fs, set, onToast, go, empty = 'Nothing to review.' }: { fs: PolicyFinding[]; set: SecProps['set']; onToast: SecProps['onToast']; go?: (s: Section) => void; empty?: string }) {
  if (!fs.length && !go) return null
  return (
    <section className="rk-card" aria-label="Review" style={{ overflow: 'hidden', flex: 'none' }} data-testid="policies-review">
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
        <h2 className="rk-h">Review</h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{countsText(fs)}</span>
      </div>
      {fs.map((f) => (
        <div key={f.id} className="rk-find" data-sev={f.severity} data-testid="finding" data-finding={f.id}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span className="rk-pill" data-tone={SEV_TONE[f.severity]}>
              <span className="rk-dot" />
              {SEV_TEXT[f.severity]}
            </span>
            <strong style={{ fontSize: 13, fontWeight: 600 }}>{f.title}</strong>
          </div>
          <p style={{ margin: 0, maxWidth: 880, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg2)' }}>{f.text}</p>
          {f.code && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxWidth: 640 }}>
              <div style={{ position: 'relative' }}>
                <pre className="rk-mono" style={{ margin: 0, padding: '10px 12px', fontSize: 12, background: 'var(--surface2)', border: '1px solid var(--line)', borderRadius: 8, overflowX: 'auto' }}>
                  {f.code}
                </pre>
                <button className="rk-btn" onClick={() => copy(f.code!, onToast, 'Copied the YAML')} style={{ position: 'absolute', top: 6, right: 6, height: 24 }}>
                  Copy
                </button>
              </div>
              {f.codeNote && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{f.codeNote}</span>}
            </div>
          )}
          {f.command && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <code className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg2)', overflowWrap: 'anywhere' }}>
                {f.command}
              </code>
              <button className="rk-btn" onClick={() => copy(f.command!, onToast, 'Copied the command')} title="Copy the command; 221B runs nothing" style={{ height: 26 }}>
                <CopyIcon />
                Copy the command
              </button>
            </div>
          )}
          {(f.path.length > 0 || go) && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
              {f.path.map((p, i) =>
                p.open ? (
                  <button key={i} className="rk-chip rk-chipbtn" onClick={() => set({ open: p.open })}>
                    {p.text}
                  </button>
                ) : (
                  <a key={i} className="rk-chip rk-chipbtn" {...(p.link ? objectLink(p.link.tab, p.link.path ?? [], p.link.params ?? {}) : {})} style={{ textDecoration: 'none' }}>
                    {p.text}
                  </a>
                ),
              )}
              {go && (
                <button className="rk-btn" onClick={() => go(f.section)} style={{ height: 26 }}>
                  Open {SECTIONS.find(([s]) => s === f.section)![1].toLowerCase()}
                </button>
              )}
            </div>
          )}
        </div>
      ))}
      {!fs.length && <p style={{ margin: 0, padding: '14px 16px', fontSize: 12.5, color: 'var(--fg3)' }}>{empty}</p>}
    </section>
  )
}

function Table<R>({ title, sub, id, columns, rows, terms, sort, now, onOpen, rowAttr, rowKey, rowLabel, cluster, onToast, empty = 'Nothing to show.', note }: { title: string; sub?: string; id: string; columns: ColumnDef<R>[]; rows: R[]; terms: (r: R) => Term[]; sort: { col: string; dir: 'asc' | 'desc' }; now: number; onOpen?: (key: string) => void; rowAttr: string; rowKey: (r: R) => string; rowLabel: (r: R) => string; cluster?: string; onToast: (m: string) => void; empty?: string; note?: ReactNode }) {
  const t = useTable<R>({ id, columns, rows, terms, now, defaultSort: sort })
  return (
    <section className="rk-card" aria-label={title} style={{ overflow: 'hidden', flex: 'none' }} data-testid={`table-${id}`}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
        <h2 className="rk-h">{title}</h2>
        {sub && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{sub}</span>}
        <span style={{ marginLeft: 'auto' }}>
          <ExportMenu onExport={(f) => t.exportRows(f, cluster, id, onToast)} disabled={!t.shown.length} what="this table" />
        </span>
      </div>
      <DataTable<R> label={title} rows={t.shown} cols={t.cols} rowKey={rowKey} rowLabel={(r) => rowLabel(r)} sort={t.sort} onSort={t.onSort} onOpen={onOpen} now={now} empty={empty} rowAttr={rowAttr} />
      {note && <p style={{ margin: 0, padding: '10px 16px', fontSize: 12, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>{note}</p>}
    </section>
  )
}

function Tiles({ tiles }: { tiles: { count: string | number; label: string; tone: string }[] }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(200px, 100%), 1fr))', gap: 10 }} data-testid="policies-tiles">
      {tiles.map((t) => (
        <div key={t.label} className="rk-card rk-tile" data-tone={t.tone} style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span className="rk-num" style={{ fontSize: 22, fontWeight: 600 }}>
            {t.count}
          </span>
          <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{t.label}</span>
        </div>
      ))}
    </div>
  )
}

function Overview(p: SecProps & { go: (s: Section) => void }) {
  const { m, fs } = p
  const open = openNamespaces(m).length
  const stalls = m.rollouts.filter((r) => r.stalls).length
  const blocked = m.drains.filter((d) => d.verdict === 'blocked').length
  return (
    <>
      <Head title="Policies" summary={countsText(fs)} intro="What may connect to what, what can be disrupted, how much a namespace may use and what may run there, worked out from the cluster’s NetworkPolicies, disruption budgets, quotas, Pod Security labels and admission webhooks." />
      <Tiles
        tiles={[
          { count: open, label: open === 1 ? 'app namespace open to every pod' : 'app namespaces open to every pod', tone: open ? 'warn' : 'muted' },
          { count: stalls, label: stalls === 1 ? 'rollout a quota would stall' : 'rollouts a quota would stall', tone: stalls ? 'warn' : 'muted' },
          { count: blocked, label: blocked === 1 ? 'node that can’t be drained now' : 'nodes that can’t be drained now', tone: blocked ? 'warn' : 'muted' },
          { count: `${m.namespaces.filter(enforced).length}/${m.namespaces.length}`, label: 'namespaces enforce Pod Security baseline or stricter', tone: 'muted' },
        ]}
      />
      <Review fs={fs} set={p.set} onToast={p.onToast} go={p.go} empty={p.loaded ? 'Nothing to review.' : 'Reading the cluster’s policies…'} />
      <Table<PolicyNSRow> title="Coverage by namespace" sub="which policies apply where" id="policy-coverage" columns={COVERAGE_COLUMNS} rows={m.namespaces} terms={nsTerms} sort={{ col: 'np', dir: 'asc' }} now={p.now} rowAttr="ns" rowKey={(r) => r.name} rowLabel={(r) => `Namespace ${r.name}`} cluster={p.cluster} onToast={p.onToast} />
    </>
  )
}

function NetPolicies(p: SecProps) {
  const { m } = p
  const cni = m.cni
  const egress = m.netpols.some((n) => n.egress)
  const enforcedBy = cni?.name ? (cni.enforces === 'yes' ? ` · enforced by ${cni.name}` : cni.enforces === 'no' ? ` · ${cni.name} does not enforce them` : ` · ${cni.name}: enforcement unknown`) : ''
  return (
    <>
      <Head
        title="Network policies"
        summary={sectionSub('netpol', m, p.fs) + enforcedBy}
        intro={`A pod selected by an ingress policy accepts only the connections some policy here allows; a pod no policy selects accepts them from anywhere. ${egress ? 'Pods selected by an egress policy may open only the connections it allows.' : 'No policy restricts egress, so outgoing traffic is never blocked.'}`}
      />
      {cni && cni.enforces !== 'yes' && (
        <div className="rk-callout" data-tone="warn" data-testid="cni-callout">
          <WarnIcon />
          <span style={{ fontSize: 12.5 }}>{cni.note}</span>
        </div>
      )}
      <Checker {...p} />
      <Review fs={p.fs.filter((f) => f.section === 'netpol')} set={p.set} onToast={p.onToast} />
      <Table<PolicyNSRow> title="Isolation by namespace" id="policy-isolation" columns={ISOLATION_COLUMNS} rows={m.namespaces.filter((n) => n.pods > 0)} terms={nsTerms} sort={{ col: 'iso', dir: 'asc' }} now={p.now} onOpen={(k) => navigate(href('namespaces', [k], { kinds: 'NetworkPolicy' }))} rowAttr="ns" rowKey={(r) => r.name} rowLabel={(r) => `${r.name} — show its NetworkPolicies in the Namespaces tab`} cluster={p.cluster} onToast={p.onToast} />
      <Table title="NetworkPolicies" id="policy-netpols" columns={NETPOL_COLUMNS} rows={m.netpols} terms={netpolTerms} sort={{ col: 'ns', dir: 'asc' }} now={p.now} onOpen={(k) => p.set({ open: k })} rowAttr="netpol" rowKey={(r) => r.key} rowLabel={(r) => `NetworkPolicy ${r.namespace}/${r.name} — open details and YAML`} cluster={p.cluster} onToast={p.onToast} empty="No NetworkPolicies in this cluster." />
    </>
  )
}

function parsePort(s: string): { port: number; protocol: string } | null {
  const m = /(\d+)\/(\w+)/.exec(s)
  return m ? { port: Number(m[1]), protocol: m[2]! } : null
}

/** "Can it connect?": from a workload's pod to a Service's pods on a port (P4-04). */
function Checker(p: SecProps) {
  const pods = useView<PodRow>('pods')
  const services = useView<ServiceRow>('services')
  const q = p.route.params
  const workloads = useMemo(() => {
    const out = new Map<string, PodRow>()
    for (const r of pods.rows) {
      if (r.done) continue
      const k = `${r.namespace}/${r.ownerName ?? r.name}`
      const cur = out.get(k)
      if (!cur || (cur.tone !== 'ok' && r.tone === 'ok')) out.set(k, r)
    }
    return [...out.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [pods.rows])
  const svcs = useMemo(() => services.rows.filter((s) => s.selector && s.total > 0 && !s.headless).sort((a, b) => a.key.localeCompare(b.key)), [services.rows])
  const from = q.get('from') ?? workloads[0]?.[0] ?? ''
  const to = q.get('to') ?? svcs[0]?.key ?? ''
  const svc = svcs.find((s) => s.key === to)
  const ports = (svc?.ports ?? []).map(parsePort).filter((x): x is { port: number; protocol: string } => !!x)
  const port = q.get('port') ?? (ports[0] ? String(ports[0].port) : '')
  const fromPod = workloads.find(([k]) => k === from)?.[1]
  const [res, setRes] = useState<{ key: string; data?: NetCheckResult; error?: string } | null>(null)
  const key = `${fromPod?.key}|${to}|${port}`
  useEffect(() => {
    if (!fromPod || !to || !port) return
    let gone = false
    const url = apiPath(`netcheck?from=${encodeURIComponent(fromPod.key)}&toService=${encodeURIComponent(to)}&port=${encodeURIComponent(port)}`)
    fetch(url, { headers: { Accept: 'application/json' } })
      .then(async (r) => {
        const body = (await r.json()) as NetCheckResult & { error?: string }
        if (!gone) setRes(r.ok ? { key, data: body } : { key, error: body.error ?? `The server answered ${r.status}.` })
      })
      .catch(() => !gone && setRes({ key, error: 'The server could not be reached.' }))
    return () => {
      gone = true
    }
  }, [key, fromPod, to, port])
  const r = res?.key === key ? res : null
  return (
    <section className="rk-card" aria-label="Can it connect?" style={{ overflow: 'hidden', flex: 'none' }} data-testid="netcheck">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
        <h2 className="rk-h">Can it connect?</h2>
        <Select label="From workload" value={from} options={workloads.map(([k]) => [k, k])} onChange={(v) => p.set({ from: v })} testId="netcheck-from" width={300} />
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>to</span>
        <Select label="To service" value={to} options={svcs.map((s) => [s.key, s.key])} onChange={(v) => p.set({ to: v, port: undefined })} testId="netcheck-to" width={300} />
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>port</span>
        <Select label="Port" value={port} options={ports.map((x) => [String(x.port), `${x.port}/${x.protocol}`])} onChange={(v) => p.set({ port: v })} testId="netcheck-port" />
      </div>
      <div style={{ padding: '12px 16px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="netcheck-answer">
        {!fromPod || !svc ? <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{pods.loaded && services.loaded ? 'Pick a workload and a Service with pods behind it.' : 'Reading pods and Services…'}</span> : r?.error ? <span className="rk-t" data-tone="err" style={{ fontSize: 12.5 }}>{r.error}</span> : r?.data ? <Answer data={r.data} from={from} fromPod={fromPod} to={to} set={p.set} /> : <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Checking…</span>}
      </div>
    </section>
  )
}

function Answer({ data, from, fromPod, to, set }: { data: NetCheckResult; from: string; fromPod: PodRow; to: string; set: SecProps['set'] }) {
  if (!data.checks.length) return <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>No pod behind {to} has that port.</span>
  const c = data.checks[0]!
  const all = data.allowed === data.checks.length
  const none = data.allowed === 0
  const target = `${data.service?.protocol ?? c.protocol} ${c.port}`
  const toPod = c.to.split('/')[1]
  const verdict = c.toHost ? { text: 'Allowed', tone: 'muted' } : !c.ingress.isolated && !c.egress.isolated ? { text: 'Allowed — no policy', tone: 'info' } : all ? { text: 'Allowed', tone: 'ok' } : none ? { text: 'Denied', tone: 'err' } : { text: `${data.allowed} of ${data.checks.length} pods allowed`, tone: 'warn' }
  const why: string[] = []
  if (c.toHost) why.push(`${toPod} uses its node’s network, so NetworkPolicies don’t apply to it.`)
  const side = (name: 'egress' | 'ingress') => {
    const s = c[name]
    const dir = name === 'egress' ? `Out of ${from}` : `Into ${to}`
    if (!s.isolated) return name === 'egress' ? '' : `No NetworkPolicy selects ${toPod} for incoming connections, so it accepts them from any pod.`
    if (s.allowed) return `${dir}: ${listText(s.allowedBy)} ${s.allowedBy.length === 1 ? 'allows' : 'allow'} it (${Object.values(s.rules ?? {})[0] ?? ''}).${s.selecting.length > s.allowedBy.length ? ` Also selecting: ${listText(s.selecting.filter((x) => !s.allowedBy.includes(x)))}.` : ''}`
    const who = name === 'ingress' ? (c.fromHost ? `traffic from a node’s address (${fromPod.name} uses its node’s network)` : `pods with ${Object.entries(fromPod.labels).filter(([k]) => !k.includes('pod-template-hash') && !k.includes('controller-revision')).map(([k, v]) => `${k}=${v}`).join(', ')} from namespace ${fromPod.namespace}`) : `${to} on ${target}`
    return `${dir}: ${listText(s.selecting)} ${s.selecting.length === 1 ? 'selects' : 'select'} it and none of ${s.selecting.length === 1 ? 'its' : 'their'} rules admits ${who}${name === 'ingress' ? ` on ${target}` : ''}.`
  }
  if (!c.toHost) {
    const e = side('egress')
    if (e) why.push(e)
    why.push(side('ingress'))
  }
  if (!none && !all) why.push(`The verdict differs between the pods behind ${to}: their labels differ.`)
  if (none) why.push('The network plugin drops the packets, so the client usually sees a timeout rather than a refusal.')
  const policies = [...new Set([...c.egress.selecting.map((x) => `${fromPod.namespace}/${x}`), ...c.ingress.selecting.map((x) => `${c.to.split('/')[0]}/${x}`)])]
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone={verdict.tone} data-testid="netcheck-verdict">
          <span className="rk-dot" />
          {verdict.text}
        </span>
        <strong style={{ fontSize: 13, fontWeight: 600 }}>
          {from} {none ? 'can’t open' : 'can open'} {target} on {to}
          {data.service && data.service.targetPort !== String(data.service.port) ? ` (pod port ${c.port})` : ''}
        </strong>
      </div>
      <p style={{ margin: 0, maxWidth: 880, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg2)' }} data-testid="netcheck-why">
        {why.join(' ')}
      </p>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <a className="rk-chip rk-chipbtn" {...objectLink('pods', [], { open: fromPod.key })} style={{ textDecoration: 'none' }}>
          pod {fromPod.key}
        </a>
        {policies.map((pk) => (
          <span key={pk} style={{ display: 'contents' }}>
            <span style={{ color: 'var(--fg3)' }}>→</span>
            <button className="rk-chip rk-chipbtn" onClick={() => set({ open: `np:${pk}` })}>
              NetworkPolicy {pk}
            </button>
          </span>
        ))}
        <span style={{ color: 'var(--fg3)' }}>→</span>
        <a className="rk-chip rk-chipbtn" {...objectLink('services', [], { open: to })} style={{ textDecoration: 'none' }}>
          Service {to}:{data.service?.port}
        </a>
      </div>
    </>
  )
}

function Budgets(p: SecProps) {
  const { m } = p
  return (
    <>
      <Head title="Disruption budgets" summary={sectionSub('pdb', m, p.fs)} intro="A PodDisruptionBudget limits how many of a workload’s pods voluntary evictions (a node drain, an autoscaler) may take down at once. A budget that allows 0 disruptions makes kubectl drain wait; a pod with no controller stops it." />
      <Review fs={p.fs.filter((f) => f.section === 'pdb')} set={p.set} onToast={p.onToast} />
      <Table title="Can each node be drained?" sub={`kubectl drain --ignore-daemonsets --delete-emptydir-data`} id="policy-drains" columns={DRAIN_COLUMNS} rows={m.drains} terms={drainTerms} sort={{ col: 'verdict', dir: 'asc' }} now={p.now} rowAttr="drain" rowKey={(r) => r.node} rowLabel={(r) => `${r.node}: ${r.verdict}`} cluster={p.cluster} onToast={p.onToast} note="The verdicts follow kubectl’s drain logic and the eviction API: what a server-side dry run of the drain refuses is Blocked; a drain that needs more disruptions than a budget allows at once waits for replacements." />
      <Table title="PodDisruptionBudgets" id="policy-pdbs" columns={PDB_COLUMNS} rows={m.pdbs} terms={pdbTerms} sort={{ col: 'ns', dir: 'asc' }} now={p.now} onOpen={(k) => p.set({ open: k })} rowAttr="pdb" rowKey={(r) => r.key} rowLabel={(r) => `PodDisruptionBudget ${r.namespace}/${r.name} — open details`} cluster={p.cluster} onToast={p.onToast} empty="No PodDisruptionBudgets in this cluster." />
    </>
  )
}

function QuotaCard({ q, now, set }: { q: QuotaRow; now: number; set: SecProps['set'] }) {
  return (
    <section className="rk-card" style={{ overflow: 'hidden' }} aria-label={`ResourceQuota ${q.namespace}/${q.name}`} data-testid="quota-card" data-quota={q.key}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
        <strong className="rk-mono" style={{ fontSize: 13 }}>
          {q.namespace}/{q.name}
        </strong>
        {q.created && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>created {fmtAge(q.created, now)} ago</span>}
        <button className="rk-link" style={{ marginLeft: 'auto', fontSize: 12.5 }} onClick={() => set({ open: q.key })}>
          Details and YAML
        </button>
      </div>
      <div style={{ padding: '10px 16px 12px', display: 'flex', flexDirection: 'column', gap: 6 }}>
        {q.items.map((it) => (
          <div key={it.resource} className="rk-qrow" data-resource={it.resource} style={{ display: 'grid', gridTemplateColumns: 'minmax(160px, 1fr) minmax(80px, 1.3fr) minmax(130px, auto)', alignItems: 'center', gap: 12 }}>
            <span className="rk-mono" style={{ fontSize: 12 }}>
              {it.resource}
            </span>
            <span className="rk-bar" style={{ height: 8 }}>
              <span className="rk-use" style={{ width: `${Math.min(100, it.pct)}%`, background: it.tone === 'err' ? 'var(--err)' : it.tone === 'warn' ? 'var(--warn)' : undefined }} />
            </span>
            <span className="rk-num" style={{ fontSize: 12.5, textAlign: 'right' }} data-testid="quota-used">
              {qtyNice(it.resource, it.used)} of {qtyNice(it.resource, it.hard)}
            </span>
          </div>
        ))}
        {q.scopes.length > 0 && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>Counts only pods with {q.scopes.join(', ')}</span>}
      </div>
    </section>
  )
}

function Quotas(p: SecProps) {
  const { m } = p
  return (
    <>
      <Head title="Quotas and limits" summary={sectionSub('quota', m, p.fs)} intro="A ResourceQuota caps what a namespace’s pods may request and how many objects it may hold; the API server rejects a pod that would go over. A LimitRange fills in requests and limits for containers that state none." />
      <Review fs={p.fs.filter((f) => f.section === 'quota')} set={p.set} onToast={p.onToast} />
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10 }}>
        <h2 style={{ margin: 0, fontSize: 14, fontWeight: 600 }}>ResourceQuotas</h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{nWord(m.quotas.length, 'quota', 'quotas')}, as the API server counts them</span>
      </div>
      {m.quotas.length ? (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(420px, 100%), 1fr))', gap: 12, alignItems: 'start' }}>
          {[...m.quotas]
            .sort((a, b) => a.key.localeCompare(b.key))
            .map((q) => (
              <QuotaCard key={q.key} q={q} now={p.now} set={p.set} />
            ))}
        </div>
      ) : (
        <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No namespace has a ResourceQuota.</p>
      )}
      <Table title="Room for a rolling update" sub="Deployments in namespaces with a quota" id="policy-rollouts" columns={ROLLOUT_COLUMNS} rows={m.rollouts} terms={rolloutTerms} sort={{ col: 'verdict', dir: 'asc' }} now={p.now} rowAttr="rollout" rowKey={(r) => r.key} rowLabel={(r) => `Deployment ${r.namespace}/${r.name}`} cluster={p.cluster} onToast={p.onToast} />
      <Table title="LimitRanges" id="policy-limits" columns={LIMITS_COLUMNS} rows={m.limits} terms={limitsTerms} sort={{ col: 'ns', dir: 'asc' }} now={p.now} onOpen={(k) => p.set({ open: k })} rowAttr="limits" rowKey={(r) => r.key} rowLabel={(r) => `LimitRange ${r.namespace}/${r.name} — open details`} cluster={p.cluster} onToast={p.onToast} />
    </>
  )
}

function PodSecurity(p: SecProps) {
  const { m } = p
  return (
    <>
      <Head title="Pod Security" summary={sectionSub('psa', m, p.fs)} intro="Pod Security admission checks every new pod against the level a namespace’s labels name: privileged allows anything, baseline blocks host access and privileged containers, restricted also demands a hardened securityContext. Warn and audit only report. The checks here are the API server’s own." />
      <Review fs={p.fs.filter((f) => f.section === 'psa')} set={p.set} onToast={p.onToast} />
      <Table<PolicyNSRow> title="Pod Security by namespace" id="policy-psa" columns={PSA_COLUMNS} rows={m.namespaces} terms={nsTerms} sort={{ col: 'enforce', dir: 'asc' }} now={p.now} onOpen={(k) => p.set({ open: `ns:${k}` })} rowAttr="psa" rowKey={(r) => r.name} rowLabel={(r) => `Namespace ${r.name} — what each level would reject`} cluster={p.cluster} onToast={p.onToast} />
      <p style={{ margin: 0, fontSize: 12, color: 'var(--fg3)' }}>{appNamespaces(m).length} application namespaces; open one for the pods each level rejects and what kubectl warns when the enforce label changes.</p>
    </>
  )
}

function Webhooks(p: SecProps) {
  const { m } = p
  return (
    <>
      <Head title="Admission webhooks" summary={sectionSub('webhooks', m, p.fs)} intro="Admission webhooks see each matching create, update or delete before it is stored, and may change or reject it. With failurePolicy Fail, the request is rejected whenever the webhook’s service can’t answer. 221B checks the Service and its endpoints; it never calls a webhook." />
      <Review fs={p.fs.filter((f) => f.section === 'webhooks')} set={p.set} onToast={p.onToast} />
      <Table title="Admission webhooks" id="policy-hooks" columns={HOOK_COLUMNS} rows={m.hooks} terms={hookTerms} sort={{ col: 'reach', dir: 'asc' }} now={p.now} onOpen={(k) => p.set({ open: k })} rowAttr="hook" rowKey={(r) => r.key} rowLabel={(r) => `${r.kind}WebhookConfiguration ${r.config} — open details`} cluster={p.cluster} onToast={p.onToast} empty="No admission webhooks in this cluster." />
    </>
  )
}

function Priorities(p: SecProps) {
  const { m } = p
  const nc = m.noClass
  const def = m.priorities.find((x) => x.globalDefault)
  return (
    <>
      <Head title="Priority classes" summary={sectionSub('prio', m, p.fs)} intro={`When no node has room for a pod, the scheduler may evict pods of lower priority to make it. ${def ? `Pods without a class get ${def.name}’s value, ${def.value}.` : 'Pods without a class run at priority 0, so they can neither preempt others nor be protected from preemption.'}`} />
      <Table
        title="PriorityClasses"
        id="policy-priorities"
        columns={PRIORITY_COLUMNS}
        rows={m.priorities}
        terms={priorityTerms}
        sort={{ col: 'value', dir: 'desc' }}
        now={p.now}
        onOpen={(k) => p.set({ open: k })}
        rowAttr="pc"
        rowKey={(r) => r.key}
        rowLabel={(r) => `PriorityClass ${r.name} — open details`}
        cluster={p.cluster}
        onToast={p.onToast}
        note={nc ? `The other ${nWord(nc.pods, 'running or waiting pod has', 'running or waiting pods have')} no class and ${def ? `get ${def.name}’s value` : 'run at priority 0'}${nc.waiting ? `, among them ${nc.waiting}, which waits for room instead of preempting anything.` : '.'}` : undefined}
      />
    </>
  )
}
