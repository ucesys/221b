// The Policies tab's model (P4-04 to P4-06): the rows the server sends,
// sorted by kind, and what to review across them.

import type { CNIRow, DrainRow, HookRow, LimitsRow, NetPolRow, NoClassRow, PDBRow, PolicyNSRow, PolicyRow, PriorityRow, QuotaRow, RolloutRow, FindingSeverity } from '../api/types'
import { nWord } from './format'
import type { Tab } from './router'
import { listText } from './rbac'

export interface PolicyModel {
  cni?: CNIRow
  noClass?: NoClassRow
  namespaces: PolicyNSRow[]
  netpols: NetPolRow[]
  pdbs: PDBRow[]
  drains: DrainRow[]
  quotas: QuotaRow[]
  rollouts: RolloutRow[]
  limits: LimitsRow[]
  hooks: HookRow[]
  priorities: PriorityRow[]
}

export function modelOf(rows: PolicyRow[]): PolicyModel {
  const m: PolicyModel = { namespaces: [], netpols: [], pdbs: [], drains: [], quotas: [], rollouts: [], limits: [], hooks: [], priorities: [] }
  for (const r of rows) {
    const p = r.key.slice(0, r.key.indexOf(':'))
    switch (p) {
      case 'cni':
        m.cni = r as CNIRow
        break
      case 'pcnone':
        m.noClass = r as NoClassRow
        break
      case 'ns':
        m.namespaces.push(r as PolicyNSRow)
        break
      case 'np':
        m.netpols.push(r as NetPolRow)
        break
      case 'pdb':
        m.pdbs.push(r as PDBRow)
        break
      case 'drain':
        m.drains.push(r as DrainRow)
        break
      case 'quota':
        m.quotas.push(r as QuotaRow)
        break
      case 'rollout':
        m.rollouts.push(r as RolloutRow)
        break
      case 'limits':
        m.limits.push(r as LimitsRow)
        break
      case 'hook':
        m.hooks.push(r as HookRow)
        break
      case 'pc':
        m.priorities.push(r as PriorityRow)
        break
    }
  }
  m.namespaces.sort((a, b) => a.name.localeCompare(b.name))
  return m
}

export type Section = 'overview' | 'netpol' | 'pdb' | 'quota' | 'psa' | 'webhooks' | 'prio'

export const SECTIONS: [Section, string][] = [
  ['overview', 'Review'],
  ['netpol', 'Network policies'],
  ['pdb', 'Disruption budgets'],
  ['quota', 'Quotas and limits'],
  ['psa', 'Pod Security'],
  ['webhooks', 'Admission webhooks'],
  ['prio', 'Priority classes'],
]

export interface PolicyFinding {
  id: string
  section: Section
  severity: FindingSeverity
  title: string
  text: string
  code?: string
  codeNote?: string
  command?: string
  /** Chips: what each names, and where it leads. */
  path: { text: string; link?: { tab: Tab; path?: string[]; params?: Record<string, string> }; open?: string }[]
}

/** Namespaces an application runs in: not Kubernetes' own. */
export function appNamespaces(m: PolicyModel): PolicyNSRow[] {
  return m.namespaces.filter((n) => !n.system)
}

/** App namespaces with pods any pod can reach: none of their pods is isolated for ingress. */
export function openNamespaces(m: PolicyModel): PolicyNSRow[] {
  return appNamespaces(m).filter((n) => n.pods - n.hostNetwork > 0 && n.isolatedIn === 0)
}

export function isolatedNamespaces(m: PolicyModel): PolicyNSRow[] {
  return appNamespaces(m).filter((n) => n.pods - n.hostNetwork > 0 && n.isolatedIn > 0)
}

export function enforced(n: PolicyNSRow): boolean {
  return n.enforce === 'baseline' || n.enforce === 'restricted'
}

export function hookUnreachable(h: HookRow): boolean {
  return ['missing', 'port', 'noEndpoints', 'noneReady'].includes(h.reach.state)
}

/** Everything worth reviewing, worst first. */
export function findings(m: PolicyModel): PolicyFinding[] {
  const out: PolicyFinding[] = []
  if (m.cni?.enforces === 'no') {
    out.push({ id: 'cni', section: 'netpol', severity: 'medium', title: `${m.cni.name ?? 'The network plugin'} does not enforce NetworkPolicies`, text: `${m.cni.note ?? ''} Every policy here is stored and ignored: any pod can reach any other.`.trim(), path: [] })
  }
  const open = openNamespaces(m)
  if (open.length) {
    const wl = open.flatMap((n) => n.open.map((w) => `${n.name}/${w}`))
    const shown = wl.slice(0, 6)
    const iso = isolatedNamespaces(m).map((n) => n.name)
    out.push({
      id: 'np-open',
      section: 'netpol',
      severity: 'medium',
      title: `${listText(open.map((n) => n.name))} ${open.length === 1 ? 'accepts' : 'accept'} connections from any pod`,
      text: `No NetworkPolicy selects their pods, so every pod in the cluster can open any of their ports: ${listText(shown)}${wl.length > shown.length ? ` and ${wl.length - shown.length} more` : ''}.${iso.length ? ` ${listText(iso)} ${iso.length === 1 ? 'shows' : 'show'} the pattern to copy: a default deny plus narrow allows.` : ''}`,
      code: `apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: default-deny-ingress\n  namespace: ${open[0]!.name}\nspec:\n  podSelector: {}\n  policyTypes: [Ingress]`,
      codeNote: 'A starting point for each namespace: then allow what its clients need.',
      path: open.map((n) => ({ text: `namespace ${n.name}`, link: { tab: 'namespaces', path: [n.name] } })),
    })
  }
  for (const d of m.drains.filter((x) => x.verdict === 'blocked')) {
    out.push({ id: `drain-${d.node}`, section: 'pdb', severity: 'low', title: `Draining ${d.node} is blocked`, text: d.why, command: d.command, path: [{ text: `node ${d.node}`, link: { tab: 'nodes', params: { open: d.node } } }, ...d.blockers.filter((b) => b.budget).map((b) => ({ text: `PodDisruptionBudget ${b.namespace}/${b.budget}`, open: `pdb:${b.namespace}/${b.budget}` }))] })
  }
  for (const b of m.pdbs.filter((x) => x.overlap.length)) {
    out.push({ id: `pdb-overlap-${b.key}`, section: 'pdb', severity: 'medium', title: `${nWord(b.overlap.length, 'pod', 'pods')} of ${b.namespace}/${b.name} ${b.overlap.length === 1 ? 'has' : 'have'} more than one budget`, text: `${listText(b.overlap.slice(0, 5))}${b.overlap.length > 5 ? ' and more' : ''} ${b.overlap.length === 1 ? 'is' : 'are'} selected by another budget too. The eviction API refuses pods with more than one budget, so a drain stops at them.`, path: [{ text: `PodDisruptionBudget ${b.namespace}/${b.name}`, open: b.key }] })
  }
  for (const r of m.rollouts.filter((x) => x.stalls)) {
    const quotas = [...new Set(r.short.map((s) => s.quota))]
    out.push({
      id: `quota-${r.key}`,
      section: 'quota',
      severity: 'medium',
      title: `A rolling update of ${r.name} would stall on ${listText(quotas)}`,
      text: `An update first adds ${nWord(r.surge, 'pod', 'pods')} and removes none until ${r.surge === 1 ? 'it is' : 'they are'} ready (maxUnavailable resolves to 0 for ${nWord(r.replicas, 'replica', 'replicas')}). The quota has too little room: ${listText(r.short.map((s) => `${s.resource} (${s.left} left, ${s.need} needed)`))}. The new ReplicaSet would report “exceeded quota” and the old pods would keep running the old version.`,
      path: [...quotas.map((q) => ({ text: `ResourceQuota ${r.namespace}/${q}`, open: `quota:${r.namespace}/${q}` })), { text: `Deployment ${r.namespace}/${r.name}`, link: { tab: 'namespaces', path: [r.namespace], params: { open: 'Deployment/' + r.name } } }],
    })
  }
  for (const n of appNamespaces(m).filter((x) => !x.enforce && x.pods > 0 && x.baseline.fail.length === 0)) {
    out.push({
      id: `psa-${n.name}`,
      section: 'psa',
      severity: 'low',
      title: `${n.name} enforces no Pod Security level`,
      text: `Without a pod-security.kubernetes.io/enforce label the namespace gets the cluster’s default, usually privileged, so a new pod there may use the host network, host paths or privileged containers. All ${nWord(n.pods, 'pod', 'pods')} in ${n.name} already pass baseline, so enforcing it changes nothing that runs today.`,
      command: `kubectl label namespace ${n.name} pod-security.kubernetes.io/enforce=baseline`,
      path: [{ text: `namespace ${n.name}`, link: { tab: 'namespaces', path: [n.name] } }],
    })
  }
  const priv = appNamespaces(m).filter((n) => n.enforce === 'privileged')
  if (priv.length) {
    out.push({
      id: 'psa-privileged',
      section: 'psa',
      severity: 'info',
      title: `${listText(priv.map((n) => n.name))} ${priv.length === 1 ? 'allows' : 'allow'} privileged pods on purpose`,
      text: priv.map((n) => `${n.name}: ${n.baseline.fail.length ? `${nWord(n.baseline.fail.length, 'pod fails', 'pods fail')} baseline (${shortReasons(n.baseline.fail)})` : 'every pod passes baseline'}`).join('. ') + '. Limit who can create pods in these namespaces.',
      path: priv.map((n) => ({ text: `namespace ${n.name}`, link: { tab: 'namespaces', path: [n.name] } })),
    })
  }
  for (const h of m.hooks) {
    const svc = h.service ?? h.url ?? ''
    if (h.failurePolicy === 'Fail' && hookUnreachable(h)) {
      out.push({ id: `hook-${h.key}`, section: 'webhooks', severity: 'high', title: `${h.webhook} rejects what it checks: its backend can’t answer`, text: `${h.kind} webhook ${h.webhook} (${h.config}) uses failurePolicy Fail and ${svc}: ${h.reach.text}. Until it answers, the API server rejects every request it matches: ${h.rules.join('; ')}.`, path: [{ text: `${h.kind}WebhookConfiguration ${h.config}`, open: h.key }] })
    } else if (h.failurePolicy !== 'Fail' && hookUnreachable(h)) {
      out.push({ id: `hook-${h.key}`, section: 'webhooks', severity: 'low', title: `${h.webhook} is skipped: its backend can’t answer`, text: `${h.kind} webhook ${h.webhook} (${h.config}) uses failurePolicy Ignore and ${svc}: ${h.reach.text}. The requests it matches (${h.rules.join('; ')}) go through unchecked.`, path: [{ text: `${h.kind}WebhookConfiguration ${h.config}`, open: h.key }] })
    } else if (h.failurePolicy === 'Fail' && h.reach.total === 1) {
      out.push({ id: `hook-${h.key}`, section: 'webhooks', severity: 'low', title: `${h.webhook} runs one pod and fails closed`, text: `${h.kind} webhook ${h.webhook} (${h.config}) uses failurePolicy Fail with one pod behind ${svc}. While that pod restarts or its node drains, the API server rejects what it matches: ${h.rules.join('; ')}. Two replicas with a disruption budget remove the single point of failure.`, path: [{ text: `${h.kind}WebhookConfiguration ${h.config}`, open: h.key }] })
    }
  }
  const order: Record<FindingSeverity, number> = { high: 0, medium: 1, low: 2, info: 3 }
  return out.sort((a, b) => order[a.severity] - order[b.severity])
}

/** The union of reasons, first seen first. */
export function shortReasons(pods: { reasons: string[] }[]): string {
  return [...new Set(pods.flatMap((p) => p.reasons))].join(', ')
}

/** "1 high · 2 medium". */
export function countsText(fs: PolicyFinding[]): string {
  const sev: FindingSeverity[] = ['high', 'medium', 'low', 'info']
  const parts = sev.map((s) => [s, fs.filter((f) => f.severity === s).length] as const).filter(([, n]) => n).map(([s, n]) => `${n} ${s}`)
  return parts.length ? parts.join(' · ') : 'Nothing to review'
}

/** A section's flag: findings that are not info, and their worst tone. */
export function flagOf(fs: PolicyFinding[]): { count: number; tone: string } {
  const real = fs.filter((f) => f.severity !== 'info')
  return { count: real.length, tone: real.some((f) => f.severity === 'high') ? 'err' : real.some((f) => f.severity === 'medium') ? 'warn' : 'info' }
}

export function sectionSub(s: Section, m: PolicyModel, fs: PolicyFinding[]): string {
  switch (s) {
    case 'overview':
      return countsText(fs)
    case 'netpol': {
      const app = appNamespaces(m).filter((n) => n.pods - n.hostNetwork > 0)
      return `${nWord(m.netpols.length, 'policy', 'policies')} · ${isolatedNamespaces(m).length} of ${app.length} app namespaces isolated`
    }
    case 'pdb': {
      const blocked = m.drains.filter((d) => d.verdict === 'blocked').length
      return `${nWord(m.pdbs.length, 'budget', 'budgets')} · ${nWord(blocked, 'node', 'nodes')} can’t be drained now`
    }
    case 'quota': {
      const stalls = m.rollouts.filter((r) => r.stalls).length
      return `${nWord(m.quotas.length, 'quota', 'quotas')} · ${nWord(m.limits.length, 'limit range', 'limit ranges')}${stalls ? ` · ${nWord(stalls, 'rollout', 'rollouts')} would stall` : ''}`
    }
    case 'psa':
      return `baseline or stricter in ${m.namespaces.filter(enforced).length} of ${m.namespaces.length} namespaces`
    case 'webhooks':
      return `${nWord(m.hooks.length, 'webhook', 'webhooks')} · ${m.hooks.filter((h) => h.failurePolicy === 'Fail').length} fail closed`
    case 'prio':
      return `${nWord(m.priorities.length, 'class', 'classes')} · ${nWord(m.priorities.reduce((a, p) => a + p.pods, 0), 'pod uses', 'pods use')} one`
  }
}

export const PSA_TONE: Record<string, string> = { restricted: 'ok', baseline: 'info', privileged: 'warn', '': 'muted' }
export const PSA_RANK: Record<string, number> = { '': 0, privileged: 1, baseline: 2, restricted: 3 }

/** The level stricter than the one enforced: what a tightening would check. */
export function nextLevel(n: PolicyNSRow): 'baseline' | 'restricted' | null {
  if (!n.enforce || n.enforce === 'privileged') return 'baseline'
  if (n.enforce === 'baseline') return 'restricted'
  return null
}

export const VERDICT: Record<DrainRow['verdict'], { text: string; tone: string }> = {
  blocked: { text: 'Blocked', tone: 'err' },
  waits: { text: 'Waits, then drains', tone: 'info' },
  clean: { text: 'Drains cleanly', tone: 'ok' },
}

/** "minAvailable 1" etc. */
export function qtyNice(resource: string, q: string): string {
  return resource.endsWith('cpu') ? cpuText(q) : q
}

function cpuText(q: string): string {
  const m = /^(\d+(?:\.\d+)?)(m?)$/.exec(q)
  if (!m) return q
  const cores = m[2] === 'm' ? Number(m[1]) / 1000 : Number(m[1])
  return `${Math.round(cores * 100) / 100} ${cores === 1 ? 'core' : 'cores'}`
}
