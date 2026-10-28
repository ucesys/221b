import { useMemo, useState, type ReactNode } from 'react'
import { useDetails } from '../api/details'
import type { ClusterInfo, ContainerDetail, ContainerRun, PodDetails, PodView, Tone as ToneKind } from '../api/types'
import { Tone } from '../columns/cells'
import { fCores, fMem, fmtAge, fmtTs, nWord } from '../lib/format'
import { apiPath, href } from '../lib/router'
import { Card, Chips, Drawer, Facts } from './Drawer'
import { EventsTable } from './EventsTable'
import { PodIcon, WarnIcon } from './Icons'
import { PodLogs } from './PodLogs'
import { ScrollBox, Table, TR } from './Table'
import { YamlView } from './YamlView'
import { ToolsButton } from './toolbox/ToolsMenu'
import { objectLink, peekLink } from '../lib/peek'
import { podQuestion } from '../lib/sherlock'

interface Props {
  podKey: string
  /** The row from the table, for the header while the details load. */
  row: PodView | null
  /** The pod is no longer in the cluster. */
  gone: boolean
  cluster: ClusterInfo | null
  now: number
  onClose: () => void
  onToast: (msg: string) => void
}

type DrawerTabId = 'overview' | 'containers' | 'environment' | 'volumes' | 'events' | 'logs' | 'yaml'

function cores(milli: number): string {
  return milli ? (milli < 1000 ? milli + 'm' : fCores(milli)) : '—'
}

function runText(s: ContainerRun | null, now: number): { text: string; tone: ToneKind; sub: string } {
  if (!s) return { text: 'not started', tone: 'muted', sub: '' }
  switch (s.kind) {
    case 'running':
      return { text: 'Running', tone: 'ok', sub: s.started ? `since ${fmtAge(s.started, now)} ago` : '' }
    case 'waiting':
      return { text: s.reason || 'Waiting', tone: s.reason === 'CrashLoopBackOff' || s.reason === 'ImagePullBackOff' || s.reason === 'ErrImagePull' || s.reason === 'CreateContainerConfigError' ? 'err' : 'warn', sub: '' }
    case 'terminated':
      return { text: s.reason || 'Terminated', tone: s.exitCode === 0 ? 'muted' : 'err', sub: `exit ${s.exitCode ?? '?'}${s.finished ? ` · ${fmtAge(s.finished, now)} ago` : ''}` }
  }
}

function Code({ text }: { text: string }) {
  return (
    <pre className="rk-mono" style={{ margin: 0, padding: '8px 12px', borderRadius: 8, background: 'var(--sunken)', fontSize: 12, lineHeight: 1.6, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
      {text}
    </pre>
  )
}

function ContainerCard({ c, now, usage }: { c: ContainerDetail; now: number; usage: ReactNode }) {
  const run = runText(c.state, now)
  const last = c.lastState ? runText(c.lastState, now) : null
  const facts: [string, ReactNode, string?][] = [
    ['Image', c.image],
    ...(c.imageID ? ([['Image ID', c.imageID]] as [string, ReactNode][]) : []),
    ...(c.pullPolicy ? ([['Pull policy', c.pullPolicy]] as [string, ReactNode][]) : []),
    ['Ports', c.ports.length ? c.ports.map((p) => `${p.name ? p.name + ' ' : ''}${p.port}/${p.protocol}${p.hostPort ? ` (host ${p.hostPort})` : ''}`).join(', ') : 'none'],
    ['Requests', `CPU ${cores(c.requests.cpu)} · memory ${c.requests.memory ? fMem(c.requests.memory) : '—'}`],
    ['Limits', `CPU ${cores(c.limits.cpu)} · memory ${c.limits.memory ? fMem(c.limits.memory) : '—'}`],
    ['Probes', c.probes.length ? c.probes.join('\n') : 'none'],
    ['Security', c.securityContext.length ? c.securityContext.join(', ') : 'defaults'],
    ['Restarts', String(c.restarts)],
  ]
  return (
    <Card label={`Container ${c.name}`}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 10px' }}>
        {c.init && <span className="rk-tag">{c.sidecar ? 'sidecar' : 'init'}</span>}
        <span className="rk-mono" style={{ fontSize: 14, fontWeight: 600 }}>
          {c.name}
        </span>
        <span className="rk-pill" data-tone={run.tone}>
          <span className="rk-dot" />
          {run.text}
        </span>
        <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }}>
          {run.sub}
        </span>
        {c.ready && <span style={{ fontSize: 12, color: 'var(--ok)' }}>ready</span>}
        {usage}
      </div>
      {c.state?.message && <Code text={c.state.message} />}
      <Facts items={facts} />
      {last && (
        <div className="rk-callout" data-tone={last.tone === 'err' ? 'err' : 'warn'}>
          <WarnIcon />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
            <strong style={{ fontSize: 12.5, fontWeight: 600 }}>
              Last run: {last.text} {last.sub}
            </strong>
            {c.lastState?.message && (
              <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)', overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>
                {c.lastState.message}
              </span>
            )}
          </div>
        </div>
      )}
      {(c.command?.length || c.args?.length || c.workingDir) && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Command</span>
          <Code text={[...(c.command ?? []), ...(c.args ?? [])].join(' ') + (c.workingDir ? `\n(in ${c.workingDir})` : '')} />
        </div>
      )}
      {c.mounts.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Mounts</span>
          <ul className="rk-mono" style={{ margin: 0, paddingLeft: 18, fontSize: 12, lineHeight: 1.6 }}>
            {c.mounts.map((m) => (
              <li key={m.path}>
                {m.path} <span style={{ color: 'var(--fg3)' }}>← {m.volume}{m.subPath ? '/' + m.subPath : ''}{m.readOnly ? ' (read-only)' : ''}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  )
}

/** The pod details drawer (P2-04): Overview, Containers, Environment, Volumes, Events, Logs and YAML. */
export function PodPanel({ podKey, row, gone, cluster, now, onClose, onToast }: Props) {
  const [ns, name] = podKey.split('/', 2) as [string, string]
  const [tab, setTab] = useState<DrawerTabId>('overview')
  const details = useDetails<PodDetails>(apiPath(`pods/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`))
  const d = details.data
  // Deleted while open: the row left the table, or the server says 404.
  // A row missing from the table is a deleted pod, unless Kubernetes
  // refused this person the details: then it is one they may not see.
  const deleted = (gone || details.gone) && !details.forbidden
  const envCount = useMemo(() => (d ? d.containers.reduce((n, c) => n + c.env.length + c.envFrom.length, 0) : 0), [d])
  const head = d ?? row
  const status = head ? { tone: head.tone, text: head.status, tip: head.statusTip } : null
  const metrics = cluster?.metrics === 'available'

  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'containers', label: 'Containers', count: d?.containers.length },
    { id: 'environment', label: 'Environment', count: envCount },
    { id: 'volumes', label: 'Volumes', count: d?.volumes.length },
    { id: 'events', label: 'Events', count: d?.events.length },
    { id: 'logs', label: 'Logs' },
    { id: 'yaml', label: 'YAML' },
  ]

  const usageLine = d?.usage ? `${cores(d.usage.cpuMilli)} CPU · ${fMem(d.usage.memoryBytes)} memory in use` : metrics ? 'no reading from metrics-server' : (cluster?.metricsMessage ?? 'usage not available')

  return (
    <Drawer
      ask={podQuestion(head ? { ...head, namespace: ns, name } : null, ns, name)}
      label="Pod details"
      testId="pod-panel"
      tools={<ToolsButton target={{ kind: 'pod', name, namespace: ns, problem: status && (status.tone === 'err' || status.tone === 'warn') ? status.text : undefined }} />}
      kicker={
        <>
          <PodIcon />
          <span>Pod in</span>
          <a className="rk-link rk-mono" href={href('namespaces', [ns])} title="Show everything in this namespace" style={{ fontSize: 11.5 }}>
            {ns}
          </a>
        </>
      }
      title={name}
      facts={
        head && (
          <>
            {status && (
              <span className="rk-pill" data-tone={status.tone} title={status.tip}>
                <span className="rk-dot" />
                {status.text}
              </span>
            )}
            <span>
              Ready{' '}
              <span className="rk-num" style={{ color: 'var(--fg)' }}>
                {head.readyUnknown ? '?' : head.ready}/{head.total}
              </span>
            </span>
            <span>
              Restarts{' '}
              <span className="rk-t rk-num" data-tone={head.restarts ? 'warn' : 'muted'} style={{ fontWeight: 500 }}>
                {head.restarts}
              </span>
            </span>
            <span>
              Started{' '}
              <span className="rk-num" style={{ color: 'var(--fg)' }} title={head.started ? fmtTs(head.started) : undefined}>
                {head.started ? fmtAge(head.started, now) + ' ago' : '—'}
              </span>
            </span>
            {head.node ? (
              <span>
                Node{' '}
                <a className="rk-link rk-mono" {...peekLink({ kind: 'node', name: head.node })} style={{ fontSize: 12 }} data-testid="pod-node-link">
                  {head.node}
                </a>
              </span>
            ) : (
              <span>
                Node{' '}
                <span className="rk-t" data-tone="warn" style={{ fontWeight: 500 }}>
                  not scheduled
                </span>
              </span>
            )}
            {head.ownerKind && (
              <span>
                {head.ownerKind}{' '}
                <a className="rk-link rk-mono" {...objectLink('namespaces', [ns], { open: `${head.ownerKind}/${head.ownerName}` })} style={{ fontSize: 12 }} title={`Open the ${head.ownerKind}`} data-testid="pod-owner-link">
                  {head.ownerName}
                </a>
              </span>
            )}
            {head.release && (
              <span>
                Helm release{' '}
                <a className="rk-link rk-mono" href={href('helm', [ns, head.release])} style={{ fontSize: 12 }} data-testid="pod-release-link">
                  {head.release}
                </a>
              </span>
            )}
            {d && d.services.length > 0 && (
              <span>
                {nWord(d.services.length, 'Service', 'Services')}{' '}
                {d.services.map((s, i) => (
                  <span key={s}>
                    {i > 0 && ', '}
                    <a className="rk-link rk-mono" {...peekLink({ kind: 'service', namespace: ns, name: s })} style={{ fontSize: 12 }} data-testid="pod-service-link">
                      {s}
                    </a>
                  </span>
                ))}
              </span>
            )}
          </>
        )
      }
      tabs={tabs}
      tab={tab}
      onTab={(t) => setTab(t as DrawerTabId)}
      onClose={onClose}
      notice={
        deleted && (
          <div className="rk-callout" data-tone="warn" data-peek-error={d ? undefined : ''} style={{ margin: '12px 20px 0', borderRadius: 8 }} data-testid="pod-deleted">
            <WarnIcon />
            <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
              <strong style={{ fontSize: 12.5, fontWeight: 600 }}>This pod was deleted</strong>
              <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{d ? 'What is shown is the last state 221B saw. Logs can no longer be read.' : 'It is gone from the cluster, and nothing more is known about it.'}</span>
            </div>
          </div>
        )
      }
    >
      {details.error && !deleted && !d && (
        <p className="rk-callout" data-tone="warn" data-peek-error="" style={{ margin: 0, fontSize: 12.5 }}>
          {details.error}
        </p>
      )}
      {!d && !details.error && <p data-peek-loading="" style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Loading…</p>}
      {d && tab === 'overview' && (
        <>
          {d.unschedulable && (
            <div className="rk-callout" data-tone="warn">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Not scheduled</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)', whiteSpace: 'pre-wrap' }}>{d.unschedulable}</span>
              </div>
            </div>
          )}
          {d.message && (
            <div className="rk-callout" data-tone="warn">
              <WarnIcon />
              <span style={{ fontSize: 12.5, color: 'var(--fg2)', whiteSpace: 'pre-wrap' }}>{d.message}</span>
            </div>
          )}
          {d.lastTermination && (
            <div className="rk-callout" data-tone="err">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Last restart: {d.lastTermination}</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{d.lastRestart ? `${fmtAge(d.lastRestart, now)} ago · ${fmtTs(d.lastRestart)}` : ''}. The previous container’s logs are under Logs.</span>
              </div>
            </div>
          )}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(390px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <Card title="Pod">
              <Facts
                items={[
                  ['Namespace', d.namespace],
                  ['Node', d.node || 'not scheduled'],
                  ['Pod IP', d.podIP || '—'],
                  ['Host IP', d.hostIP || '—'],
                  ['Started', d.started ? `${fmtTs(d.started)} · ${fmtAge(d.started, now)} ago` : '—'],
                  ['Created', `${fmtTs(d.created)} · ${fmtAge(d.created, now)} ago`],
                  ['QoS class', d.qos || '—'],
                  ['Priority', `${d.priority}${d.priorityClass ? ` (${d.priorityClass})` : ''}`],
                  ['Service account', d.serviceAccount || 'default'],
                  ['Controlled by', d.owners.length ? d.owners.map((o) => `${o.kind}/${o.name}`).join(' → ') : 'nothing: a bare pod'],
                  ['Helm release', d.release || '—'],
                  ['Restart policy', d.restartPolicy],
                  ['Host network', d.hostNetwork ? 'yes' : 'no'],
                  ['In use', usageLine],
                  ['UID', d.uid],
                ]}
              />
            </Card>
            <Card title="Conditions">
              <div style={{ display: 'flex', flexDirection: 'column' }}>
                {d.conditions.map((c) => (
                  <div key={c.type} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '6px 0', borderBottom: '1px solid var(--line)', fontSize: 12.5 }}>
                    <Tone tone={c.status === 'True' ? 'ok' : c.status === 'False' ? 'warn' : 'muted'} text={c.status} />
                    <span className="rk-mono" style={{ flex: '1 1 auto' }} title={[c.reason, c.message].filter(Boolean).join(': ')}>
                      {c.type}
                      {c.reason && <span style={{ color: 'var(--fg3)' }}> · {c.reason}</span>}
                    </span>
                    <span className="rk-num" style={{ color: 'var(--fg3)', fontSize: 12 }}>
                      {c.since ? fmtAge(c.since, now) + ' ago' : ''}
                    </span>
                  </div>
                ))}
                {d.conditions.length === 0 && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>None yet</span>}
              </div>
              <h3 className="rk-h" style={{ marginTop: 4 }}>
                Scheduling
              </h3>
              <dl className="rk-kv">
                <dt>Node selector</dt>
                <dd>
                  <span style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                    {Object.keys(d.nodeSelector).length ? Object.entries(d.nodeSelector).map(([k, v]) => <span key={k} className="rk-chip">{`${k}=${v}`}</span>) : <span className="rk-chip">none</span>}
                  </span>
                </dd>
                <dt>Tolerations</dt>
                <dd>
                  <span style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                    {d.tolerations.length ? d.tolerations.map((t) => <span key={t} className="rk-chip">{t}</span>) : <span className="rk-chip">none</span>}
                  </span>
                </dd>
                <dt>Affinity</dt>
                <dd>
                  <span style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                    {d.affinity.length ? d.affinity.map((a) => <span key={a} className="rk-chip">{a}</span>) : <span className="rk-chip">none</span>}
                  </span>
                </dd>
              </dl>
            </Card>
          </div>
          <Card title="Resources per container" pad={false}>
            <ScrollBox label="Resources per container">
              <Table label="Resources per container" template="minmax(190px, 1.6fr) 90px 90px 100px 100px" minWidth={570} cols={[{ label: 'Container' }, { label: 'CPU req', right: true }, { label: 'CPU limit', right: true }, { label: 'Mem req', right: true }, { label: 'Mem limit', right: true }]}>
                {d.containers.map((c) => (
                  <TR key={c.name}>
                    <span>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                        <span className="rk-mono">{c.name}</span>
                        {c.init && <span className="rk-tag">{c.sidecar ? 'sidecar' : 'init'}</span>}
                      </span>
                    </span>
                    <span className="rk-r rk-num">{cores(c.requests.cpu)}</span>
                    <span className="rk-r rk-num">{cores(c.limits.cpu)}</span>
                    <span className="rk-r rk-num">{c.requests.memory ? fMem(c.requests.memory) : '—'}</span>
                    <span className="rk-r rk-num">{c.limits.memory ? fMem(c.limits.memory) : '—'}</span>
                  </TR>
                ))}
              </Table>
            </ScrollBox>
            <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 11.5, color: 'var(--fg3)' }}>Pod in use: {usageLine}. metrics-server reports usage per pod; the per-container split is not available here.</p>
          </Card>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(390px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <Card title="Labels">
              <Chips map={d.labels} />
            </Card>
            <Card title="Annotations">
              {Object.keys(d.annotations).length === 0 ? (
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>None</p>
              ) : (
                <dl className="rk-kv" style={{ gridTemplateColumns: 'minmax(0, 1fr)', gap: 2 }}>
                  {Object.keys(d.annotations)
                    .sort()
                    .map((k) => (
                      <div key={k} style={{ display: 'contents' }}>
                        <dt className="rk-mono" style={{ fontSize: 11.5, marginTop: 6 }}>
                          {k}
                        </dt>
                        <dd className="rk-mono" style={{ fontSize: 12, whiteSpace: 'pre-wrap' }}>
                          {d.annotations[k]}
                        </dd>
                      </div>
                    ))}
                </dl>
              )}
            </Card>
          </div>
        </>
      )}
      {d && tab === 'containers' && (
        <>
          {d.containers.some((c) => c.init) && (
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 10px' }}>
              <h3 className="rk-h">Init containers</h3>
              <span style={{ fontSize: 12, color: 'var(--fg3)' }}>run to completion before the app containers start; sidecars keep running</span>
            </div>
          )}
          {d.containers.filter((c) => c.init).map((c) => <ContainerCard key={c.name} c={c} now={now} usage={null} />)}
          {d.containers.some((c) => c.init) && <h3 className="rk-h">Containers</h3>}
          {d.containers.filter((c) => !c.init).map((c) => <ContainerCard key={c.name} c={c} now={now} usage={null} />)}
        </>
      )}
      {d && tab === 'environment' && (
        <>
          {d.containers.map((c) => (
            <Card key={c.name} title={`${c.name}${c.init ? ' (init)' : ''}`} pad={false} label={`Environment of ${c.name}`}>
              {c.env.length === 0 && c.envFrom.length === 0 ? (
                <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>No environment variables set by the pod.</p>
              ) : (
                <ScrollBox label={`Environment of ${c.name}`}>
                  <Table label={`Environment of ${c.name}`} template="minmax(180px, 1fr) minmax(260px, 2fr)" minWidth={440} cols={[{ label: 'Name' }, { label: 'Value' }]}>
                    {c.env.map((e) => (
                      <TR key={e.name}>
                        <span className="rk-mono" style={{ fontSize: 12 }}>
                          {e.name}
                        </span>
                        <span>
                          {e.source === 'Secret' ? (
                            <span className="rk-chip" title="Taken from a Secret; 221B never shows Secret values (X-01)">
                              from Secret <em>{e.from}</em> key <em>{e.key}</em>
                              {e.optional ? ' (optional)' : ''}
                            </span>
                          ) : e.source === 'ConfigMap' ? (
                            <span className="rk-chip">
                              from ConfigMap <em>{e.from}</em> key <em>{e.key}</em>
                              {e.optional ? ' (optional)' : ''}
                            </span>
                          ) : e.source === 'field' ? (
                            <span className="rk-chip">
                              field <em>{e.from}</em>
                            </span>
                          ) : e.source === 'resource' ? (
                            <span className="rk-chip">
                              resource <em>{e.from}</em>
                            </span>
                          ) : (
                            <span className="rk-mono" style={{ fontSize: 12 }}>
                              {e.value ?? ''}
                            </span>
                          )}
                        </span>
                      </TR>
                    ))}
                    {c.envFrom.map((f, i) => (
                      <TR key={'from' + i}>
                        <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg3)' }}>
                          {f.prefix ? f.prefix + '*' : 'every key'}
                        </span>
                        <span>
                          <span className="rk-chip" title={f.source === 'Secret' ? 'Every key of the Secret becomes a variable; values are never shown' : 'Every key of the ConfigMap becomes a variable'}>
                            all keys of {f.source} <em>{f.name}</em>
                            {f.optional ? ' (optional)' : ''}
                          </span>
                        </span>
                      </TR>
                    ))}
                  </Table>
                </ScrollBox>
              )}
            </Card>
          ))}
        </>
      )}
      {d && tab === 'volumes' && (
        <>
          {d.volumes.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No volumes.</p>}
          {d.volumes.map((v) => (
            <Card key={v.name} label={`Volume ${v.name}`}>
              <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 10px' }}>
                <span className="rk-mono" style={{ fontSize: 13.5, fontWeight: 600 }}>
                  {v.name}
                </span>
                <span className="rk-tag">{v.type}</span>
                <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)' }}>
                  {v.source}
                </span>
                {v.claim && (
                  // ST-21: the claim's pill opens its drawer on the Storage tab.
                  <a className="rk-pill" data-tone={v.claim.phase === 'Bound' ? 'ok' : 'warn'} href={href('storage', [], { peek: `claim:${ns}/${v.claim.name}` })} title={`Open the claim ${v.claim.name} on the Storage tab`} style={{ textDecoration: 'none' }} data-testid="pod-claim-link">
                    <span className="rk-dot" />
                    {v.claim.phase}
                    {v.claim.capacity ? ` · ${v.claim.capacity}` : ''}
                    {v.claim.storageClass ? ` · ${v.claim.storageClass}` : ''}
                  </a>
                )}
              </div>
              {v.mounts.length > 0 ? (
                <ul className="rk-mono" style={{ margin: 0, paddingLeft: 18, fontSize: 12, lineHeight: 1.6 }}>
                  {v.mounts.map((m) => (
                    <li key={m.container + m.path}>
                      {m.container}: {m.path}
                      {m.subPath ? ` (subPath ${m.subPath})` : ''}
                      {m.readOnly ? ' (read-only)' : ''}
                    </li>
                  ))}
                </ul>
              ) : (
                <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Not mounted by any container.</span>
              )}
            </Card>
          ))}
        </>
      )}
      {d && tab === 'events' && <EventsTable events={d.events} now={now} />}
      {d && tab === 'logs' && <PodLogs namespace={ns} name={name} containers={d.containers} gone={deleted} onToast={onToast} />}
      {tab === 'yaml' && !deleted && <YamlView kind="pods" namespace={ns} name={name} onToast={onToast} />}
      {tab === 'yaml' && deleted && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The pod is gone, so there is no YAML to read.</p>}
    </Drawer>
  )
}
