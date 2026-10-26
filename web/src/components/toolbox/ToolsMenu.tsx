import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { fetchToolbox, type ToolAction, type ToolboxInfo, type ToolName } from '../../api/toolbox'
import type { PodDetails } from '../../api/types'
import { apiPath } from '../../lib/router'
import { ConfirmDialog } from '../ConfirmDialog'
import { BoxIcon, CordonIcon, DrainIcon, ForwardIcon, TerminalIcon, TrashIcon, WrenchIcon } from '../Icons'
import { useToolbox, type ToolboxApi } from './context'

export type ToolsTarget = { kind: 'pod'; name: string; namespace: string } | { kind: 'node'; name: string; cordoned?: boolean; role?: string }

/** A change the menu asks about before making it: Drain, Delete. */
interface Confirm {
  title: string
  body: ReactNode
  confirm: string
  action: ToolAction
  what: string
}

/**
 * The Tools button beside a pod's or a node's name (TB-01): a menu of the
 * tools, each opening its dialog, and the row's fixed changes. Each item
 * says why when it cannot be used, as the server would refuse it (TB-05).
 */
export function ToolsButton({ target }: { target: ToolsTarget }) {
  const tb = useToolbox()
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const open = anchor !== null
  if (!tb) return null
  const what = target.kind === 'pod' ? `pod ${target.name}` : `node ${target.name}`
  return (
    <>
      <button
        className="tb-tools"
        aria-label={`Tools for ${what}`}
        title={`Tools for ${what}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation()
          e.preventDefault()
          const el = e.currentTarget
          setAnchor((a) => (a ? null : el))
        }}
        onMouseDown={(e) => e.stopPropagation()}
        data-testid={`tools-${target.kind}-${target.name}`}
      >
        <WrenchIcon />
      </button>
      {open && <ToolsMenu tb={tb} target={target} anchor={anchor} onClose={() => setAnchor(null)} onConfirm={setConfirm} />}
      {confirm && (
        <ConfirmDialog
          title={confirm.title}
          confirm={confirm.confirm}
          danger
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            setConfirm(null)
            tb.action(confirm.action, confirm.what)
          }}
        >
          {confirm.body}
        </ConfirmDialog>
      )}
    </>
  )
}

/** A pod's or node's name with its Tools button. */
export function NameWithTools({ children, target }: { children: ReactNode; target: ToolsTarget }) {
  return (
    <span className="tb-namecell">
      {children}
      <ToolsButton target={target} />
    </span>
  )
}

function place(anchor: HTMLElement | null, width = 300): CSSProperties {
  const r = anchor?.getBoundingClientRect()
  if (!r) return { top: 80, left: 80 }
  const left = Math.max(8, Math.min(window.innerWidth - width - 8, r.left - 8))
  const below = window.innerHeight - r.bottom - 8
  return below >= 260 ? { top: r.bottom + 4, left, maxHeight: below } : { bottom: window.innerHeight - r.top + 4, left, maxHeight: r.top - 8 }
}

interface Item {
  key: string
  icon: ReactNode
  label: string
  hint?: ReactNode
  off?: string
  run: () => void
  danger?: boolean
  sepBefore?: boolean
}

function ToolsMenu({ tb, target, anchor, onClose, onConfirm }: { tb: ToolboxApi; target: ToolsTarget; anchor: HTMLElement | null; onClose: () => void; onConfirm: (c: Confirm) => void }) {
  const [info, setInfo] = useState<ToolboxInfo | null>(null)
  const [err, setErr] = useState('')
  const [pod, setPod] = useState<PodDetails | null>(null)
  const menu = useRef<HTMLDivElement>(null)
  const [style] = useState(() => place(anchor))

  useEffect(() => {
    if (!tb.available) return
    let live = true
    fetchToolbox(tb.cluster, target.kind === 'pod' ? [target.namespace] : [], target.kind === 'node').then(
      (i) => live && setInfo(i),
      (e: unknown) => live && setErr(e instanceof Error ? e.message : String(e)),
    )
    if (target.kind === 'pod') {
      fetch(apiPath(`pods/${encodeURIComponent(target.namespace)}/${encodeURIComponent(target.name)}`, tb.cluster), { headers: { Accept: 'application/json' } })
        .then((r) => (r.ok ? (r.json() as Promise<PodDetails>) : null))
        .then((d) => live && setPod(d))
        .catch(() => undefined)
    }
    return () => {
      live = false
    }
  }, [tb.available, tb.cluster, target])

  useEffect(() => {
    const away = (e: MouseEvent) => {
      if (menu.current && !menu.current.contains(e.target as Node) && !anchor?.contains(e.target as Node)) onClose()
    }
    const scroll = (e: Event) => {
      if (menu.current && !menu.current.contains(e.target as Node)) onClose()
    }
    document.addEventListener('mousedown', away)
    window.addEventListener('scroll', scroll, true)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('mousedown', away)
      window.removeEventListener('scroll', scroll, true)
      window.removeEventListener('resize', onClose)
    }
  }, [anchor, onClose])

  useLayoutEffect(() => {
    menu.current?.querySelector<HTMLElement>('[role=menuitem]:not([aria-disabled=true])')?.focus()
  }, [info, err])

  const why = (t: ToolName): string | undefined => {
    if (!info) return undefined
    const o = info.off[t]
    if (o) return o
    if (target.kind === 'pod') return info.namespaces[target.namespace]?.tools[t] || undefined
    if (t === 'node-shell' || t === 'node-command') return info.nodes[t] || undefined
    return undefined
  }
  const open = (tool: ToolName, prefill = {}) => {
    onClose()
    tb.openTool({ tool, pod: target.kind === 'pod' ? { name: target.name, namespace: target.namespace } : undefined, node: target.kind === 'node' ? target.name : undefined, prefill, from: 'Tools menu' })
  }

  let items: Item[]
  if (target.kind === 'pod') {
    const ports = (pod?.containers ?? []).filter((c) => !c.init).flatMap((c) => c.ports.filter((p) => p.protocol === 'TCP').map((p) => p.port))
    const images = info?.debugContainerImages.map((n) => info.images.find((i) => i.name === n)?.image.split('/').pop()?.split(':')[0] ?? n).join(', ')
    items = [
      { key: 'shell', icon: <TerminalIcon />, label: 'Shell…', hint: pod ? `${pod.containers.find((c) => !c.init)?.name ?? ''}` : undefined, off: why('shell'), run: () => open('shell') },
      // The dialog lists the pod's ports; the first is chosen.
      { key: 'forward', icon: <ForwardIcon />, label: 'Port forward…', hint: ports.length ? `${ports.length} port${ports.length === 1 ? '' : 's'}` : undefined, off: why('forward'), run: () => open('forward', ports.length ? { port: ports[0], localPort: ports[0] } : {}) },
      { key: 'debug-container', icon: <BoxIcon />, label: 'Debug container…', hint: images, off: why('debug-container'), run: () => open('debug-container') },
      {
        key: 'delete',
        icon: <TrashIcon />,
        label: 'Delete…',
        sepBefore: true,
        danger: true,
        run: () => {
          onClose()
          onConfirm({
            title: `Delete pod ${target.name}?`,
            confirm: 'Delete pod',
            action: { action: 'delete-pod', namespace: target.namespace, pod: target.name },
            what: `pod ${target.name}`,
            body: (
              <p style={{ margin: 0 }}>
                In <span className="rk-mono">{target.namespace}</span>. Its controller, if it has one, starts a new pod in its place; a pod without one is gone. Kubernetes decides, as you.
              </p>
            ),
          })
        },
      },
    ]
  } else {
    items = [
      { key: 'node-shell', icon: <TerminalIcon />, label: 'Node shell…', hint: <span className="rk-tag" data-tone="err">root</span>, off: why('node-shell'), run: () => open('node-shell') },
      { key: 'debug-pod', icon: <BoxIcon />, label: 'Debug pod on this node…', off: why('debug-pod'), run: () => open('debug-pod') },
      {
        key: 'cordon',
        icon: <CordonIcon />,
        label: target.cordoned ? 'Uncordon' : 'Cordon',
        hint: target.cordoned ? 'take new pods again' : 'no new pods',
        sepBefore: true,
        run: () => {
          onClose()
          tb.action({ action: target.cordoned ? 'uncordon' : 'cordon', node: target.name }, `node ${target.name}`)
        },
      },
      {
        key: 'drain',
        icon: <DrainIcon />,
        label: 'Drain…',
        danger: true,
        run: () => {
          onClose()
          onConfirm({
            title: `Drain node ${target.name}?`,
            confirm: 'Drain node',
            action: { action: 'drain', node: target.name },
            what: `node ${target.name}`,
            body: (
              <p style={{ margin: 0 }}>
                Cordons the node, then evicts every pod on it except DaemonSets’; data in their emptyDir volumes is lost. A PodDisruptionBudget can hold it up. Watson shows its progress.
              </p>
            ),
          })
        },
      },
    ]
  }

  const keys = (e: KeyboardEvent<HTMLDivElement>) => {
    // The menu's keys are its own: the row it opened from moves between
    // rows on the same arrows, and must not.
    e.stopPropagation()
    if (e.key === 'Escape') {
      onClose()
      anchor?.focus()
      return
    }
    if (e.key === 'Tab') {
      onClose()
      return
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const all = [...(menu.current?.querySelectorAll<HTMLElement>('[role=menuitem]:not([aria-disabled=true])') ?? [])]
    const i = all.indexOf(document.activeElement as HTMLElement)
    all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus()
  }

  const head =
    target.kind === 'pod' ? (
      <>
        Tools for pod <span className="rk-mono">{target.name}</span> in <span className="rk-mono">{target.namespace}</span>
      </>
    ) : (
      <>
        Tools for node <span className="rk-mono">{target.name}</span>
        {target.role ? ` · ${target.role}` : ''}
      </>
    )
  // At the end of the document: a table cell's styles (its text does not
  // wrap) and keyboard handling stay out of it.
  return createPortal(
    <div ref={menu} className="tb-menu" role="menu" aria-label={`Tools for ${target.kind} ${target.name}`} style={style} onKeyDown={keys} onClick={(e) => e.stopPropagation()} onMouseDown={(e) => e.stopPropagation()} data-testid="tools-menu">
      <div className="tb-mhead">{head}</div>
      {!tb.available ? (
        <p className="tb-mnote" style={{ maxWidth: 300 }}>
          {tb.off || 'The tools open through Watson, which is off here.'}
        </p>
      ) : err ? (
        <p className="tb-mnote" data-tone="err" style={{ color: 'var(--err)' }}>
          {err}
        </p>
      ) : (
        <>
          {items.map((it) => (
            <div key={it.key} style={{ display: 'contents' }}>
              {it.sepBefore && <div className="tb-msep" role="separator" />}
              <button
                role="menuitem"
                className="tb-mi"
                aria-disabled={!info || it.off ? true : undefined}
                data-danger={it.danger || undefined}
                title={it.off ? `${it.label.replace('…', '')}: ${it.off}` : undefined}
                onClick={() => {
                  if (!info || it.off) return
                  it.run()
                }}
                data-testid={`tool-${it.key}`}
              >
                {it.icon}
                {it.label}
                <span className="tb-mh">{it.off ? it.off : it.hint}</span>
              </button>
            </div>
          ))}
        </>
      )}
    </div>,
    document.body,
  )
}
