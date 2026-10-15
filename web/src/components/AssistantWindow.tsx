import { useEffect, useState } from 'react'
import { useSherlock } from '../api/sherlock'
import { useWatson } from '../api/watson'
import { canPopOut, onPop, popOut, post, useAnnounce, windowHash, type Pane } from '../lib/sherlock'
import { navigate } from '../lib/router'
import { PopInIcon, PopOutIcon } from './Icons'
import { SherlockPane } from './Sherlock'
import { WatsonPane } from './Watson'

interface Props {
  cluster: string
  clusterName: string
  panes: Pane[]
  onToast: (msg: string) => void
}

const NAMES: Record<Pane, string> = { sherlock: 'Sherlock', watson: 'Watson' }

/**
 * Sherlock (P5-08) and Watson (P6-09) in a window of their own, for
 * another monitor: one of them, or both side by side. What they hold is
 * the session's, kept on the server, so it carries on here: Ask Sherlock
 * and Watson's buttons in the main window send their work here, and an
 * approval made here runs the command. Links open in the main window.
 * The window follows the main one to another cluster, and closing it
 * docks the panels there again.
 */
export function AssistantWindow({ cluster, clusterName, panes, onToast }: Props) {
  const hasS = panes.includes('sherlock')
  const hasW = panes.includes('watson')
  const sherlock = useSherlock(cluster, hasS)
  const watson = useWatson(cluster, hasW)
  const win = useAnnounce(cluster, panes)
  const [pane, setPane] = useState<Pane>(panes[0] ?? 'sherlock')
  useEffect(() => {
    document.title = `${panes.map((p) => NAMES[p]).join(' and ')} · ${clusterName || cluster} · 221B`
  }, [cluster, clusterName, panes])
  const keep = (next: Pane[]) => {
    if (next.length === 0) {
      post({ type: 'bye', win })
      window.close()
      return
    }
    navigate(windowHash(cluster, next), true)
  }
  useEffect(
    () =>
      onPop((m) => {
        if (m.type === 'cluster' && m.cluster !== cluster) navigate(windowHash(m.cluster, panes), true)
        if (m.type === 'focus' && (!m.pane || panes.includes(m.pane))) window.focus()
        if (m.type === 'close') keep(m.panes ? panes.filter((p) => !m.panes!.includes(p)) : [])
        if (m.type === 'add' && !panes.includes(m.pane)) {
          keep([...panes, m.pane].sort())
          window.focus()
        }
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [cluster, panes.join(',')],
  )
  // A panel's own controls: back to the main window, or, sharing this
  // window, to a window of its own.
  const controls = (p: Pane) => (
    <>
      {panes.length > 1 && canPopOut() && (
        <button
          className="rk-icon-btn"
          aria-label={`Move ${NAMES[p]} to a window of its own`}
          title={`Move ${NAMES[p]} to a window of its own`}
          onClick={() => {
            keep(panes.filter((x) => x !== p))
            if (!popOut(cluster, [p])) onToast('The browser did not open a new window. Allow pop-ups for the console.')
          }}
          data-testid={`${p}-apart`}
        >
          <PopOutIcon />
        </button>
      )}
      {panes.length === 1 && (
        <button
          className="rk-btn"
          onClick={() => {
            const other: Pane = p === 'sherlock' ? 'watson' : 'sherlock'
            post({ type: 'close', panes: [other] })
            keep([...panes, other].sort())
          }}
          title={`Show ${NAMES[p === 'sherlock' ? 'watson' : 'sherlock']} in this window too`}
          style={{ height: 28 }}
          data-testid={`${p}-add-other`}
        >
          With {NAMES[p === 'sherlock' ? 'watson' : 'sherlock']}
        </button>
      )}
      <button className="rk-icon-btn" aria-label={`Dock ${NAMES[p]} in the main window`} title={`Dock ${NAMES[p]} in the main window again`} onClick={() => keep(panes.filter((x) => x !== p))} data-testid={`${p}-dock-back`}>
        <PopInIcon />
      </button>
    </>
  )
  const both = hasS && hasW
  const switcher = both ? (
    <div className="rk-seg rk-narrow" role="group" aria-label="Show">
      <button aria-pressed={pane === 'sherlock'} onClick={() => setPane('sherlock')}>
        Sherlock
      </button>
      <button aria-pressed={pane === 'watson'} onClick={() => setPane('watson')}>
        Watson
      </button>
    </div>
  ) : undefined
  return (
    <div className="rk-swin" data-pane={pane} data-both={both ? 'true' : 'false'}>
      {hasS && <SherlockPane cluster={cluster} clusterName={clusterName || cluster} state={sherlock} inWindow onToast={onToast} controls={controls('sherlock')} switcher={switcher} />}
      {hasW && <WatsonPane cluster={cluster} clusterName={clusterName || cluster} state={watson} onToast={onToast} controls={controls('watson')} switcher={switcher} />}
    </div>
  )
}
