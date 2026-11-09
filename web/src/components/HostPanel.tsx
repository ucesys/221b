import { useEffect, useRef, useState } from 'react'
import { cleanupCommand, fetchHosts, removeHost } from '../api/hosts'
import type { NodeRow } from '../api/types'
import { copyText } from '../lib/clipboard'
import { fmtAge, fmtTs, localTimes, nWord } from '../lib/format'
import { roleTag } from '../lib/nodes'
import { useRoute } from '../lib/router'
import { ConfirmDialog } from './ConfirmDialog'
import { CopyName } from './CopyName'
import { HostDialog, type HostStep } from './HostDialog'
import { CloseIcon } from './Icons'
import { FactsSection, Section, useNodeParams } from './NodePanel'

interface Props {
  r: NodeRow
  now: number
  onClose: () => void
  onToast?: (msg: string) => void
}

/** Whether the person may manage hosts: a SuperAdmin, which the hosts' list answers. */
function useMayManage(cluster: string): boolean {
  const [may, setMay] = useState<{ cluster: string; ok: boolean } | null>(null)
  useEffect(() => {
    let gone = false
    fetchHosts(cluster).then(
      () => !gone && setMay({ cluster, ok: true }),
      () => !gone && setMay({ cluster, ok: false }),
    )
    return () => {
      gone = true
    }
  }, [cluster])
  return may?.cluster === cluster && may.ok
}

/**
 * The details of a host outside Kubernetes (Phase 12): how the console
 * reaches it, the key it pinned (the fingerprint, never the key), its
 * facts and variables as a node's, and, for a SuperAdmin, what takes it
 * further: checking its key, pairing it, removing it.
 */
export function HostPanel({ r, now, onClose, onToast }: Props) {
  const h = r.host!
  const { cluster } = useRoute()
  const closeRef = useRef<HTMLButtonElement>(null)
  const may = useMayManage(cluster)
  const params = useNodeParams(r.name)
  const [dialog, setDialog] = useState<HostStep | null>(null)
  const [removing, setRemoving] = useState<{ command: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    closeRef.current?.focus()
  }, [r.name])

  const tag = roleTag(r)
  const labels = Object.keys(r.labels).sort()
  const errors = r.problems.filter((p) => p.severity === 'error').length
  const at = `${h.address}:${h.port}`
  const connection: [string, string, string?][] = [
    ['Address', at],
    ['Login user', h.loginUser],
    ['Host key', h.fingerprint ?? 'Not confirmed yet', h.fingerprint ? 'SHA256 fingerprint of the pinned key. The key stays on the server.' : undefined],
    ['Key trusted', h.trustedAt ? `${fmtTs(h.trustedAt)}${h.trustedBy ? ` by ${h.trustedBy}` : ''}` : '—'],
    ['Paired', h.pairedAt ? `${fmtTs(h.pairedAt)}${h.pairedBy ? ` by ${h.pairedBy}` : ''}` : '—'],
    ['Added', `${fmtTs(r.created)} · ${fmtAge(r.created, now)} ago${h.addedBy ? ` by ${h.addedBy}` : ''}`],
  ]
  const next: [HostStep, string] | null = h.keyChanged ? ['key', 'Check the key again…'] : !h.fingerprint ? ['key', 'Confirm the host key…'] : h.state !== 'paired' ? ['pair', 'Pair…'] : null

  const askRemove = () => {
    setError('')
    cleanupCommand(cluster, r.name).then(
      (c) => setRemoving(c),
      (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    )
  }
  const remove = () => {
    setBusy(true)
    removeHost(cluster, r.name)
      .then(() => {
        setRemoving(null)
        onToast?.(`Removed the host ${r.name}.`)
        onClose()
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div style={{ position: 'fixed', top: 0, right: 0, bottom: 'var(--dock-space, 0px)', left: 0, zIndex: 40, display: 'flex', justifyContent: 'flex-end' }}>
      <button aria-label={`Close the details of ${r.name}`} tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <aside
        className="rk-npanel rk-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={`Host details: ${r.name}`}
        data-testid="node-panel"
        data-host="true"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: 'min(980px, 100%)', height: '100%', overflow: 'auto', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderLeft: '1px solid var(--line)', boxShadow: 'var(--shadow)' }}
      >
        <div style={{ padding: '18px 20px 14px', display: 'flex', alignItems: 'flex-start', gap: 10, borderBottom: '1px solid var(--line)' }}>
          <div style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>Host details · outside Kubernetes</span>
            <h2 className="rk-mono" style={{ margin: 0, fontSize: 17, fontWeight: 600, lineHeight: 1.3, overflowWrap: 'anywhere' }}>
              {r.name}
              <CopyName text={r.name} />
            </h2>
          </div>
          <button ref={closeRef} className="rk-icon-btn" aria-label="Close host details" title="Close (Esc)" onClick={onClose}>
            <CloseIcon />
          </button>
        </div>
        <div style={{ padding: '16px 20px 28px', display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: '20px 28px', alignItems: 'start' }}>
          <Section title="Status" wide>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }} data-testid="node-status">
              <span className="rk-pill" data-tone={r.tone} title={localTimes(r.statusTip)} style={{ height: 24, fontSize: 12.5 }}>
                <span className="rk-dot" />
                {r.status}
              </span>
              <span className="rk-role" data-role={r.role} title={tag.title}>
                {tag.text}
              </span>
              {may && (
                <span style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
                  {next && (
                    <button className="rk-btn" data-on="true" onClick={() => setDialog(next[0])} data-testid="host-next-step">
                      {next[1]}
                    </button>
                  )}
                  <button className="rk-btn" onClick={askRemove} data-testid="host-remove">
                    Remove host…
                  </button>
                </span>
              )}
            </div>
            <p style={{ margin: 0, fontSize: 12, color: 'var(--fg2)' }}>{localTimes(r.statusTip)}</p>
            {error && (
              <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12 }}>
                {error}
              </p>
            )}
          </Section>
          {r.problems.length > 0 && (
            <section style={{ display: 'flex', flexDirection: 'column', gap: 8, gridColumn: '1 / -1' }} aria-label="Problems">
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                <h3 className="rk-h">Problems</h3>
                <span className="rk-t" data-tone={errors ? 'err' : 'warn'} style={{ fontSize: 11.5, fontWeight: 500 }}>
                  {nWord(r.problems.length, 'problem', 'problems')}
                </span>
              </div>
              {r.problems.map((pb) => (
                <div key={pb.title} className="rk-callout" data-tone={pb.severity === 'error' ? 'err' : 'warn'} style={{ flexDirection: 'column', gap: 6 }}>
                  <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{pb.title}</strong>
                  <span style={{ fontSize: 12, color: 'var(--fg2)' }}>{pb.summary}</span>
                </div>
              ))}
            </section>
          )}
          <Section title="Connection">
            <dl className="rk-kv" data-testid="host-connection">
              {connection.map(([k, v, tip]) => (
                <div key={k} style={{ display: 'contents' }}>
                  <dt>{k}</dt>
                  <dd className="rk-mono" title={tip}>
                    {v}
                  </dd>
                </div>
              ))}
            </dl>
            <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>Reached by SSH from the console, with a certificate it signs for each connection. Nothing cordons or drains a host, and a reboot is over once it answers from a new boot.</p>
          </Section>
          <Section title="Labels" count={labels.length}>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {labels.map((k) => (
                <span key={k} className="rk-chip">
                  {k}
                  <em>{r.labels[k] !== '' ? '=' + r.labels[k] : ''}</em>
                </span>
              ))}
              {labels.length === 0 && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>No labels: the host is only in lists of nodes that name it.</span>}
            </div>
          </Section>
          <FactsSection node={r.name} />
          {params && (
            <Section title="Variables" count={params.list.length} wide>
              <p style={{ margin: 0, fontSize: 12, color: 'var(--fg2)' }} data-testid="node-fleets">
                {params.fleets.length ? `In ${nWord(params.fleets.length, 'fleet', 'fleets')}: ${params.fleets.join(', ')}` : 'In no fleet: only All nodes reaches it'}
              </p>
              {params.list.length > 0 && (
                <table className="tb-table" data-testid="node-params">
                  <thead>
                    <tr>
                      <th>Variable</th>
                      <th>Value</th>
                      <th>From</th>
                    </tr>
                  </thead>
                  <tbody>
                    {params.list.map((p) => (
                      <tr key={p.name}>
                        <td className="rk-mono">{p.name}</td>
                        <td className="rk-mono">{p.conflict ? <span style={{ color: 'var(--err)' }}>conflict</span> : p.value}</td>
                        <td>{p.conflict ? p.conflict.join(' and ') : p.source === 'node' ? 'this host' : p.from}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Section>
          )}
        </div>
      </aside>
      {dialog && <HostDialog cluster={cluster} host={{ name: r.name, address: h.address, port: h.port }} start={dialog} onClose={() => setDialog(null)} onDone={(m) => m && onToast?.(m)} />}
      {removing && (
        <ConfirmDialog title={`Remove the host ${r.name}?`} confirm="Remove host" danger busy={busy} error={error} onConfirm={remove} onCancel={() => setRemoving(null)}>
          <p style={{ margin: 0 }}>The console forgets it, its pinned key and its pairing; Automation no longer reaches it. To take the console&apos;s lines off the host too, run this there as root:</p>
          <pre className="rk-mono" data-testid="host-cleanup" style={{ margin: 0, padding: '8px 10px', maxHeight: 160, overflow: 'auto', fontSize: 11, background: 'var(--sunken)', border: '1px solid var(--line)', borderRadius: 6, whiteSpace: 'pre-wrap' }}>
            {removing.command}
          </pre>
          <button className="rk-link" style={{ alignSelf: 'flex-start', fontSize: 12 }} onClick={() => void copyText(removing.command).then((ok) => ok && onToast?.('Copied the cleanup command.'))}>
            Copy the command
          </button>
        </ConfirmDialog>
      )}
    </div>
  )
}
