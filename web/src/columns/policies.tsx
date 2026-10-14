import type { DrainRow, HookRow, LimitsRow, NetPolRow, PDBRow, PolicyNSRow, PriorityRow, RolloutRow } from '../api/types'
import { fmtAge, fmtTs, minutesSince } from '../lib/format'
import { listText } from '../lib/rbac'
import { nextLevel, PSA_RANK, PSA_TONE, qtyNice, shortReasons, VERDICT } from '../lib/policies'
import { href } from '../lib/router'
import type { ColumnDef, Term } from '../lib/table'
import { CellLink, Mono } from './cells'

function age<R extends { created?: string }>(): ColumnDef<R> {
  return {
    id: 'age',
    label: 'Age',
    sortName: 'age (newest first)',
    width: '64px',
    min: 64,
    right: true,
    sortValue: (r, now) => (r.created ? minutesSince(r.created, now) : null),
    cell: (r, now) => <Mono text={r.created ? fmtAge(r.created, now) : '—'} tip={r.created ? fmtTs(r.created) : undefined} />,
    export: (r) => [{ head: 'Created', cell: r.created ? fmtTs(r.created) : '' }],
  }
}

function Small({ text, tip, tone }: { text: string; tip?: string; tone?: string }) {
  return (
    <span className={tone ? 'rk-t' : undefined} data-tone={tone} title={tip ?? text} style={{ fontSize: 12.5, color: tone ? undefined : 'var(--fg2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>
      {text || '—'}
    </span>
  )
}

function Meter({ n, of, tone }: { n: number; of: number; tone: string }) {
  const pct = of ? Math.round((n / of) * 100) : 0
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
      <span className="rk-bar" style={{ width: 70, flex: 'none' }}>
        <span className="rk-use" data-tone={tone} style={{ width: `${pct}%`, background: tone === 'ok' ? 'var(--ok)' : tone === 'warn' ? 'var(--warn)' : undefined }} />
      </span>
      <span className="rk-num" style={{ fontSize: 12.5 }}>
        {n} of {of}
      </span>
    </span>
  )
}

const nsLink = (ns: string) => <CellLink href={href('namespaces', [ns])} text={ns} tip={`Open ${ns} in the Namespaces tab`} />

// ---------- network policies ----------

export const ISOLATION_COLUMNS: ColumnDef<PolicyNSRow>[] = [
  { id: 'ns', label: 'Namespace', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} />, export: (r) => [{ head: 'Namespace', cell: r.name }] },
  { id: 'pols', label: 'Policies', sortName: 'policies (most first)', width: '80px', min: 80, right: true, defaultDir: 'desc', sortValue: (r) => r.policies, cell: (r) => <span className="rk-num">{r.policies}</span>, export: (r) => [{ head: 'Policies', cell: r.policies }] },
  {
    id: 'iso',
    label: 'Isolated pods',
    sortName: 'isolation (least first)',
    width: 'minmax(170px, 1fr)',
    min: 170,
    sortValue: (r) => (r.pods - r.hostNetwork > 0 ? r.isolatedIn / (r.pods - r.hostNetwork) : 2),
    cell: (r) => {
      const net = r.pods - r.hostNetwork
      if (!net) return <Small text={r.hostNetwork ? 'host network only' : 'no pods'} />
      return <Meter n={r.isolatedIn} of={net} tone={r.isolatedIn === net ? 'ok' : r.isolatedIn ? 'warn' : 'muted'} />
    },
    export: (r) => [{ head: 'Isolated pods', cell: `${r.isolatedIn} of ${r.pods - r.hostNetwork}` }],
  },
  {
    id: 'deny',
    label: 'Default deny',
    width: '120px',
    min: 120,
    sortValue: (r) => (r.denyIn ? 0 : 1) + (r.denyOut ? 0 : 2),
    cell: (r) => <Small text={r.denyIn && r.denyOut ? 'in and out' : r.denyIn ? 'incoming' : r.denyOut ? 'outgoing' : 'no'} tone={r.denyIn || r.denyOut ? 'ok' : undefined} />,
    export: (r) => [{ head: 'Default deny', cell: r.denyIn && r.denyOut ? 'in and out' : r.denyIn ? 'incoming' : r.denyOut ? 'outgoing' : 'no' }],
  },
  { id: 'egress', label: 'Egress limited', sortName: 'pods whose egress is limited (most first)', width: '110px', min: 110, right: true, defaultDir: 'desc', sortValue: (r) => r.isolatedOut, cell: (r) => <span className="rk-num">{r.isolatedOut}</span>, export: (r) => [{ head: 'Egress limited', cell: r.isolatedOut }] },
  { id: 'hn', label: 'Host-network pods', sortName: 'host-network pods (policies can’t isolate them)', width: '140px', min: 140, right: true, defaultDir: 'desc', sortValue: (r) => r.hostNetwork, cell: (r) => <span className="rk-num">{r.hostNetwork}</span>, export: (r) => [{ head: 'Host-network pods', cell: r.hostNetwork }] },
  { id: 'open', label: 'Open to every pod', sortName: 'workloads any pod can reach', width: 'minmax(220px, 1.4fr)', min: 220, sortValue: (r) => r.open.length, cell: (r) => <Mono text={r.open.join(', ') || '—'} tip={r.open.join(', ')} size={12} />, export: (r) => [{ head: 'Open to every pod', cell: r.open.join(', ') }] },
]

export const nsTerms = (r: PolicyNSRow): Term[] => [{ kind: 'name', text: r.name }]

export const NETPOL_COLUMNS: ColumnDef<NetPolRow>[] = [
  { id: 'name', label: 'Policy', width: 'minmax(200px, 1.2fr)', min: 200, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} tip={r.name} />, export: (r) => [{ head: 'Policy', cell: r.name }] },
  { id: 'ns', label: 'Namespace', width: '120px', min: 120, sortValue: (r) => r.namespace, cell: (r) => <Mono text={r.namespace} />, export: (r) => [{ head: 'Namespace', cell: r.namespace }] },
  { id: 'sel', label: 'Applies to', width: 'minmax(160px, 1fr)', min: 160, sortValue: (r) => r.selector, cell: (r) => <Mono text={r.selector} tip={r.selector} size={12} />, export: (r) => [{ head: 'Applies to', cell: r.selector }] },
  { id: 'types', label: 'Restricts', width: '120px', min: 120, sortValue: (r) => (r.ingress ? 0 : 2) + (r.egress ? 0 : 1), cell: (r) => <Small text={[r.ingress && 'incoming', r.egress && 'outgoing'].filter(Boolean).join(' and ')} />, export: (r) => [{ head: 'Restricts', cell: [r.ingress && 'Ingress', r.egress && 'Egress'].filter(Boolean).join(', ') }] },
  { id: 'pods', label: 'Pods', sortName: 'pods selected (most first)', width: '60px', min: 60, right: true, defaultDir: 'desc', sortValue: (r) => r.pods.length, cell: (r) => <span className="rk-num">{r.pods.length}</span>, export: (r) => [{ head: 'Pods', cell: r.pods.length }] },
  {
    id: 'allows',
    label: 'Allows',
    width: 'minmax(300px, 2.4fr)',
    min: 300,
    sortValue: (r) => (r.allows ?? '') + (r.allowsOut ?? ''),
    cell: (r) => <Small text={[r.allows && `In: ${r.allows}`, r.allowsOut && `Out: ${r.allowsOut}`].filter(Boolean).join(' · ')} />,
    export: (r) => [
      { head: 'Allows in', cell: r.allows ?? '' },
      { head: 'Allows out', cell: r.allowsOut ?? '' },
    ],
  },
  age<NetPolRow>(),
]

export const netpolTerms = (r: NetPolRow): Term[] => [
  { kind: 'name', text: r.name },
  { kind: 'namespace', text: r.namespace },
]

// ---------- disruption budgets and drains ----------

export const DRAIN_COLUMNS: ColumnDef<DrainRow>[] = [
  { id: 'node', label: 'Node', width: 'minmax(130px, .8fr)', min: 130, sortValue: (r) => r.node, cell: (r) => <CellLink href={href('nodes', [], { open: r.node })} text={r.node + (r.cordoned ? ' (cordoned)' : '')} tip={`Open ${r.node} in the Nodes tab`} />, export: (r) => [{ head: 'Node', cell: r.node }] },
  { id: 'ev', label: 'Pods to evict', sortName: 'pods to evict (most first)', width: '110px', min: 110, right: true, defaultDir: 'desc', sortValue: (r) => r.evict.length, cell: (r) => <span className="rk-num" title={`${r.daemonSet.length} DaemonSet and ${r.mirror} static pods stay`}>{r.evict.length}</span>, export: (r) => [{ head: 'Pods to evict', cell: r.evict.length }] },
  { id: 'budgets', label: 'Budgets involved', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => r.budgets.join(', '), cell: (r) => <Small text={r.budgets.join(', ') || 'none'} />, export: (r) => [{ head: 'Budgets involved', cell: r.budgets.join(', ') }] },
  {
    id: 'verdict',
    label: 'Verdict',
    sortName: 'verdict (blocked first)',
    width: '170px',
    min: 170,
    sortValue: (r) => ({ blocked: 0, waits: 1, clean: 2 })[r.verdict],
    cell: (r) => (
      <span className="rk-pill" data-tone={VERDICT[r.verdict].tone} data-testid="drain-verdict">
        <span className="rk-dot" />
        {VERDICT[r.verdict].text}
      </span>
    ),
    export: (r) => [{ head: 'Verdict', cell: VERDICT[r.verdict].text }],
  },
  { id: 'why', label: 'Why', width: 'minmax(280px, 2.6fr)', min: 280, sortValue: (r) => r.why, cell: (r) => <Small text={r.why} />, export: (r) => [{ head: 'Why', cell: r.why }] },
]

export const drainTerms = (r: DrainRow): Term[] => [{ kind: 'name', text: r.node }]

export const PDB_COLUMNS: ColumnDef<PDBRow>[] = [
  { id: 'name', label: 'Budget', width: 'minmax(200px, 1.4fr)', min: 200, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} tip={r.name} />, export: (r) => [{ head: 'Budget', cell: r.name }] },
  { id: 'ns', label: 'Namespace', width: '130px', min: 130, sortValue: (r) => r.namespace, cell: (r) => <Mono text={r.namespace} />, export: (r) => [{ head: 'Namespace', cell: r.namespace }] },
  { id: 'protects', label: 'Protects', width: 'minmax(180px, 1.2fr)', min: 180, sortValue: (r) => r.selector, cell: (r) => <Small text={`pods with ${r.selector}`} />, export: (r) => [{ head: 'Protects', cell: r.selector }] },
  { id: 'rule', label: 'Rule', width: '150px', min: 150, sortValue: (r) => r.rule, cell: (r) => <Mono text={r.rule} size={12} />, export: (r) => [{ head: 'Rule', cell: r.rule }] },
  { id: 'healthy', label: 'Healthy', width: '84px', min: 84, right: true, sortValue: (r) => r.currentHealthy, cell: (r) => <span className="rk-num" title={`${r.desiredHealthy} must stay healthy`}>{`${r.currentHealthy}/${r.expectedPods}`}</span>, export: (r) => [{ head: 'Healthy', cell: `${r.currentHealthy}/${r.expectedPods}` }] },
  {
    id: 'allowed',
    label: 'Disruptions allowed',
    sortName: 'disruptions allowed (fewest first)',
    width: '150px',
    min: 150,
    sortValue: (r) => r.disruptionsAllowed,
    cell: (r) => (
      <span className="rk-t rk-num" data-tone={r.disruptionsAllowed > 0 ? 'ok' : 'warn'} style={{ fontWeight: 500 }}>
        {r.disruptionsAllowed}
        {r.expectedPods === 0 ? ' · selects no pod' : r.overlap.length ? ' · overlaps' : ''}
      </span>
    ),
    export: (r) => [{ head: 'Disruptions allowed', cell: r.disruptionsAllowed }],
  },
  age<PDBRow>(),
]

export const pdbTerms = (r: PDBRow): Term[] => [
  { kind: 'name', text: r.name },
  { kind: 'namespace', text: r.namespace },
]

// ---------- quotas ----------

function needText(r: RolloutRow): string {
  const parts = (m: Record<string, string>) =>
    Object.entries(m)
      .filter(([k]) => k === 'cpu' || k === 'memory')
      .map(([k, v]) => (k === 'cpu' ? qtyNice('cpu', v) : `${v} memory`))
      .join(', ')
  const req = parts(r.requests)
  const lim = parts(r.limits)
  return [req && `requests ${req}`, lim && `limits ${lim}`].filter(Boolean).join(' · ')
}

export const ROLLOUT_COLUMNS: ColumnDef<RolloutRow>[] = [
  { id: 'name', label: 'Deployment', width: 'minmax(160px, 1fr)', min: 160, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} />, export: (r) => [{ head: 'Deployment', cell: r.name }] },
  { id: 'ns', label: 'Namespace', width: '120px', min: 120, sortValue: (r) => r.namespace, cell: (r) => <Mono text={r.namespace} />, export: (r) => [{ head: 'Namespace', cell: r.namespace }] },
  { id: 'replicas', label: 'Replicas', width: '80px', min: 80, right: true, sortValue: (r) => r.replicas, cell: (r) => <span className="rk-num">{r.replicas}</span>, export: (r) => [{ head: 'Replicas', cell: r.replicas }] },
  { id: 'surge', label: 'Surge', sortName: 'pods added during an update', width: '70px', min: 70, right: true, sortValue: (r) => r.surge, cell: (r) => <span className="rk-num">{r.surge}</span>, export: (r) => [{ head: 'Surge', cell: r.surge }] },
  { id: 'needs', label: 'Each pod needs', width: 'minmax(220px, 1.4fr)', min: 220, sortValue: (r) => needText(r), cell: (r) => <Small text={needText(r)} />, export: (r) => [{ head: 'Each pod needs', cell: needText(r) }] },
  {
    id: 'verdict',
    label: 'Verdict',
    sortName: 'verdict (stalls first)',
    width: 'minmax(220px, 1.4fr)',
    min: 220,
    sortValue: (r) => (r.stalls ? 0 : r.short.length ? 1 : 2),
    cell: (r) => <Small text={r.stalls ? `Would stall on ${listText([...new Set(r.short.map((s) => s.resource))])}` : r.short.length ? 'Fits by removing an old pod first' : 'Fits'} tone={r.stalls ? 'warn' : 'ok'} />,
    export: (r) => [{ head: 'Verdict', cell: r.stalls ? `Would stall on ${r.short.map((s) => s.resource).join(', ')}` : 'Fits' }],
  },
]

export const rolloutTerms = (r: RolloutRow): Term[] => [{ kind: 'name', text: r.name }]

const rlText = (m?: Record<string, string>) => (m ? [m.cpu, m.memory].filter(Boolean).join(' · ') : '') || '—'

export const LIMITS_COLUMNS: ColumnDef<LimitsRow>[] = [
  { id: 'name', label: 'Limit range', width: 'minmax(180px, 1fr)', min: 180, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} />, export: (r) => [{ head: 'Limit range', cell: r.name }] },
  { id: 'ns', label: 'Namespace', width: '110px', min: 110, sortValue: (r) => r.namespace, cell: (r) => <Mono text={r.namespace} />, export: (r) => [{ head: 'Namespace', cell: r.namespace }] },
  { id: 'req', label: 'Default request', width: '150px', min: 150, sortValue: (r) => rlText(r.items[0]?.defaultRequest), cell: (r) => <Mono text={rlText(r.items.find((i) => i.type === 'Container')?.defaultRequest)} size={12} />, export: (r) => [{ head: 'Default request', cell: rlText(r.items.find((i) => i.type === 'Container')?.defaultRequest) }] },
  { id: 'lim', label: 'Default limit', width: '140px', min: 140, sortValue: (r) => rlText(r.items[0]?.default), cell: (r) => <Mono text={rlText(r.items.find((i) => i.type === 'Container')?.default)} size={12} />, export: (r) => [{ head: 'Default limit', cell: rlText(r.items.find((i) => i.type === 'Container')?.default) }] },
  { id: 'max', label: 'Maximum', width: '120px', min: 120, sortValue: (r) => rlText(r.items[0]?.max), cell: (r) => <Mono text={rlText(r.items.find((i) => i.type === 'Container')?.max)} size={12} />, export: (r) => [{ head: 'Maximum', cell: rlText(r.items.find((i) => i.type === 'Container')?.max) }] },
  { id: 'at', label: 'Containers at the defaults', width: 'minmax(220px, 1.5fr)', min: 220, sortValue: (r) => r.atDefaults.length, cell: (r) => <Small text={r.atDefaults.join(', ')} />, export: (r) => [{ head: 'Containers at the defaults', cell: r.atDefaults.join(', ') }] },
  age<LimitsRow>(),
]

export const limitsTerms = (r: LimitsRow): Term[] => [{ kind: 'name', text: r.name }]

// ---------- Pod Security ----------

function level(l?: string, v?: string): string {
  return l ? l + (v && v !== 'latest' ? `:${v}` : '') : ''
}

export const PSA_COLUMNS: ColumnDef<PolicyNSRow>[] = [
  { id: 'ns', label: 'Namespace', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => r.name, cell: (r) => nsLink(r.name), export: (r) => [{ head: 'Namespace', cell: r.name }] },
  {
    id: 'enforce',
    label: 'Enforce',
    sortName: 'enforce level (least strict first)',
    width: '120px',
    min: 120,
    sortValue: (r) => PSA_RANK[r.enforce ?? ''] ?? 0,
    cell: (r) => (
      <span className="rk-tag" data-tone={PSA_TONE[r.enforce ?? ''] ?? 'muted'} style={{ fontSize: 11, padding: '1px 7px' }} title={r.enforce ? undefined : 'No label: the cluster’s default applies, usually privileged'}>
        {level(r.enforce, r.enforceVersion) || 'none'}
      </span>
    ),
    export: (r) => [{ head: 'Enforce', cell: level(r.enforce, r.enforceVersion) }],
  },
  { id: 'warn', label: 'Warn', width: '100px', min: 100, sortValue: (r) => PSA_RANK[r.warn ?? ''] ?? 0, cell: (r) => <Small text={level(r.warn, r.warnVersion)} />, export: (r) => [{ head: 'Warn', cell: level(r.warn, r.warnVersion) }] },
  { id: 'audit', label: 'Audit', width: '100px', min: 100, sortValue: (r) => PSA_RANK[r.audit ?? ''] ?? 0, cell: (r) => <Small text={level(r.audit, r.auditVersion)} />, export: (r) => [{ head: 'Audit', cell: level(r.audit, r.auditVersion) }] },
  { id: 'pods', label: 'Pods', sortName: 'pods (most first)', width: '64px', min: 64, right: true, defaultDir: 'desc', sortValue: (r) => r.pods, cell: (r) => <span className="rk-num">{r.pods}</span>, export: (r) => [{ head: 'Pods', cell: r.pods }] },
  { id: 'base', label: 'Fail baseline', sortName: 'pods that fail baseline (most first)', width: '110px', min: 110, right: true, defaultDir: 'desc', sortValue: (r) => r.baseline.fail.length, cell: (r) => <span className="rk-num" style={{ color: r.baseline.fail.length ? undefined : 'var(--fg3)' }}>{r.baseline.fail.length}</span>, export: (r) => [{ head: 'Fail baseline', cell: r.baseline.fail.length }] },
  { id: 'restr', label: 'Fail restricted', sortName: 'pods that fail restricted (most first)', width: '120px', min: 120, right: true, defaultDir: 'desc', sortValue: (r) => r.restricted.fail.length, cell: (r) => <span className="rk-num" style={{ color: r.restricted.fail.length ? undefined : 'var(--fg3)' }}>{r.restricted.fail.length}</span>, export: (r) => [{ head: 'Fail restricted', cell: r.restricted.fail.length }] },
  {
    id: 'why',
    label: 'Why the next level rejects them',
    width: 'minmax(240px, 1.8fr)',
    min: 240,
    sortValue: (r) => {
      const n = nextLevel(r)
      return n ? shortReasons(r[n].fail) : ''
    },
    cell: (r) => {
      const n = nextLevel(r)
      return <Small text={n ? (r[n].fail.length ? `${n}: ${shortReasons(r[n].fail)}` : `every pod passes ${n}`) : 'restricted is the strictest level'} />
    },
    export: (r) => {
      const n = nextLevel(r)
      return [{ head: 'Next level fails because', cell: n ? shortReasons(r[n].fail) : '' }]
    },
  },
]

// ---------- webhooks ----------

export const HOOK_COLUMNS: ColumnDef<HookRow>[] = [
  { id: 'name', label: 'Webhook', width: 'minmax(200px, 1.2fr)', min: 200, sortValue: (r) => r.webhook, cell: (r) => <Mono text={r.webhook} dim={false} tip={`${r.config} / ${r.webhook}`} />, export: (r) => [{ head: 'Configuration', cell: r.config }, { head: 'Webhook', cell: r.webhook }] },
  {
    id: 'kind',
    label: 'Kind',
    width: '96px',
    min: 96,
    sortValue: (r) => r.kind,
    cell: (r) => (
      <span className="rk-tag" data-tone={r.kind === 'Mutating' ? 'info' : undefined} style={{ fontSize: 11, padding: '1px 7px' }}>
        {r.kind}
      </span>
    ),
    export: (r) => [{ head: 'Kind', cell: r.kind }],
  },
  { id: 'checks', label: 'Checks', width: 'minmax(200px, 1.4fr)', min: 200, sortValue: (r) => r.rules.join('; '), cell: (r) => <Small text={r.rules.join('; ')} />, export: (r) => [{ head: 'Checks', cell: r.rules.join('; ') }] },
  { id: 'scope', label: 'Scope', width: 'minmax(150px, 1fr)', min: 150, hiddenByDefault: true, sortValue: (r) => r.namespaces, cell: (r) => <Small text={`${r.namespaces}${r.objects !== 'every object' ? ' · ' + r.objects : ''}`} />, export: (r) => [{ head: 'Namespaces', cell: r.namespaces }, { head: 'Objects', cell: r.objects }] },
  { id: 'svc', label: 'Backend', width: 'minmax(190px, 1.2fr)', min: 190, sortValue: (r) => r.service ?? r.url ?? '', cell: (r) => <Mono text={r.service ?? r.url ?? '—'} size={12} />, export: (r) => [{ head: 'Backend', cell: r.service ?? r.url ?? '' }] },
  {
    id: 'fail',
    label: 'Failure policy',
    width: '112px',
    min: 112,
    sortValue: (r) => r.failurePolicy,
    cell: (r) => (
      <span className="rk-tag" data-tone={r.failurePolicy === 'Fail' ? 'warn' : undefined} style={{ fontSize: 11, padding: '1px 7px' }}>
        {r.failurePolicy}
      </span>
    ),
    export: (r) => [{ head: 'Failure policy', cell: r.failurePolicy }],
  },
  {
    id: 'reach',
    label: 'Reachable',
    sortName: 'backend readiness (least ready first)',
    width: '150px',
    min: 150,
    sortValue: (r) => (r.reach.total ? r.reach.ready / r.reach.total + (r.reach.total > 1 ? 1 : 0) : -1),
    cell: (r) => (
      <span className="rk-t" data-tone={r.reach.tone} style={{ fontSize: 12.5, fontWeight: 500 }} data-testid="hook-reach">
        {r.reach.text}
      </span>
    ),
    export: (r) => [{ head: 'Reachable', cell: r.reach.text }],
  },
  { id: 'side', label: 'Side effects', width: '110px', min: 110, hiddenByDefault: true, sortValue: (r) => r.sideEffects ?? '', cell: (r) => <Small text={r.sideEffects ?? ''} />, export: (r) => [{ head: 'Side effects', cell: r.sideEffects ?? '' }] },
  { id: 'timeout', label: 'Timeout', width: '72px', min: 72, right: true, sortValue: (r) => r.timeout, cell: (r) => <span className="rk-num">{r.timeout}s</span>, export: (r) => [{ head: 'Timeout', cell: r.timeout }] },
]

export const hookTerms = (r: HookRow): Term[] => [
  { kind: 'name', text: r.webhook },
  { kind: 'config', text: r.config },
]

// ---------- priority classes ----------

export const PRIORITY_COLUMNS: ColumnDef<PriorityRow>[] = [
  { id: 'name', label: 'Class', width: 'minmax(190px, 1fr)', min: 190, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} />, export: (r) => [{ head: 'Class', cell: r.name }] },
  { id: 'value', label: 'Value', sortName: 'value (highest first)', width: '120px', min: 120, right: true, defaultDir: 'desc', sortValue: (r) => r.value, cell: (r) => <span className="rk-num">{r.value}</span>, export: (r) => [{ head: 'Value', cell: r.value }] },
  { id: 'default', label: 'Global default', width: '110px', min: 110, sortValue: (r) => (r.globalDefault ? 0 : 1), cell: (r) => (r.globalDefault ? <span className="rk-tag" data-tone="info" style={{ fontSize: 11, padding: '1px 7px' }}>yes</span> : <Small text="" />), export: (r) => [{ head: 'Global default', cell: r.globalDefault ? 'yes' : '' }] },
  { id: 'preempt', label: 'Preemption', width: '160px', min: 160, sortValue: (r) => r.preemption, cell: (r) => (r.preemption === 'Never' ? <span className="rk-tag" style={{ fontSize: 11, padding: '1px 7px' }}>Never</span> : <Small text={r.preemption} />), export: (r) => [{ head: 'Preemption', cell: r.preemption }] },
  { id: 'pods', label: 'Pods', sortName: 'pods using it (most first)', width: '70px', min: 70, right: true, defaultDir: 'desc', sortValue: (r) => r.pods, cell: (r) => <span className="rk-num">{r.pods}</span>, export: (r) => [{ head: 'Pods', cell: r.pods }] },
  { id: 'used', label: 'Used by', width: 'minmax(200px, 1.4fr)', min: 200, sortValue: (r) => r.workloads.join(', '), cell: (r) => <Mono text={r.workloads.join(', ')} size={12} tip={r.workloads.join(', ')} />, export: (r) => [{ head: 'Used by', cell: r.workloads.join(', ') }] },
  { id: 'desc', label: 'Description', width: 'minmax(240px, 1.8fr)', min: 240, sortValue: (r) => r.description ?? '', cell: (r) => <Small text={r.description ?? ''} />, export: (r) => [{ head: 'Description', cell: r.description ?? '' }] },
]

export const priorityTerms = (r: PriorityRow): Term[] => [{ kind: 'name', text: r.name }]

// ---------- coverage ----------

export const COVERAGE_COLUMNS: ColumnDef<PolicyNSRow>[] = [
  { id: 'ns', label: 'Namespace', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => r.name, cell: (r) => nsLink(r.name), export: (r) => [{ head: 'Namespace', cell: r.name }] },
  {
    id: 'np',
    label: 'Network policies',
    width: 'minmax(180px, 1.2fr)',
    min: 180,
    sortValue: (r) => (r.pods - r.hostNetwork > 0 ? r.isolatedIn / (r.pods - r.hostNetwork) : 2),
    cell: (r) => {
      const net = r.pods - r.hostNetwork
      if (!net) return <Small text={r.hostNetwork ? 'host network only' : 'no pods'} />
      const tone = r.isolatedIn === net ? 'ok' : r.isolatedIn ? 'warn' : r.system ? 'muted' : 'warn'
      return <Small text={`${r.isolatedIn} of ${net} pods isolated${r.policies ? ` · ${r.policies}` : ''}`} tone={tone} />
    },
    export: (r) => [{ head: 'Network policies', cell: `${r.isolatedIn} of ${r.pods - r.hostNetwork} pods isolated` }],
  },
  { id: 'budgets', label: 'Budgets', width: '80px', min: 80, right: true, sortValue: (r) => r.budgets, cell: (r) => <span className="rk-num">{r.budgets}</span>, export: (r) => [{ head: 'Budgets', cell: r.budgets }] },
  { id: 'quota', label: 'Quota', width: 'minmax(120px, .8fr)', min: 120, sortValue: (r) => r.quotas.join(', '), cell: (r) => <Mono text={r.quotas.join(', ') || '—'} size={12} />, export: (r) => [{ head: 'Quota', cell: r.quotas.join(', ') }] },
  { id: 'limits', label: 'Limit range', width: 'minmax(120px, .8fr)', min: 120, sortValue: (r) => r.limitRanges.join(', '), cell: (r) => <Mono text={r.limitRanges.join(', ') || '—'} size={12} />, export: (r) => [{ head: 'Limit range', cell: r.limitRanges.join(', ') }] },
  {
    id: 'psa',
    label: 'Pod Security',
    width: '130px',
    min: 130,
    sortValue: (r) => PSA_RANK[r.enforce ?? ''] ?? 0,
    cell: (r) => (
      <span className="rk-tag" data-tone={PSA_TONE[r.enforce ?? ''] ?? 'muted'} style={{ fontSize: 11, padding: '1px 7px' }}>
        {r.enforce || 'not enforced'}
      </span>
    ),
    export: (r) => [{ head: 'Pod Security', cell: r.enforce || 'not enforced' }],
  },
]
