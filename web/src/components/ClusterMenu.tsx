import { useRef, useState } from 'react'
import { clusterList, envOf, stateOf, useClusters, type ClusterSummary } from '../api/clusters'
import type { StreamState } from '../api/live'
import type { ClusterInfo, Me } from '../api/types'
import { connection } from '../lib/connection'
import { clusterHref, clusterPageHref, navigate, type Route } from '../lib/router'
import { Chevron, PlusIcon, SlidersIcon } from './Icons'

interface Props {
  /** The open cluster as its stream describes it; null until it answers. */
  cluster: ClusterInfo | null
  stream: StreamState
  route: Route
  me: Me | null
  /** Opens the connection editor: for a cluster, or for a new one (null). */
  onEdit: (id: string | null) => void
}

/** "v1.33.2 · 12 nodes · Connected", or why it is not. */
export function clusterSub(c: ClusterSummary): { text: string; tone: 'muted' | 'err' } {
  const s = stateOf(c)
  if (c.state === 'failed' || c.state === 'disconnected') return { text: `${s.text}: ${c.message || 'no answer from the API server'}`, tone: 'err' }
  const bits: string[] = []
  if (c.version) bits.push(c.version)
  if (c.synced) bits.push(`${c.nodes} node${c.nodes === 1 ? '' : 's'}`)
  bits.push(s.text)
  return { text: bits.join(' · '), tone: 'muted' }
}

/**
 * The cluster button in the top bar and its menu (P3-04): every cluster
 * with its state, environment and version; switching keeps the open tab
 * and its filters. Administrators also reach each connection's editor and
 * add clusters here (P3-03).
 */
export function ClusterMenu({ cluster, stream, route, me, onEdit }: Props) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const { clusters } = useClusters()
  const here = clusters.find((c) => c.id === route.cluster)
  const conn = connection(cluster, stream)
  // The connection's name is the one people gave it; the stream's is the
  // installation's, until the connection list has answered.
  const name = here?.name ?? cluster?.name ?? (route.cluster === 'local' ? '—' : route.cluster)
  const version = cluster?.version ?? here?.version ?? ''
  const env = here ? envOf(here.env) : null
  const admin = me?.admin ?? false
  const tip = [name, env ? env.label : '', version ? 'Kubernetes ' + version : ''].filter(Boolean).join(' · ') + '\n' + conn.tip + '\nSwitch clusters' + (admin ? ' or edit their connections' : '')

  const close = () => {
    setOpen(false)
    button.current?.focus()
  }
  const pick = (c: ClusterSummary) => {
    setOpen(false)
    if (c.id === route.cluster) return
    const params: Record<string, string> = {}
    for (const [k, v] of route.params.entries()) params[k] = v
    // The open tab and its filters come along; what was selected (a path) does not.
    navigate(route.view === 'cluster' ? clusterPageHref(c.id) : clusterHref(c.id, route.tab, [], params))
  }

  return (
    <div
      className="rk-dd"
      style={{ display: 'flex', alignItems: 'center', minWidth: 0 }}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation()
          close()
        }
      }}
    >
      <button
        ref={button}
        className="rk-clbtn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Cluster ${name}${version ? ', Kubernetes ' + version : ''}: ${conn.label}. Switch clusters${admin ? ' or edit their connections' : ''}`}
        title={tip}
        onClick={() => {
          // The list is read again as the menu opens, so it shows the state now.
          if (!open) void clusterList.refresh()
          setOpen(!open)
        }}
        data-testid="cluster"
      >
        <span className="rk-t" data-tone={conn.tone} style={{ display: 'inline-flex' }}>
          <span className="rk-dot" />
        </span>
        <span className="rk-mono rk-clname">{name}</span>
        {version && (
          <span className="rk-ver rk-mono" style={{ color: 'var(--fg3)' }} data-testid="cluster-version">
            {version}
          </span>
        )}
        {env && clusters.length > 1 && (
          <span className="rk-tag" data-tone={env.tone} data-testid="cluster-env">
            {env.short}
          </span>
        )}
        <span className="rk-sr" data-testid="cluster-state">
          {conn.label}
        </span>
        <Chevron />
      </button>
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div className="rk-ddmenu rk-clmenu" role="menu" aria-label="Clusters" data-testid="cluster-menu">
            <div className="rk-ddhead">
              <span style={{ flex: '1 1 auto' }}>Clusters</span>
              <span className="rk-cnt">{clusters.length}</span>
            </div>
            {clusters.map((c) => {
              const e = envOf(c.env)
              const s = stateOf(c)
              const sub = clusterSub(c)
              const cur = c.id === route.cluster
              return (
                <div key={c.id} className="rk-clrow" data-testid="cluster-row" data-cluster={c.id}>
                  <button className="rk-ddopt rk-clpick" role="menuitem" aria-current={cur ? 'page' : undefined} aria-label={`Switch to ${c.name}${cur ? ' (open now)' : ''}`} title={`${c.server}\n${s.text}`} onClick={() => pick(c)}>
                    <span className="rk-t" data-tone={s.tone} style={{ flex: 'none', display: 'inline-flex' }}>
                      <span className="rk-dot" />
                    </span>
                    <span style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
                      <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                        <span className="rk-mono" style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                          {c.name}
                        </span>
                        <span className="rk-tag" data-tone={e.tone}>
                          {e.short}
                        </span>
                        {c.local && (
                          <span className="rk-tag" data-tone="muted" title="The cluster the console runs in">
                            here
                          </span>
                        )}
                      </span>
                      <span className="rk-clsub" data-tone={sub.tone}>
                        {sub.text}
                      </span>
                    </span>
                  </button>
                  <a className="rk-icon-btn" role="menuitem" aria-label={`${admin ? 'Edit the connection to' : 'About the connection to'} ${c.name}`} title={admin ? `Edit the connection to ${c.name}` : `About the connection to ${c.name}`} href={clusterPageHref(c.id)} onClick={() => setOpen(false)} data-testid="cluster-connection">
                    <SlidersIcon />
                  </a>
                </div>
              )
            })}
            {admin && (
              <>
                <div className="rk-ddsep" />
                <button
                  className="rk-ddopt"
                  role="menuitem"
                  onClick={() => {
                    setOpen(false)
                    onEdit(null)
                  }}
                  data-testid="cluster-add"
                >
                  <PlusIcon />
                  <span style={{ flex: '1 1 auto' }}>Add a cluster</span>
                </button>
              </>
            )}
          </div>
        </>
      )}
    </div>
  )
}
