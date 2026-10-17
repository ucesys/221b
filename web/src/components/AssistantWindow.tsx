import { useEffect, useState } from 'react'
import { useSherlock } from '../api/sherlock'
import { sendToWatson, useWatson, WatsonError } from '../api/watson'
import { onPop, post, useAnnounce, windowHash, type Pane } from '../lib/sherlock'
import { navigate } from '../lib/router'
import { PopInIcon } from './Icons'
import { SherlockPane } from './Sherlock'
import { autoRunReads, WatsonContext, WatsonPane, type WatsonApi } from './Watson'

interface Props {
  cluster: string
  clusterName: string
  onToast: (msg: string) => void
}

/**
 * Sherlock (P5-08) and Watson (P6-09) together in a window of their own,
 * for another monitor: one window for both, side by side (decided at the
 * Phase 6 review), Sherlock only where it is on. What they hold is the
 * session's, kept on the server, so it carries on here: Ask Sherlock and
 * Watson's buttons in the main window send their work here, and an
 * approval made here runs the command. Links open in the main window. The
 * window follows the main one to another cluster, and docking it back
 * closes it and docks the panels there again.
 */
export function AssistantWindow({ cluster, clusterName, onToast }: Props) {
  const sherlock = useSherlock(cluster)
  const watson = useWatson(cluster)
  const win = useAnnounce(cluster)
  const hasS = !!sherlock.status?.enabled
  // Watson shows only beside Sherlock, as in the main window.
  const hasW = hasS && !!watson.status?.available
  const [pane, setPane] = useState<Pane>('watson')
  useEffect(() => {
    document.title = `${[hasS && 'Sherlock', hasW && 'Watson'].filter(Boolean).join(' and ') || 'Assistants'} · ${clusterName || cluster} · 221B`
  }, [cluster, clusterName, hasS, hasW])
  const dockBack = () => {
    post({ type: 'bye', win })
    window.close()
  }
  useEffect(
    () =>
      onPop((m) => {
        if (m.type === 'cluster' && m.cluster !== cluster) navigate(windowHash(m.cluster), true)
        if (m.type === 'focus') window.focus()
        if (m.type === 'close') dockBack()
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [cluster, win],
  )
  const back = (
    <button className="rk-icon-btn" aria-label="Dock Sherlock and Watson in the main window" title="Dock Sherlock and Watson in the main window again" onClick={dockBack} data-testid="assistants-dock-back">
      <PopInIcon />
    </button>
  )
  const both = hasS && hasW
  // Sherlock's commands go to Watson beside it, in this window.
  const watsonApi: WatsonApi = {
    available: hasW,
    readOnly: !!watson.status?.readOnly,
    send: (command, o = {}) => {
      setPane('watson')
      sendToWatson(cluster, command, { run: autoRunReads(), ...o }).catch((e: unknown) => onToast(e instanceof WatsonError ? e.message : String(e)))
    },
  }
  const shown: Pane = both ? pane : hasS ? 'sherlock' : 'watson'
  const switcher = both ? (
    <div className="rk-seg rk-narrow" role="group" aria-label="Show">
      <button aria-pressed={shown === 'sherlock'} onClick={() => setPane('sherlock')}>
        Sherlock
      </button>
      <button aria-pressed={shown === 'watson'} onClick={() => setPane('watson')}>
        Watson
      </button>
    </div>
  ) : undefined
  return (
    <WatsonContext.Provider value={watsonApi}>
    <div className="rk-swin" data-pane={shown} data-both={both ? 'true' : 'false'} data-testid="assistants-window">
      {hasS && <SherlockPane cluster={cluster} clusterName={clusterName || cluster} state={sherlock} inWindow onToast={onToast} controls={hasW ? <span className="rk-narrow">{back}</span> : back} switcher={switcher} />}
      {hasW && <WatsonPane cluster={cluster} clusterName={clusterName || cluster} state={watson} onToast={onToast} controls={back} switcher={switcher} />}
      {!hasS && !hasW && (
        <p style={{ margin: 0, padding: 24, color: 'var(--fg3)' }}>
          Waiting for the console…
        </p>
      )}
    </div>
    </WatsonContext.Provider>
  )
}
