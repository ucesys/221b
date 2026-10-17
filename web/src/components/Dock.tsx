import { useEffect, useRef, type KeyboardEvent, type PointerEvent, type ReactNode } from 'react'
import type { Pane } from '../lib/sherlock'
import { AssistantsIcon, MaximizeIcon, MinimizeIcon, PopInIcon, PopOutIcon, RestoreIcon } from './Icons'

export type DockMode = 'open' | 'max' | 'min'

/** What a panel needs to draw itself in the dock. */
export type RenderPane = (controls: ReactNode, switcher: ReactNode) => ReactNode

interface Props {
  mode: DockMode
  onMode: (m: DockMode) => void
  height: number
  onHeight: (h: number) => void
  /** Sherlock's share of the width when both panels show. */
  split: number
  onSplit: (s: number) => void
  /** The panel shown on a narrow screen. */
  pane: Pane
  onPane: (p: Pane) => void
  /** The panels docked here; null when off or in a window of its own. */
  sherlock: RenderPane | null
  watson: RenderPane | null
  /** Moves Sherlock and Watson to a window of their own; undefined where the browser cannot. */
  onPopOut?: () => void
  /** Commands waiting for approval, and whether anything is running. */
  waiting: number
  busy: boolean
}

const MIN_H = 150

/**
 * The panel docked at the bottom of every tab: Sherlock and Watson side
 * by side (P5-01, P6-01), resizable by the top edge and the line between
 * them (the arrow keys too), maximized over the page or minimized to an
 * icon. Together they move to a window of their own (P5-08, P6-09).
 */
export function Dock(p: Props) {
  const { mode, onMode, height, onHeight, split, onSplit } = p
  const drag = useRef<{ kind: 'h' | 's'; x0: number; y0: number; h0: number; w: number; s0: number } | null>(null)
  const section = useRef<HTMLElement>(null)
  const clamp = (h: number) => Math.round(Math.max(MIN_H, Math.min(window.innerHeight - 150, h)))
  const clampS = (s: number) => Math.max(0.18, Math.min(0.82, s))

  useEffect(() => {
    if (mode === 'max') section.current?.querySelector<HTMLElement>('textarea, input[type=text]')?.focus()
  }, [mode])

  const both = !!p.sherlock && !!p.watson
  const names = [p.sherlock && 'Sherlock', p.watson && 'Watson'].filter(Boolean).join(' and ')
  const shown: Pane = both ? p.pane : p.sherlock ? 'sherlock' : 'watson'

  if (mode === 'min') {
    const what = [p.sherlock && 'Sherlock', p.watson && 'Watson'].filter(Boolean).join(' and ')
    const label = `Open ${what}` + (p.waiting ? ` — ${p.waiting === 1 ? '1 command waits' : `${p.waiting} commands wait`} for approval` : p.busy ? ' — something is running' : '')
    return (
      <button className="rk-fab" aria-label={label} title={label} onClick={() => onMode('open')} data-testid="sherlock-fab">
        <AssistantsIcon />
        {p.waiting > 0 && <span className="rk-fab-badge">{p.waiting}</span>}
        {p.busy && <span className="rk-spin" aria-hidden="true" />}
      </button>
    )
  }
  const down = (kind: 'h' | 's', e: PointerEvent<HTMLElement>) => {
    if (e.button !== 0) return
    e.currentTarget.setPointerCapture?.(e.pointerId)
    e.preventDefault()
    const r = section.current?.getBoundingClientRect()
    drag.current = { kind, x0: e.clientX, y0: e.clientY, h0: r?.height ?? height, w: r?.width ?? window.innerWidth, s0: split }
  }
  const move = (e: PointerEvent<HTMLElement>) => {
    const d = drag.current
    if (!d) return
    if (d.kind === 'h') onHeight(clamp(d.h0 + d.y0 - e.clientY))
    else onSplit(clampS(d.s0 + (e.clientX - d.x0) / d.w))
  }
  const up = (e: PointerEvent<HTMLElement>) => {
    drag.current = null
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }
  const hKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    const step = { ArrowUp: 1, PageUp: 4, ArrowDown: -1, PageDown: -4 }[e.key]
    if (!step) return
    e.preventDefault()
    onHeight(clamp((section.current?.getBoundingClientRect().height ?? height) + step * 32))
  }
  const sKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    const step = { ArrowLeft: -1, ArrowRight: 1 }[e.key]
    if (!step) return
    e.preventDefault()
    onSplit(clampS(split + step * 0.04))
  }
  const minMax = (cls?: string) => (
    <>
      <button className={'rk-icon-btn' + (cls ? ' ' + cls : '')} aria-label="Minimize" title="Minimize to an icon" onClick={() => onMode('min')} data-testid="sherlock-minimize">
        <MinimizeIcon />
      </button>
      <button className={'rk-icon-btn' + (cls ? ' ' + cls : '')} aria-label={mode === 'max' ? 'Restore to the bottom of the page' : 'Maximize'} title={mode === 'max' ? 'Restore to the bottom of the page' : 'Maximize'} onClick={() => onMode(mode === 'max' ? 'open' : 'max')} data-testid="sherlock-maximize">
        {mode === 'max' ? <RestoreIcon /> : <MaximizeIcon />}
      </button>
    </>
  )
  const popOut = p.onPopOut && (
    <button className="rk-icon-btn" aria-label={`Open ${names} in a window of their own`} title={`Open ${names} in a window of their own — for another monitor`} onClick={() => p.onPopOut!()} data-testid="dock-popout">
      <PopOutIcon />
    </button>
  )
  const switcher = both ? (
    <div className="rk-seg rk-narrow" role="group" aria-label="Show">
      <button aria-pressed={shown === 'sherlock'} onClick={() => p.onPane('sherlock')} data-testid="dock-show-sherlock">
        Sherlock
      </button>
      <button aria-pressed={shown === 'watson'} onClick={() => p.onPane('watson')} data-testid="dock-show-watson">
        Watson
        {p.waiting > 0 && (
          <span className="rk-cnt" style={{ background: 'var(--warn-weak)', color: 'var(--warn)' }}>
            {p.waiting}
          </span>
        )}
      </button>
    </div>
  ) : null
  // The window controls sit at the right end: in the right panel when both
  // show, and in each panel on a narrow screen, where one shows at a time.
  const sherlockControls = both ? (
    <span className="rk-narrow" style={{ gap: 2 }}>
      {popOut}
      {minMax()}
    </span>
  ) : (
    <>
      {popOut}
      {minMax()}
    </>
  )
  const watsonControls = (
    <>
      {popOut}
      {minMax()}
    </>
  )
  return (
    <>
      {mode === 'max' && <button className="rk-dock-bd" aria-label={`Restore the ${names} panel`} tabIndex={-1} onClick={() => onMode('open')} />}
      <section
        ref={section}
        className="rk-dock"
        data-mode={mode}
        data-pane={shown}
        role={mode === 'max' ? 'dialog' : 'region'}
        aria-modal={mode === 'max' ? true : undefined}
        aria-label={`${names} panel`}
        style={{ ['--dock-cols' as string]: both ? `minmax(0, ${split.toFixed(3)}fr) 8px minmax(0, ${(1 - split).toFixed(3)}fr)` : 'minmax(0, 1fr)' }}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && mode === 'max') {
            e.stopPropagation()
            onMode('open')
          }
        }}
        data-testid="sherlock-dock"
      >
        {mode === 'open' && (
          <>
            <div className="rk-dock-edge" aria-hidden="true" onPointerDown={(e) => down('h', e)} onPointerMove={move} onPointerUp={up} onPointerCancel={up} />
            <button className="rk-grip" aria-label={`Resize the ${names} panel`} title="Drag to resize — the arrow keys work too" onPointerDown={(e) => down('h', e)} onPointerMove={move} onPointerUp={up} onPointerCancel={up} onKeyDown={hKey} data-testid="sherlock-grip">
              <i />
            </button>
          </>
        )}
        <div className="rk-dock-body">
          {p.sherlock?.(sherlockControls, switcher)}
          {both && <button className="rk-vgrip" aria-label="Resize Sherlock and Watson" title="Drag to share the width — the arrow keys work too" onPointerDown={(e) => down('s', e)} onPointerMove={move} onPointerUp={up} onPointerCancel={up} onKeyDown={sKey} data-testid="dock-vgrip" />}
          {p.watson?.(watsonControls, switcher)}
        </div>
      </section>
    </>
  )
}

/** In the main window while Sherlock and Watson are in their own window. */
export function PoppedOutFab({ onFocus, onDock }: { onFocus: () => void; onDock: () => void }) {
  return (
    <div className="rk-popfab" data-testid="assistants-popped">
      <button className="rk-fab" aria-label="Sherlock and Watson are in a window of their own — bring it forward" title="Sherlock and Watson are in a window of their own — bring it forward" onClick={onFocus} data-testid="assistants-focus">
        <AssistantsIcon />
      </button>
      <button className="rk-btn rk-popback" onClick={onDock} title="Close their window and dock Sherlock and Watson here again" data-testid="assistants-dock-here">
        <PopInIcon />
        Dock here
      </button>
    </div>
  )
}
