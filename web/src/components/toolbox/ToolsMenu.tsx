import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import { fetchToolbox, type ToolboxInfo, type ToolName } from '../../api/toolbox'
import type { PodDetails } from '../../api/types'
import { apiPath } from '../../lib/router'
import { BoltIcon, BoxIcon, ChevronRightIcon, CordonIcon, DrainIcon, ForwardIcon, TerminalIcon, WrenchIcon } from '../Icons'
import { useToolbox, type ToolboxApi } from './context'

export type ToolsTarget = { kind: 'pod'; name: string; namespace: string } | { kind: 'node'; name: string; cordoned?: boolean; role?: string }

/**
 * The Tools button beside a pod's or a node's name (TB-01): a menu of the
 * tools, each opening its dialog; nothing else is in it. Each item says why
 * when it cannot be used, as the server would refuse it (TB-05).
 */
export function ToolsButton({ target }: { target: ToolsTarget }) {
  const tb = useToolbox()
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
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
      {open && <ToolsMenu tb={tb} target={target} anchor={anchor} onClose={() => setAnchor(null)} />}
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
  sub?: { label: string; hint?: string; run: () => void }[]
  subHead?: ReactNode
  run?: () => void
  sepBefore?: boolean
}

function ToolsMenu({ tb, target, anchor, onClose }: { tb: ToolboxApi; target: ToolsTarget; anchor: HTMLElement | null; onClose: () => void }) {
  const [info, setInfo] = useState<ToolboxInfo | null>(null)
  const [err, setErr] = useState('')
  const [pod, setPod] = useState<PodDetails | null>(null)
  const [sub, setSub] = useState('')
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
    const ports = (pod?.containers ?? []).filter((c) => !c.init).flatMap((c) => c.ports.filter((p) => p.protocol === 'TCP').map((p) => ({ c: c.name, p })))
    const images = info?.debugContainerImages.map((n) => info.images.find((i) => i.name === n)?.image.split('/').pop()?.split(':')[0] ?? n).join(', ')
    items = [
      { key: 'shell', icon: <TerminalIcon />, label: 'Shell…', hint: pod ? `${pod.containers.find((c) => !c.init)?.name ?? ''}` : undefined, off: why('shell'), run: () => open('shell') },
      { key: 'command', icon: <BoltIcon />, label: 'Run a command…', hint: 'output in the dock', off: why('command'), run: () => open('command') },
      {
        key: 'forward',
        icon: <ForwardIcon />,
        label: 'Port forward',
        hint: ports.length ? `${ports.length} port${ports.length === 1 ? '' : 's'}` : undefined,
        off: why('forward'),
        subHead: 'Forward to your computer',
        sub: [
          ...ports.map(({ c, p }) => ({ label: `${p.port}/TCP${p.name ? '  ' + p.name : ''}`, hint: `container ${c}`, run: () => open('forward', { port: p.port, localPort: p.port }) })),
          { label: 'Another port…', run: () => open('forward') },
        ],
      },
      { key: 'debug-container', icon: <BoxIcon />, label: 'Debug container…', hint: images, off: why('debug-container'), run: () => open('debug-container') },
    ]
  } else {
    const cmds = info?.nodeCommands ?? []
    items = [
      { key: 'node-shell', icon: <TerminalIcon />, label: 'Node shell…', hint: <span className="rk-tag" data-tone="err">root</span>, off: why('node-shell'), run: () => open('node-shell') },
      {
        key: 'node-command',
        icon: <BoltIcon />,
        label: 'Run a command',
        off: why('node-command'),
        subHead: (
          <>
            One command on <span className="rk-mono">{target.name}</span> · each is its own Watson card
          </>
        ),
        sub: [...cmds.map((c) => ({ label: c, run: () => open('node-command', { command: c }) })), { label: 'Another command…', run: () => open('node-command') }],
      },
      { key: 'debug-pod', icon: <BoxIcon />, label: 'Debug pod on this node…', off: why('debug-pod'), run: () => open('debug-pod') },
      {
        key: 'cordon',
        icon: <CordonIcon />,
        label: target.cordoned ? 'Uncordon…' : 'Cordon…',
        hint: 'Watson card',
        sepBefore: true,
        run: () => {
          onClose()
          tb.watson(`kubectl ${target.cordoned ? 'uncordon' : 'cordon'} ${target.name}`, 'Tools menu')
        },
      },
      {
        key: 'drain',
        icon: <DrainIcon />,
        label: 'Drain…',
        hint: 'Watson card',
        run: () => {
          onClose()
          tb.watson(`kubectl drain ${target.name} --ignore-daemonsets --delete-emptydir-data`, 'Tools menu')
        },
      },
    ]
  }

  const keys = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      if (sub) setSub('')
      else {
        onClose()
        anchor?.focus()
      }
      return
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const all = [...(menu.current?.querySelectorAll<HTMLElement>('[role=menuitem]:not([aria-disabled=true])') ?? [])]
    const i = all.indexOf(document.activeElement as HTMLElement)
    all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus()
  }

  const subItem = items.find((x) => x.key === sub)
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
  return (
    <div ref={menu} className="tb-menu" role="menu" aria-label={`Tools for ${target.kind} ${target.name}`} style={style} onKeyDown={keys} onClick={(e) => e.stopPropagation()} data-testid="tools-menu">
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
                aria-haspopup={it.sub ? 'menu' : undefined}
                aria-expanded={it.sub ? sub === it.key : undefined}
                data-active={sub === it.key}
                title={it.off ? `${it.label.replace('…', '')}: ${it.off}` : undefined}
                onClick={() => {
                  if (!info || it.off) return
                  if (it.sub) setSub(sub === it.key ? '' : it.key)
                  else it.run?.()
                }}
                onMouseEnter={() => it.sub && !it.off && info && setSub(it.key)}
                onKeyDown={(e) => {
                  if (e.key === 'ArrowRight' && it.sub && !it.off) {
                    setSub(it.key)
                    setTimeout(() => menu.current?.querySelector<HTMLElement>('[data-sub=true] [role=menuitem]')?.focus(), 0)
                  }
                }}
                data-testid={`tool-${it.key}`}
              >
                {it.icon}
                {it.label}
                <span className="tb-mh">
                  {it.off ? it.off : it.hint}
                  {it.sub && <ChevronRightIcon />}
                </span>
              </button>
            </div>
          ))}
          <div className="tb-msep" role="separator" />
          <p className="tb-mnote">Every item makes a Watson card; sessions open in the dock.</p>
        </>
      )}
      {subItem?.sub && (
        <div className="tb-menu" data-sub="true" role="menu" aria-label={subItem.label} style={{ position: 'absolute', top: 40, left: 'calc(100% - 8px)' }} onMouseLeave={() => setSub('')}>
          {subItem.subHead && <div className="tb-mhead">{subItem.subHead}</div>}
          {subItem.sub.map((s, i) => (
            <button
              key={s.label}
              role="menuitem"
              className="tb-mi"
              onClick={s.run}
              onKeyDown={(e) => {
                if (e.key === 'ArrowLeft') setSub('')
              }}
              style={i === subItem.sub!.length - 1 && subItem.sub!.length > 1 ? { borderTop: '1px solid var(--line)', borderRadius: 0, marginTop: 4, paddingTop: 4 } : undefined}
            >
              <span className={i < subItem.sub!.length - 1 ? 'rk-mono' : undefined}>{s.label}</span>
              {s.hint && <span className="tb-mh">{s.hint}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
