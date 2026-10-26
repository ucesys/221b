import { useState, type ReactNode } from 'react'
import { useDetails } from '../api/details'
import { detailsPath, useStorageScope, type Seg, type StorageDetails, type StorageEvent, type StorageHop } from '../api/storage'
import type { ClusterInfo } from '../api/types'
import { agoText } from '../lib/alerts'
import { fmtAge, fmtMinutes, minutesSince, nWord } from '../lib/format'
import { peekLink } from '../lib/peek'
import { href } from '../lib/router'
import { bytes, provisionerName } from '../lib/volumes'
import { ConfirmDialog } from './ConfirmDialog'
import { Drawer } from './Drawer'
import { useSherlockApi } from './Sherlock'
import { useWatsonApi } from './Watson'
import { YamlView } from './YamlView'

export type StorageTarget = { kind: 'claim'; namespace: string; name: string } | { kind: 'volume'; name: string }

interface Props {
  target: StorageTarget
  cluster: ClusterInfo | null
  clusterId: string
  now: number
  onClose: () => void
  onToast: (msg: string) => void
}

function DiskIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <ellipse cx="8" cy="4" rx="5.5" ry="2.2" />
      <path d="M2.5 4v8c0 1.2 2.5 2.2 5.5 2.2s5.5-1 5.5-2.2V4M2.5 8c0 1.2 2.5 2.2 5.5 2.2S13.5 9.2 13.5 8" />
    </svg>
  )
}

/** Where a link of the chain goes. */
function segHref(l: NonNullable<Seg['l']>): { href: string; 'data-peek'?: string } {
  switch (l.kind) {
    case 'class':
      return peekLink({ kind: 'class', name: l.name })
    case 'volume':
      return peekLink({ kind: 'volume', name: l.name })
    case 'claim':
      return peekLink({ kind: 'claim', namespace: l.namespace ?? '', name: l.name })
    case 'pod':
      return peekLink({ kind: 'pod', namespace: l.namespace ?? '', name: l.name })
    case 'node':
      return peekLink({ kind: 'node', name: l.name })
    case 'statefulset':
      return peekLink({ kind: 'object', type: 'StatefulSet', namespace: l.namespace ?? '', name: l.name })
    case 'release':
      return { href: href('helm', [l.namespace ?? '', l.name]) }
    case 'namespace':
      return { href: href('namespaces', [l.name]) }
  }
  return { href: '#' }
}

/** Rich text from the server: bold, monospace and links. */
export function Segs({ segs }: { segs: Seg[] }) {
  return (
    <>
      {segs.map((s, i) => {
        let node: ReactNode = s.t
        if (s.l)
          node = (
            <a className={s.m ? 'rk-mono' : undefined} {...segHref(s.l)}>
              {s.t}
            </a>
          )
        else if (s.m) node = <span className="rk-mono">{s.t}</span>
        return s.b ? <b key={i}>{node}</b> : <span key={i}>{node}</span>
      })}
    </>
  )
}

function DiamondIcon() {
  return (
    <svg width="11" height="11" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <path d="M8 1.5 14.5 8 8 14.5 1.5 8Z" />
    </svg>
  )
}

/** The chain (ST-14), one hop per row. */
export function Chain({ hops }: { hops: StorageHop[] }) {
  return (
    <div className="rk-card" style={{ padding: 6 }}>
      <ol className="rk-st-chain" style={{ listStyle: 'none', margin: 0, padding: 0 }} aria-label="From the class down to the node" data-testid="storage-chain">
        {hops.map((h) => (
          <li key={h.key} className="rk-st-hop" data-tone={h.tone === 'ok' || h.tone === 'info' ? undefined : h.tone} data-hop={h.key} data-state={h.tone}>
            <span className="rk-st-n">{h.n}</span>
            <span className="rk-st-k">{h.label}</span>
            <span className="rk-st-v">
              <Segs segs={h.value} />
              {h.tag && (
                <span className="rk-tag" data-tone="accent" style={{ fontFamily: 'var(--font)', fontSize: 11, fontWeight: 500 }}>
                  {h.tag}
                </span>
              )}
            </span>
            <span className="rk-st-d">
              {h.meter !== undefined && (
                <span className="rk-st-meter" style={{ width: 160, display: 'inline-block', verticalAlign: 'middle', marginRight: 8 }}>
                  <span data-tone={h.tone === 'ok' ? undefined : h.tone} style={{ width: `${Math.min(100, h.meter).toFixed(1)}%` }} />
                </span>
              )}
              <Segs segs={h.desc} />
            </span>
            <span className="rk-st-s" data-tone={h.tone === 'ok' ? undefined : h.tone}>
              {h.tone === 'err' ? <DiamondIcon /> : h.state === '—' ? null : <span className="rk-dot" />}
              {h.state}
            </span>
          </li>
        ))}
      </ol>
    </div>
  )
}

/** The events table (ST-15). */
export function EventsTable({ events, now }: { events: StorageEvent[]; now: number }) {
  if (!events.length) return <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No events: Kubernetes keeps them for an hour.</p>
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="rk-st-ev" aria-label="Events about this claim and its pods" data-testid="storage-events">
        <thead>
          <tr>
            <th>Reason</th>
            <th>Message</th>
            <th className="rk-r">Count</th>
            <th className="rk-r">First seen</th>
            <th className="rk-r">Last seen</th>
            <th>Reported by</th>
          </tr>
        </thead>
        <tbody>
          {events.map((e, i) => (
            <tr key={i} data-reason={e.reason}>
              <td>
                <span className="rk-st-rsn" data-tone={e.tone === 'warn' ? undefined : e.tone}>
                  {e.reason}
                </span>
                {e.pod && <span className="rk-dim"> pod {e.pod}</span>}
              </td>
              <td>{e.message}</td>
              <td className="rk-r rk-num">×{e.count}</td>
              <td className="rk-r rk-num">{agoText(e.first, now)}</td>
              <td className="rk-r rk-num">{agoText(e.last, now)}</td>
              <td className="rk-mono">{e.reportedBy}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** A size Kubernetes reads: "150Gi", "1.5Ti", "500Mi". */
const SIZE = /^[1-9]\d*(\.\d+)?(Ki|Mi|Gi|Ti|Pi)$/

/** The bytes of such a size. */
export function sizeBytes(s: string): number {
  const m = SIZE.exec(s.trim())
  if (!m) return NaN
  const mult: Record<string, number> = { Ki: 2 ** 10, Mi: 2 ** 20, Gi: 2 ** 30, Ti: 2 ** 40, Pi: 2 ** 50 }
  return parseFloat(s) * mult[m[2]!]!
}

/** The Watson commands of the drawer's actions (ST-18). */
export function expandCommand(ns: string, name: string, size: string): string {
  return `kubectl patch pvc ${name} -n ${ns} --type merge -p '{"spec":{"resources":{"requests":{"storage":"${size}"}}}}'`
}

export function deleteClaimCommand(ns: string, name: string): string {
  return `kubectl delete pvc ${name} -n ${ns}`
}

/** A size half again as large, rounded to a whole Gi, for the Expand field. */
function nextSize(cap: number | undefined): string {
  if (!cap) return ''
  return `${Math.ceil((cap * 1.5) / 2 ** 30)}Gi`
}

/**
 * The drawer of a volume claim, or of a volume no claim holds (ST-13 to
 * ST-16): the chain from the class to the node, the events, and the
 * actions, each a Watson change (ST-18).
 */
export function StoragePanel({ target, clusterId, now, onClose, onToast }: Props) {
  const isClaim = target.kind === 'claim'
  const d = useDetails<StorageDetails>(detailsPath(clusterId, isClaim ? { namespace: target.namespace, claim: target.name } : { volume: target.name }))
  const scope = useStorageScope(clusterId)
  const watson = useWatsonApi()
  const sherlock = useSherlockApi()
  const [tab, setTab] = useState('overview')
  const [size, setSize] = useState<string | null>(null)
  const [asking, setAsking] = useState(false)
  const data = d.data
  const p = data?.pair
  const label = isClaim ? 'Claim details' : 'Volume details'
  if (!data || !p) {
    return (
      <Drawer label={label} kicker={isClaim ? `Volume claim in ${target.namespace}` : 'Volume'} title={target.name} onClose={onClose} testId="storage-panel">
        <p style={{ margin: 0, fontSize: 12.5, color: d.error ? 'var(--err)' : 'var(--fg3)' }}>{d.error || 'Reading the claim and its volume…'}</p>
      </Drawer>
    )
  }
  const ns = p.namespace ?? ''
  const warnings = data.events.filter((e) => e.type === 'Warning').length
  const pods = p.pods ?? []
  const volumeYaml = !!p.volume && p.state !== 'pending' && (scope?.volumes ?? true)
  const tabs = [
    { id: 'overview', label: 'Overview' },
    ...(isClaim ? [{ id: 'pods', label: 'Pods', count: pods.length }] : []),
    { id: 'events', label: 'Events', count: data.events.length },
    ...(isClaim ? [{ id: 'claim-yaml', label: 'Claim YAML' }] : []),
    ...(volumeYaml ? [{ id: 'volume-yaml', label: 'Volume YAML' }] : []),
  ]
  const failing = data.broken || p.state === 'pending' || p.state === 'lost' || p.state === 'failed'
  const since = p.since ? ` for ${fmtMinutes(minutesSince(p.since, now))}` : ''

  const kicker = isClaim ? (
    <>
      <DiskIcon />
      Volume claim in{' '}
      <a className="rk-mono rk-st-a" href={href('namespaces', [ns])}>
        {ns}
      </a>
      {p.volume && p.state !== 'pending' ? (
        <>
          {' '}
          · bound to{' '}
          {scope?.volumes ?? true ? (
            <a className="rk-mono rk-st-a" {...peekLink({ kind: 'volume', name: p.volume })}>
              {p.volume}
            </a>
          ) : (
            <span className="rk-mono">{p.volume}</span>
          )}
        </>
      ) : (
        ' · no volume yet'
      )}
    </>
  ) : (
    <>
      <DiskIcon />
      Volume{' '}
      {p.claim ? (
        <>
          · bound to{' '}
          <a className="rk-mono rk-st-a" {...peekLink({ kind: 'claim', namespace: ns, name: p.claim })} data-testid="storage-volume-claim">
            {ns}/{p.claim}
          </a>
        </>
      ) : p.wasClaim ? (
        <>
          · {p.state === 'bound' ? 'bound to' : 'released from'} <span className="rk-mono">{p.wasClaim}</span>
        </>
      ) : (
        `· ${p.phase}`
      )}
    </>
  )

  const mode = [p.volumeMode || 'Filesystem', p.source?.fsType || data.class?.parameters?.fsType].filter(Boolean).join(', ')
  const facts = (
    <>
      <span className="rk-pill" data-tone={p.tone}>
        <span className="rk-dot" />
        {p.phase}
        {p.state === 'bound' ? '' : since}
      </span>
      {p.state === 'pending' ? (
        <span>
          Requested <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{bytes(p.requested)}</b>
        </span>
      ) : (
        <span>
          Capacity <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{bytes(p.capacity)}</b>
        </span>
      )}
      {p.accessModes?.length ? (
        <span>
          Access <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{p.accessModes.join(', ')}</b>
        </span>
      ) : null}
      {p.state !== 'pending' && (
        <span>
          Mode <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{mode}</b>
        </span>
      )}
      <span>
        Class{' '}
        {p.class ? (
          data.class ? (
            <a className="rk-mono rk-st-a" {...peekLink({ kind: 'class', name: p.class })}>
              {p.class}
            </a>
          ) : (
            <span className="rk-mono" style={{ color: p.classMissing ? 'var(--err)' : 'var(--fg)' }}>
              {p.class}
              {p.classMissing ? ' (not found)' : ''}
            </span>
          )
        ) : (
          <b style={{ color: 'var(--fg)', fontWeight: 500 }}>none</b>
        )}
      </span>
      {p.owner && (
        <span>
          {p.owner.kind}{' '}
          {p.owner.kind === 'StatefulSet' ? (
            <a className="rk-mono rk-st-a" {...peekLink({ kind: 'object', type: 'StatefulSet', namespace: ns, name: p.owner.name })}>
              {p.owner.name}
            </a>
          ) : (
            <span className="rk-mono" style={{ color: 'var(--fg)' }}>
              {p.owner.name}
            </span>
          )}
        </span>
      )}
      {p.release && (
        <span>
          Release{' '}
          <a className="rk-mono rk-st-a" href={href('helm', [ns, p.release])}>
            {p.release}
          </a>
        </span>
      )}
      {p.state === 'bound' && (
        <span>
          Age <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{fmtAge(p.created, now)}</b>
        </span>
      )}
      {data.alert && (
        <span>
          Alert <a className="rk-st-a" href={href('alerts', [], { q: p.claim })}>open on the Alerts tab</a>
        </span>
      )}
    </>
  )

  // Sherlock's suggestions for a claim that fails (ST-16).
  const provisioner = data.class ? provisionerName(data.class.provisioner, data.class.name) : p.class || 'the provisioner'
  const firstPod = pods[0]?.name
  const questions = [`Which ${provisioner} disks are nearly full?`, 'Which Released or Available volumes could be reclaimed?', ...(firstPod ? [`What does ${firstPod} need the volume for?`] : [])]

  // ST-18: nothing is offered where Watson changes nothing (a read-only
  // role or deployment, RB-03, RB-32), or does not run.
  const canChange = watson.available && !watson.readOnly
  const fromSet = p.owner?.kind === 'StatefulSet' && p.owner.template
  const reclaim = p.reclaim || data.class?.reclaim || ''
  const expandable = isClaim && p.state === 'bound' && !!data.expansion
  const sizeNow = size ?? nextSize(p.capacity)
  const sizeOk = !Number.isNaN(sizeBytes(sizeNow)) && sizeBytes(sizeNow) > (p.capacity ?? 0)
  const send = (command: string, why: string) => {
    watson.send(command, { from: `Storage: ${ns}/${p.claim}`, why, over: true })
    onToast('Watson has the change: it runs once someone approves it.')
  }
  const lackOfSpace = /space|ResourceExhausted|insufficient/i.test(data.events.find((e) => e.reason === 'ProvisioningFailed')?.message ?? '')
  const hint: ReactNode =
    p.state === 'pending' && fromSet ? (
      <>A StatefulSet recreates a deleted claim from the same template, so {lackOfSpace ? `free ${provisioner} space first (reclaim a volume, add a disk)` : 'fix the cause first'} and the provisioner’s next retry binds it. Anything that changes goes through Watson: a server dry run, then your approval.</>
    ) : reclaim ? (
      <>
        Changes go through Watson: a server dry run, then your approval. Reclaim policy is <b>{reclaim}</b>, so deleting this claim {reclaim === 'Delete' ? 'deletes the volume and its data' : 'keeps the volume and its data, Released'}.
      </>
    ) : (
      <>Changes go through Watson: a server dry run, then your approval.</>
    )
  const footer =
    isClaim && canChange ? (
      <div className="rk-st-foot" data-testid="storage-actions">
        {expandable && (
          <div className="rk-st-act">
            <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Expand to</span>
            <span className="rk-field" style={{ width: 96 }}>
              <input className="rk-mono" value={sizeNow} onChange={(e) => setSize(e.target.value)} aria-label="New size" aria-invalid={!sizeOk} data-testid="storage-expand-size" />
            </span>
            <button className="rk-btn" disabled={!sizeOk} title={sizeOk ? `Asks Watson to grow the claim to ${sizeNow}` : 'A size larger than now, such as 150Gi'} onClick={() => send(expandCommand(ns, p.claim!, sizeNow.trim()), `Expand ${ns}/${p.claim} from ${bytes(p.capacity)} to ${sizeNow.trim()}.`)} data-testid="storage-expand">
              Expand
            </button>
          </div>
        )}
        {p.state === 'bound' && (
          <button className="rk-btn" disabled title={data.snapshots}>
            Snapshot
          </button>
        )}
        <span className="rk-hint">{hint}</span>
        <button className="rk-btn" data-danger="true" onClick={() => setAsking(true)} data-testid="storage-delete">
          Delete claim
        </button>
      </div>
    ) : isClaim ? (
      <div className="rk-st-foot">
        <span className="rk-hint">{watson.readOnly ? `Watson changes nothing on this cluster${watson.how ? `: ${watson.how}` : ''}.` : 'Expand and Delete go through Watson, which runs where Sherlock is on.'}</span>
      </div>
    ) : undefined

  return (
    <Drawer label={label} kicker={kicker} title={isClaim ? p.claim! : p.volume!} facts={facts} tabs={tabs} tab={tab} onTab={setTab} onClose={onClose} footer={footer} testId="storage-panel">
      {tab === 'overview' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <p className="rk-st-intro" data-testid="storage-intro">
            <Segs segs={data.intro} />
          </p>
          <Chain hops={data.hops} />
          {failing ? (
            <>
              <section className="rk-st-sec" aria-label="Events">
                <h3>
                  Events{' '}
                  <span className="rk-cnt" data-tone={warnings ? 'warn' : undefined}>
                    {data.events.length}
                  </span>
                </h3>
                <EventsTable events={data.events} now={now} />
              </section>
              {sherlock.available && (
                <div className="rk-st-chips" data-testid="storage-questions">
                  <span style={{ fontSize: 12, color: 'var(--fg3)', alignSelf: 'center' }}>Ask Sherlock:</span>
                  {questions.map((q) => (
                    <button key={q} className="rk-btn" onClick={() => sherlock.ask(q, true)} data-question={q}>
                      {q}
                    </button>
                  ))}
                </div>
              )}
            </>
          ) : (
            <div className="rk-st-cards2">
              <section className="rk-card rk-st-card" aria-label="Lifecycle">
                <h3>Lifecycle</h3>
                {data.lifecycle.map((l, i) => (
                  <p key={i}>
                    <Segs segs={l} />
                  </p>
                ))}
              </section>
              <section className="rk-card rk-st-card" aria-label="Recent events">
                <h3>
                  Recent events <span className="rk-cnt">{data.events.length}</span>
                </h3>
                <div className="rk-steps">
                  {data.events.slice(0, 2).map((e, i) => (
                    <span key={i} data-status={e.type === 'Warning' ? 'now' : 'done'}>
                      <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="m3 8.5 3 3 7-7" />
                      </svg>
                      <span className="rk-mono rk-dim">{e.last ? fmtAge(e.last, now) : ''}</span> {e.reason} — {e.message}
                    </span>
                  ))}
                </div>
                {data.noWarningDays >= 0 && <p>No warnings in the last {nWord(data.noWarningDays, 'day', 'days')}.</p>}
              </section>
            </div>
          )}
        </div>
      )}
      {tab === 'pods' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {pods.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No pod mounts this claim.</p>}
          {pods.length > 0 && (
            <table className="rk-st-ev" aria-label="Pods that mount the claim" data-testid="storage-pods">
              <thead>
                <tr>
                  <th>Pod</th>
                  <th>Mount</th>
                  <th>Status</th>
                  <th>Node</th>
                </tr>
              </thead>
              <tbody>
                {pods.map((x) => (
                  <tr key={x.name}>
                    <td>
                      <a className="rk-mono rk-st-a" {...peekLink({ kind: 'pod', namespace: ns, name: x.name })}>
                        {x.name}
                      </a>
                    </td>
                    <td>
                      {x.mountPath && <span className="rk-mono">{x.mountPath}</span>}
                      {x.mountPath && ' · '}
                      {x.readOnly ? 'read-only' : 'read-write'}
                      {x.subPath && <> · subPath <span className="rk-mono">{x.subPath}</span></>}
                      {x.container && <> · container <span className="rk-mono">{x.container}</span></>}
                      {x.note && <span className="rk-st-reason" data-tone="warn">{x.note}</span>}
                    </td>
                    <td>
                      <span className="rk-pill" data-tone={x.tone}>
                        <span className="rk-dot" />
                        {x.status}
                      </span>
                    </td>
                    <td className="rk-mono">{x.node ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
      {tab === 'events' && <EventsTable events={data.events} now={now} />}
      {tab === 'claim-yaml' && isClaim && <YamlView kind="persistentvolumeclaims" namespace={ns} name={p.claim!} onToast={onToast} />}
      {tab === 'volume-yaml' && p.volume && <YamlView kind="persistentvolumes" name={p.volume} onToast={onToast} />}
      {asking && isClaim && (
        <ConfirmDialog
          title={`Delete the claim ${ns}/${p.claim}?`}
          confirm="Send to Watson"
          danger
          onCancel={() => setAsking(false)}
          onConfirm={() => {
            setAsking(false)
            send(deleteClaimCommand(ns, p.claim!), `Delete the claim ${ns}/${p.claim}.`)
          }}
        >
          <p style={{ margin: 0 }}>
            {reclaim === 'Delete'
              ? `Reclaim policy Delete: Kubernetes deletes the volume ${p.volume ?? ''} and its data with the claim.`
              : reclaim === 'Retain'
                ? `Reclaim policy Retain: the volume ${p.volume ?? ''} and its data stay, Released, until someone reuses or deletes it.`
                : 'The claim has no volume yet: nothing else is deleted.'}
            {pods.some((x) => x.status === 'Running') ? ' A pod that mounts it keeps it until the pod goes: the claim is deleted when no pod uses it.' : ''}
          </p>
          {fromSet && (
            <p style={{ margin: 0 }}>
              It comes from the <span className="rk-mono">{p.owner!.template}</span> template of StatefulSet <span className="rk-mono">{p.owner!.name}</span>, which recreates it, empty, for its pod.
            </p>
          )}
          <p className="rk-fhint" style={{ margin: 0 }}>
            Watson shows a server dry run first; nothing is deleted until someone approves it, and on a production cluster with the rule on, a second person approves (P6-07).
          </p>
        </ConfirmDialog>
      )}
    </Drawer>
  )
}
