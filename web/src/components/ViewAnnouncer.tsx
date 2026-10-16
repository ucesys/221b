import { useSyncExternalStore } from 'react'
import { lastAnnouncement, onAnnounce } from '../lib/uiControl'

/**
 * Says what Sherlock changed in this window's view (P6U-10) while its
 * answers show in a window of their own, where their notice is read out.
 */
export function ViewAnnouncer() {
  const said = useSyncExternalStore(onAnnounce, lastAnnouncement, lastAnnouncement)
  return (
    <div className="rk-sr" role="status" aria-live="polite" data-testid="sherlock-view-live">
      {said.text}
    </div>
  )
}
