import { useEffect } from 'react'
import { useSherlock } from '../api/sherlock'
import { onPop, post, sherlockHash, useAnnounce } from '../lib/sherlock'
import { navigate } from '../lib/router'
import { PopInIcon } from './Icons'
import { SherlockPane } from './Sherlock'

interface Props {
  cluster: string
  clusterName: string
  onToast: (msg: string) => void
}

/**
 * Sherlock in a window of its own (P5-08), for another monitor. The
 * conversation is the session's, kept on the server, so it carries on
 * here; links open in the main window, which also sends its Ask Sherlock
 * questions here. It follows the main window to another cluster, and
 * closing it docks the panel there again.
 */
export function SherlockWindow({ cluster, clusterName, onToast }: Props) {
  const state = useSherlock(cluster)
  useAnnounce(cluster)
  useEffect(() => {
    document.title = `Sherlock · ${clusterName || cluster} · 221B`
  }, [cluster, clusterName])
  useEffect(
    () =>
      onPop((m) => {
        if (m.type === 'cluster' && m.cluster !== cluster) navigate(sherlockHash(m.cluster), true)
        if (m.type === 'focus') window.focus()
        if (m.type === 'close') window.close()
      }),
    [cluster],
  )
  const dockBack = () => {
    post({ type: 'bye' })
    window.close()
  }
  return (
    <div className="rk-swin">
      <SherlockPane
        cluster={cluster}
        clusterName={clusterName || cluster}
        state={state}
        inWindow
        onToast={onToast}
        controls={
          <button className="rk-icon-btn" aria-label="Dock in the main window" title="Close this window and dock Sherlock in the main window again" onClick={dockBack} data-testid="sherlock-dock-back">
            <PopInIcon />
          </button>
        }
      />
    </div>
  )
}
