import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { NodeRow, Tone } from '../api/types'
import { clampPct, fmtAge, fmtTs, localTimes, nWord } from '../lib/format'
import { effectTone, isSystemLabel, resourceSections, roleTag, taintNote, type ResourceSection } from '../lib/nodes'
import { href } from '../lib/router'
import { CloseIcon } from './Icons'
import { ToolsButton } from './toolbox/ToolsMenu'
import { CopyName } from './CopyName'
import { AskSherlock } from './Sherlock'
import { FixCommand } from './Watson'
import { YamlView } from './YamlView'
import { copyText } from '../lib/clipboard'

interface Props {
  r: NodeRow
  metrics: boolean
  metricsNote: string
  /** Why uptime is not available, when it is not. */
  uptimeNote: string
  now: number
  onClose: () => void
  onToast?: (msg: string) => void
}

function podSummary(r: NodeRow): string {
  const parts = [nWord(r.pods.running, 'pod', 'pods')]
  if (r.pods.notReady) parts.push(`${r.pods.notReady} not ready`)
  if (r.pods.completed) parts.push(`${r.pods.completed} completed`)
  return parts.join(' · ')
}

const conditionLabels: Record<string, [string, string]> = {
  MemoryPressure: ['MemoryPressure: no', 'MemoryPressure'],
  DiskPressure: ['DiskPressure: no', 'DiskPressure'],
  PIDPressure: ['PIDPressure: no', 'PIDPressure'],
  NetworkUnavailable: ['NetworkUnavailable: no', 'NetworkUnavailable'],
}

function conditionTags(r: NodeRow): { label: string; tone: Tone; tip: string }[] {
  const out: { label: string; tone: Tone; tip: string }[] = []
  for (const c of r.conditions) {
    const tip = [c.reason, c.message, c.since ? `since ${fmtTs(c.since)}` : ''].filter(Boolean).join(': ')
    if (c.type === 'Ready') {
      out.unshift({ label: c.status === 'True' ? 'Ready' : c.status === 'False' ? 'NotReady' : 'Ready: unknown', tone: c.status === 'True' ? 'ok' : 'err', tip })
      continue
    }
    const l = conditionLabels[c.type]
    const on = c.status === 'True'
    out.push({ label: l ? (on ? l[1] : l[0]) : `${c.type}: ${c.status}`, tone: on ? (c.type === 'NetworkUnavailable' ? 'err' : 'warn') : 'muted', tip })
  }
  if (r.cordoned) out.push({ label: 'SchedulingDisabled', tone: 'warn', tip: localTimes(r.statusTip) })
  return out
}

/** What can be done to a node (6.3 review): through Watson, or copied. */
function nodeActions(r: NodeRow): { what: string; command: string }[] {
  return [
    r.cordoned
      ? { what: 'Let new pods be scheduled here again.', command: `kubectl uncordon ${r.name}` }
      : { what: 'Stop new pods being scheduled here; the pods running here stay.', command: `kubectl cordon ${r.name}` },
    { what: 'Move its pods to other nodes, for maintenance: it cordons the node first.', command: `kubectl drain ${r.name} --ignore-daemonsets --delete-emptydir-data` },
  ]
}

function copyCommand(text: string, onToast?: (m: string) => void) {
  void copyText(text).then((ok) => onToast?.(ok ? 'Copied the command' : 'Could not copy: the browser refused the clipboard.'))
}

function Section({ title, count, aside, children, label }: { title: string; count?: number | string; aside?: ReactNode; children: ReactNode; label?: string }) {
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label={label ?? title}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <h3 className="rk-h">{title}</h3>
        {count !== undefined && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{count}</span>}
        {aside && <span style={{ flex: '1 1 auto' }} />}
        {aside}
      </div>
      {children}
    </section>
  )
}

/** One resource as a plain section: a bar and its figures, line by line. */
function Resource({ s }: { s: ResourceSection }) {
  return (
    <Section title={s.label} label={`${s.label} resources`}>
      <div className="rk-bar" style={{ height: 8, borderRadius: 4 }} title={`${s.label}: ${s.lines.map((l) => `${l.k} ${l.v}`).join(', ')}`}>
        <span className="rk-req" style={{ width: `${clampPct(s.reqPct).toFixed(1)}%` }} />
        <span className="rk-use" data-tone={s.useTone} style={{ width: `${clampPct(s.usePct).toFixed(1)}%` }} />
      </div>
      <dl className="rk-kv" style={{ gridTemplateColumns: 'minmax(110px, max-content) minmax(0, 1fr)', gap: '5px 18px' }} data-testid={`resource-${s.label.toLowerCase()}`}>
        {s.lines.map((l) => (
          <div key={l.k} style={{ display: 'contents' }}>
            <dt>{l.k}</dt>
            <dd className="rk-num" style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
              <span className={l.tone ? 'rk-t' : undefined} data-tone={l.tone} style={{ fontWeight: 500 }}>
                {l.v}
              </span>
              {l.sub && <span style={{ color: 'var(--fg3)', fontSize: 12 }}>{l.sub}</span>}
            </dd>
          </div>
        ))}
      </dl>
      {s.note && (
        <p className="rk-t" data-tone={s.noteTone} style={{ margin: 0, fontSize: 11.5 }}>
          {s.note}
        </p>
      )}
    </Section>
  )
}

/** The details panel for one node. Changes to it go through Sherlock and Watson, not buttons here. */
export function NodePanel({ r, metrics, metricsNote, uptimeNote, now, onClose, onToast }: Props) {
  const closeRef = useRef<HTMLButtonElement>(null)
  const [showSys, setShowSys] = useState(false)
  const [tab, setTab] = useState<'details' | 'yaml'>('details')

  useEffect(() => {
    closeRef.current?.focus()
  }, [r.name])

  const labels = Object.keys(r.labels).sort()
  const sysCount = labels.filter(isSystemLabel).length
  const shownLabels = labels.filter((k) => showSys || !isSystemLabel(k))
  const tag = roleTag(r)
  const uptime = r.bootedAt ? `${fmtAge(r.bootedAt, now)} · booted ${fmtTs(r.bootedAt)}` : uptimeNote ? 'Not available' : '—'
  const system: [string, string, string?][] = [
    ['Uptime', uptime, r.bootedAt ? undefined : uptimeNote || 'The kubelet has not reported when the node booted.'],
    ['Internal IP', r.internalIP || '—'],
    ['Zone', r.zone || '—'],
    ['OS', r.os || '—'],
    ['Kernel', r.kernel || '—'],
    ['Kubelet', r.kubelet || '—'],
    ['Runtime', r.runtime || '—'],
    ['Architecture', r.arch || '—'],
    ['Joined', `${fmtTs(r.created)} · ${fmtAge(r.created, now)} ago`],
  ]
  const errors = r.problems.filter((p) => p.severity === 'error').length
  const warnings = r.problems.length - errors
  const problemCount = [errors ? nWord(errors, 'error', 'errors') : '', warnings ? nWord(warnings, 'warning', 'warnings') : ''].filter(Boolean).join(' · ')

  // Over the page, as every details pane is (asked at the Phase 4 review).
  return (
    <div style={{ position: 'fixed', top: 0, right: 0, bottom: 0, left: 0, zIndex: 40, display: 'flex', justifyContent: 'flex-end' }}>
      <button aria-label={`Close the details of ${r.name}`} tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <aside
        className="rk-npanel rk-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={`Node details: ${r.name}`}
        data-testid="node-panel"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: 'min(560px, 100%)', height: '100%', overflow: 'auto', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderLeft: '1px solid var(--line)', boxShadow: 'var(--shadow)' }}
      >
        <div style={{ padding: '18px 20px 14px', display: 'flex', flexDirection: 'column', gap: 10, borderBottom: '1px solid var(--line)' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
            <div style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>Node details</span>
              <h2 className="rk-mono" style={{ margin: 0, fontSize: 17, fontWeight: 600, lineHeight: 1.3, overflowWrap: 'anywhere' }}>
                {r.name}
                <CopyName text={r.name} />
              </h2>
            </div>
            <AskSherlock question={r.problems.length ? `What is wrong with node ${r.name}?` : `Tell me about node ${r.name}`} compact over />
            <ToolsButton target={{ kind: 'node', name: r.name, cordoned: r.cordoned, role: r.role === 'control-plane' ? 'control plane' : 'worker', problem: r.problems.length ? r.problems.map((p) => p.title).join('; ') : undefined }} />
            <button ref={closeRef} className="rk-icon-btn" aria-label="Close node details" title="Close (Esc)" onClick={onClose}>
              <CloseIcon />
            </button>
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 14px', fontSize: 12 }}>
            <a className="rk-link" href={href('pods', [], { node: r.name })} data-testid="node-pods-link">
              {nWord(r.pods.running, 'pod', 'pods')} on this node →
            </a>
            {/* ST-21: the volumes and claims on this node, on the Storage tab. */}
            <a className="rk-link" href={href('storage', [], { node: r.name })} data-testid="node-storage-link">
              Storage →
            </a>
            <nav aria-label="Node sections" style={{ display: 'flex', gap: '0 16px', marginLeft: 'auto' }}>
              <button className="rk-tab" aria-current={tab === 'details' ? 'page' : undefined} onClick={() => setTab('details')} style={{ height: 30 }} data-dtab="details">
                Details
              </button>
              <button className="rk-tab" aria-current={tab === 'yaml' ? 'page' : undefined} onClick={() => setTab('yaml')} style={{ height: 30 }} data-dtab="yaml">
                YAML
              </button>
            </nav>
          </div>
        </div>
        {tab === 'yaml' && (
          <div style={{ padding: '16px 20px 28px' }}>
            <YamlView kind="nodes" name={r.name} onToast={onToast} />
          </div>
        )}
        <div style={{ padding: '16px 20px 28px', display: tab === 'yaml' ? 'none' : 'flex', flexDirection: 'column', gap: 18 }}>
          <Section title="Status">
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }} data-testid="node-status">
              <span className="rk-pill" data-tone={r.tone} title={localTimes(r.statusTip)} style={{ height: 24, fontSize: 12.5 }}>
                <span className="rk-dot" />
                {r.status}
              </span>
              <span className="rk-role" data-role={r.role} title={tag.title}>
                {tag.text}
              </span>
              <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{podSummary(r)}</span>
            </div>
            <p style={{ margin: 0, fontSize: 12, color: 'var(--fg2)' }}>{localTimes(r.statusTip)}</p>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 }} aria-label="Conditions">
              {conditionTags(r).map((c) => (
                <span key={c.label} className="rk-tag" data-tone={c.tone} title={c.tip} style={{ fontSize: 11, padding: '2px 7px' }}>
                  {c.label}
                </span>
              ))}
            </div>
          </Section>
          {r.problems.length > 0 && (
            <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Problems">
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                <h3 className="rk-h">Problems</h3>
                <span className="rk-t" data-tone={errors ? 'err' : 'warn'} style={{ fontSize: 11.5, fontWeight: 500 }}>
                  {problemCount}
                </span>
              </div>
              {r.problems.map((pb) => (
                <div key={pb.title} className="rk-callout" data-tone={pb.severity === 'error' ? 'err' : 'warn'} style={{ flexDirection: 'column', gap: 6 }}>
                  <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{pb.title}</strong>
                  <span style={{ fontSize: 12, color: 'var(--fg2)' }}>{pb.summary}</span>
                  {pb.entries.map((e, i) => (
                    <div key={i} style={{ display: 'flex', flexDirection: 'column', gap: 3, width: '100%', paddingTop: 7, borderTop: '1px solid var(--line)' }}>
                      <span className="rk-t" data-tone={pb.severity === 'error' ? 'err' : 'warn'} style={{ fontSize: 11.5, fontWeight: 600 }}>
                        {e.reason}
                      </span>
                      {e.message && <span style={{ fontSize: 11.5, color: 'var(--fg2)', overflowWrap: 'anywhere' }}>{e.message}</span>}
                      <span style={{ fontSize: 11, color: 'var(--fg3)' }} title={e.since ? fmtTs(e.since) : undefined}>
                        {[e.object, e.since ? `since ${fmtAge(e.since, now)} ago` : ''].filter(Boolean).join(' · ')}
                      </span>
                    </div>
                  ))}
                </div>
              ))}
            </section>
          )}
          <Section title="Actions">
            {nodeActions(r).map((a) => (
              <div key={a.command} style={{ display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="node-action">
                <span style={{ fontSize: 12, color: 'var(--fg2)' }}>{a.what}</span>
                <FixCommand command={a.command} from={`Node ${r.name}`} why={a.what} onCopy={() => copyCommand(a.command, onToast)} />
              </div>
            ))}
          </Section>
          <Section title="Taints" count={r.taints.length}>
            {r.taints.length > 0 && (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {r.taints.map((t) => (
                  <span key={t.key + t.effect} className="rk-chip">
                    {t.key}
                    <em>{t.value ? '=' + t.value : ''}</em>
                    <span className="rk-tag" data-tone={effectTone(t.effect)}>
                      {t.effect}
                    </span>
                  </span>
                ))}
              </div>
            )}
            <p style={{ margin: 0, fontSize: 12, color: 'var(--fg3)' }}>{taintNote(r)}</p>
          </Section>
          <Section
            title="Labels"
            count={labels.length}
            aside={
              sysCount > 0 && (
                <button className="rk-link" onClick={() => setShowSys(!showSys)} style={{ fontSize: 12 }}>
                  {showSys ? 'Hide kubernetes.io labels' : `Show ${sysCount} kubernetes.io labels`}
                </button>
              )
            }
          >
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {shownLabels.map((k) => (
                <span key={k} className="rk-chip" data-dim={isSystemLabel(k)}>
                  {k}
                  <em>{r.labels[k] !== '' ? '=' + r.labels[k] : ''}</em>
                </span>
              ))}
              {shownLabels.length === 0 && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Only kubernetes.io labels.</span>}
            </div>
          </Section>
          {resourceSections(r, metrics, metricsNote).map((s) => (
            <Resource key={s.label} s={s} />
          ))}
          <Section title="System">
            <dl className="rk-kv">
              {system.map(([k, v, tip]) => (
                <div key={k} style={{ display: 'contents' }}>
                  <dt>{k}</dt>
                  <dd className="rk-mono" title={tip}>
                    {v}
                  </dd>
                </div>
              ))}
            </dl>
            {!r.bootedAt && uptimeNote && (
              <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>{uptimeNote}</p>
            )}
          </Section>
        </div>
      </aside>
    </div>
  )
}
