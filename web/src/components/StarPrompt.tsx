// Star us on GitHub (0.13.2, CU-21; the owner's decisions): after the
// installation, a dialog asks the people who installed the console, the
// SuperAdmins the Helm values name and the first-run page's, to star 221B on GitHub, with a link to
// the repository (a new tab), Close and Never show this again. The server
// says whether to ask (GET /api/v1/star-prompt): the person is one of them
// and the console-wide setting, Administration › Settings › General › Ask to
// star 221B on GitHub, is on. Closed with the box ticked, the setting goes
// off for everyone (audited); closed without it, the dialog shows again at
// the next sign-in, not at every page load: the page keeps that it was
// closed under the session's key. The console never calls GitHub and keeps
// nothing about starring.
import { useEffect, useState } from 'react'
import { fetchStarPrompt, neverStarPrompt, REPO_URL } from '../api/starPrompt'
import { readJSON, writeJSON } from '../lib/storage'
import { ConfirmDialog } from './ConfirmDialog'

/** Where the page keeps that the dialog was closed in a session. */
export const starClosedKey = (session: string) => `221b.starPrompt.closed.${session}`

export function StarPrompt() {
  const [session, setSession] = useState('')
  const [never, setNever] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    let gone = false
    fetchStarPrompt().then(
      (p) => {
        if (!gone && p.show && p.session && !readJSON(starClosedKey(p.session), false)) setSession(p.session)
      },
      () => undefined,
    )
    return () => {
      gone = true
    }
  }, [])
  if (!session) return null
  const close = async () => {
    if (never) {
      setBusy(true)
      setError('')
      try {
        await neverStarPrompt()
      } catch (e) {
        setBusy(false)
        setError(e instanceof Error ? e.message : String(e))
        return
      }
    }
    writeJSON(starClosedKey(session), true)
    setSession('')
  }
  return (
    <ConfirmDialog title="Star 221B on GitHub?" confirm="Close" alone busy={busy} error={error} onConfirm={() => void close()} onCancel={() => void close()}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="star-prompt">
        <p style={{ margin: 0 }}>Thank you for installing 221B. If it helps you, a star on GitHub helps others find it.</p>
        <a className="rk-link" href={REPO_URL} target="_blank" rel="noopener noreferrer" data-testid="star-prompt-link">
          github.com/ucesys/221b
        </a>
        <label className="rk-tcheck" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={never} onChange={(e) => setNever(e.target.checked)} disabled={busy} data-testid="star-prompt-never" />
          Never show this again
        </label>
      </div>
    </ConfirmDialog>
  )
}
