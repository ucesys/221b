import { useMemo, useState } from 'react'
import { useView } from '../api/live'
import type { StorageRow } from '../api/storage'
import { compareNatural, fmtAge } from '../lib/format'
import { peekLink } from '../lib/peek'
import { byClass, bytes, bytesTotal, pairPeek, provisionerName, provisionerNote, replicas, storageData, usageTone } from '../lib/volumes'
import { Drawer } from './Drawer'
import { useWatsonApi } from './Watson'
import { YamlView } from './YamlView'

interface Props {
  name: string
  now: number
  onClose: () => void
  onToast: (msg: string) => void
}

const DEFAULT_ANNOTATION = 'storageclass.kubernetes.io/is-default-class'

/** The Watson commands that change the default class (ST-18): the annotation, overwritten. */
export function defaultCommand(name: string, on: boolean): string {
  return `kubectl annotate storageclass ${name} ${DEFAULT_ANNOTATION}=${on} --overwrite`
}

function LayersIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden="true">
      <path d="M8 2 14 5 8 8 2 5l6-3ZM2 8l6 3 6-3M2 11l6 3 6-3" />
    </svg>
  )
}

/**
 * The drawer of a storage class (ST-17): how it provisions, its
 * parameters, its capacity, and a warning while a claim of it fails to
 * provision; the default changes through Watson (ST-18).
 */
export function StorageClassPanel({ name, now, onClose, onToast }: Props) {
  const { rows, loaded } = useView<StorageRow>('storage')
  const data = useMemo(() => storageData(rows), [rows])
  const watson = useWatsonApi()
  const [tab, setTab] = useState('overview')
  const c = data.classes.find((x) => x.name === name)
  const group = useMemo(() => (c ? byClass([c], data.pairs.filter((p) => p.class === name))[0] : undefined), [c, data.pairs, name])
  if (!c || !group) {
    return (
      <Drawer label="Storage class details" kicker="StorageClass · cluster-scoped" title={name} onClose={onClose} width={640} testId="storage-class-panel">
        <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>{loaded ? `No storage class named ${name}, or your role does not read classes.` : 'Reading the storage classes…'}</p>
      </Drawer>
    )
  }
  const t = group.totals
  const params = Object.entries(c.parameters ?? {}).sort((a, b) => compareNatural(a[0], b[0]))
  const n = replicas(c)
  const byNs = new Map<string, { claims: number; pending: number }>()
  for (const p of group.pairs) {
    if (!p.claim || !p.namespace) continue
    const e = byNs.get(p.namespace) ?? { claims: 0, pending: 0 }
    e.claims++
    if (p.state === 'pending') e.pending++
    byNs.set(p.namespace, e)
  }
  const nsList = [...byNs.entries()].sort((a, b) => b[1].claims - a[1].claims || compareNatural(a[0], b[0]))
  const failing = group.pairs.filter((p) => p.state === 'pending' && p.reason?.tone === 'err')
  const canChange = watson.available && !watson.readOnly
  const toggleDefault = () => {
    watson.send(defaultCommand(c.name, !c.default), { from: `Storage class ${c.name}`, why: c.default ? `${c.name} stops being the default class.` : `${c.name} becomes the default class.`, over: true })
    onToast('Watson has the change: it runs once someone approves it.')
  }
  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'volumes', label: 'Volumes', count: t.volumes },
    { id: 'parameters', label: 'Parameters', count: params.length },
    { id: 'yaml', label: 'YAML' },
  ]
  const facts = (
    <>
      <span>
        Volumes <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{t.volumes}</b>
      </span>
      <span>
        Provisioned <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{bytesTotal(t.provisioned)}</b>
      </span>
      <span>
        Used <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{bytesTotal(t.used)}</b>
      </span>
      <span>
        Claims <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{t.claims}</b>
        {t.pending + t.waiting ? `, ${t.pending + t.waiting} pending` : ''}
      </span>
      <span>
        Age <b style={{ color: 'var(--fg)', fontWeight: 500 }}>{fmtAge(c.created, now)}</b>
      </span>
    </>
  )
  const footer = (
    <div className="rk-st-foot">
      <span className="rk-hint">Changing the default is two Watson changes: remove it here, then set it on the other class. Bound claims keep their class either way. 221B does not delete classes.</span>
      {canChange && (
        <button className="rk-btn" onClick={toggleDefault} data-testid="storage-class-default">
          {c.default ? 'Remove default' : 'Set as default'}
        </button>
      )}
    </div>
  )
  const usedPct = t.usedPct
  return (
    <Drawer
      label="Storage class details"
      kicker={
        <>
          <LayersIcon />
          StorageClass · cluster-scoped · provisioner <span className="rk-mono">{c.provisioner}</span>
          {c.default && (
            <span className="rk-tag" data-tone="accent" style={{ marginLeft: 4 }}>
              default
            </span>
          )}
        </>
      }
      title={c.name}
      facts={facts}
      tabs={tabs}
      tab={tab}
      onTab={setTab}
      onClose={onClose}
      footer={footer}
      width={640}
      testId="storage-class-panel"
    >
      {tab === 'overview' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <section className="rk-card rk-st-card" aria-label="How it provisions">
            <h3>How it provisions</h3>
            <dl className="rk-kv">
              <dt>Provisioner</dt>
              <dd>
                <span className="rk-mono">{c.provisioner}</span> — {provisionerNote(c.provisioner)}
              </dd>
              <dt>Reclaim policy</dt>
              <dd>
                <b>{c.reclaim}</b> — {c.reclaim === 'Delete' ? 'deleting a claim deletes its volume and the data' : 'deleting a claim keeps its volume and the data, Released'}
              </dd>
              <dt>Binding mode</dt>
              <dd>
                <b>{c.binding}</b> — {c.binding === 'WaitForFirstConsumer' ? 'the volume is made, or chosen, when a pod that mounts the claim is scheduled, on that pod’s node' : 'the volume is created as soon as the claim is, before any pod'}
              </dd>
              <dt>Volume expansion</dt>
              <dd>{c.expansion ? <><b>Allowed</b> — online, while mounted</> : <><b>Not allowed</b> — a bigger volume means a new claim and a copy</>}</dd>
              <dt>Mount options</dt>
              <dd className={c.mountOptions?.length ? 'rk-mono' : undefined}>{c.mountOptions?.length ? c.mountOptions.join(', ') : 'none'}</dd>
              <dt>Allowed topologies</dt>
              <dd className={c.topologies?.length ? 'rk-mono' : undefined}>{c.topologies?.length ? c.topologies.join('; ') : 'any node'}</dd>
              <dt>Default class</dt>
              <dd>
                {c.default ? (
                  <>
                    <b>Yes</b> — claims that name no class get <span className="rk-mono">{c.name}</span>
                  </>
                ) : (
                  <>
                    <b>No</b> — claims get it only by naming it
                  </>
                )}
              </dd>
            </dl>
          </section>
          <div className="rk-st-cards2">
            <section className="rk-card rk-st-card" aria-label="Parameters">
              <h3>Parameters</h3>
              {params.length ? (
                <dl className="rk-kv">
                  {params.map(([k, v]) => (
                    <div key={k} style={{ display: 'contents' }}>
                      <dt>
                        <span className="rk-mono">{k}</span>
                      </dt>
                      <dd>{v}</dd>
                    </div>
                  ))}
                </dl>
              ) : (
                <p>None: the provisioner’s defaults.</p>
              )}
            </section>
            <section className="rk-card rk-st-card" aria-label="Capacity">
              <h3>Capacity</h3>
              <div className="rk-st-meter">
                <span data-tone={usageTone(usedPct) === 'ok' ? undefined : usageTone(usedPct)} style={{ width: `${Math.min(100, usedPct).toFixed(1)}%` }} />
              </div>
              <p>
                <b>{bytesTotal(t.used)}</b> used of <b>{bytesTotal(t.provisioned)}</b> provisioned, {t.volumes} {t.volumes === 1 ? 'volume' : 'volumes'}.
                {n > 1 && ` Each volume is stored ${n} times, so the disks hold about ${bytesTotal(t.provisioned * n)} for this class.`}
              </p>
              {nsList.length > 0 && (
                <p>
                  Claims by namespace:{' '}
                  {nsList.map(([ns, e], i) => (
                    <span key={ns}>
                      {i > 0 && ' · '}
                      {ns} <b>{e.claims}</b>
                      {e.pending ? ` (${e.pending} pending)` : ''}
                    </span>
                  ))}
                </p>
              )}
            </section>
          </div>
          {failing.length > 0 && (
            <div className="rk-callout" data-tone="warn" data-testid="storage-class-failing">
              <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden="true">
                <path d="M8 2.2 14.3 13H1.7L8 2.2ZM8 6.5v3M8 11.6h.01" />
              </svg>
              <span>
                Provisioning is failing for{' '}
                {failing.map((p, i) => (
                  <span key={p.claim}>
                    {i > 0 && ', '}
                    <a className="rk-mono" {...peekLink({ kind: 'claim', namespace: p.namespace!, name: p.claim! })}>
                      {p.namespace}/{p.claim}
                    </a>
                  </span>
                ))}
                : {failing[0]!.reason!.text.replace(/^provisioning failed · /, '')}. New claims in this class will fail the same way until {/space/i.test(failing[0]!.reason!.text) ? 'space is freed' : 'the cause is fixed'}.
              </span>
            </div>
          )}
        </div>
      )}
      {tab === 'volumes' && (
        <table className="rk-st-ev" aria-label={`Volumes and claims of ${c.name}`} data-testid="storage-class-volumes">
          <thead>
            <tr>
              <th>Volume</th>
              <th>Claim</th>
              <th>Status</th>
              <th className="rk-r">Capacity</th>
              <th className="rk-r">Used</th>
            </tr>
          </thead>
          <tbody>
            {group.pairs.map((p) => (
              <tr key={pairPeek(p)}>
                <td className="rk-mono">{p.state === 'pending' ? <span className="rk-dim">none yet</span> : p.volume}</td>
                <td className="rk-mono">
                  <a {...(p.claim ? peekLink({ kind: 'claim', namespace: p.namespace!, name: p.claim }) : peekLink({ kind: 'volume', name: p.volume! }))}>{p.claim ? `${p.namespace}/${p.claim}` : p.wasClaim ? `was ${p.wasClaim}` : '—'}</a>
                </td>
                <td>
                  <span className="rk-pill" data-tone={p.tone}>
                    <span className="rk-dot" />
                    {p.phase}
                  </span>
                </td>
                <td className="rk-r rk-num">{bytes(p.state === 'pending' ? p.requested : p.capacity)}</td>
                <td className="rk-r rk-num">{p.usage ? `${Math.round(p.usage.pct)}%` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {tab === 'parameters' && (
        <dl className="rk-kv">
          {params.map(([k, v]) => (
            <div key={k} style={{ display: 'contents' }}>
              <dt>
                <span className="rk-mono">{k}</span>
              </dt>
              <dd className="rk-mono">{v}</dd>
            </div>
          ))}
          {!params.length && <dd>None: {provisionerName(c.provisioner, c.name)}’s defaults.</dd>}
        </dl>
      )}
      {tab === 'yaml' && <YamlView kind="storageclasses" name={c.name} onToast={onToast} />}
    </Drawer>
  )
}
