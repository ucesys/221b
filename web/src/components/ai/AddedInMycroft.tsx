// Settings › Knowledge › Added in Mycroft (Phase 12, MY-04 to MY-07,
// MY-11): the one place a person adds to Mycroft's wiki: a note, a web
// address to read, or documents (the form that was AIOps › Mycroft › Wiki
// › Add), with what was added, each removable. Admins and SuperAdmins add
// (MY-04): a SuperAdmin finds the card in Settings › Knowledge with the
// rest, and an Admin, for whom the rest of Settings is closed, finds it
// alone at the same address. #/cases/mycroft/wiki/add, the old address,
// opens it (lib/router.ts).
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { CallError } from '../../api/call'
import { fetchWiki, type WikiState } from '../../api/knowledge'
import { WikiAdd } from '../cases/WikiAdd'
import { AddedItems } from '../cases/WikiSources'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/**
 * The card. focus scrolls to it once it is loaded (#/admin/settings/knowledge/added);
 * fallback, when given, is shown instead to someone who may not add, and
 * after follows the card to someone who may.
 */
export function AddedInMycroft({ onToast, focus = false, fallback, after }: { onToast?: (msg: string) => void; focus?: boolean; fallback?: ReactNode; after?: ReactNode }) {
  const [wiki, setWiki] = useState<WikiState | null>(null)
  const [error, setError] = useState('')
  const [denied, setDenied] = useState(false)
  const [tick, setTick] = useState(0)
  const refresh = useCallback(() => setTick((t) => t + 1), [])
  // After an addition, the state is read again every few seconds while
  // what was added waits to be read, for two minutes at most.
  const [pollUntil, setPollUntil] = useState(0)
  const added = useCallback(() => {
    setPollUntil(Date.now() + 120_000)
    refresh()
  }, [refresh])
  useEffect(() => {
    let stop = false
    fetchWiki(tick > 0).then(
      (w) => {
        if (stop) return
        setWiki(w)
        setError('')
      },
      (e: unknown) => {
        if (stop) return
        setDenied(e instanceof CallError && e.status === 403)
        setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [tick])
  useEffect(() => {
    if (!wiki || Date.now() > pollUntil || !(wiki.running || wiki.added.some((a) => !a.lastRead))) return
    const t = setTimeout(refresh, 3000)
    return () => clearTimeout(t)
  }, [wiki, pollUntil, refresh])
  const card = useRef<HTMLElement>(null)
  const loaded = wiki !== null
  useEffect(() => {
    if (focus && loaded) card.current?.scrollIntoView?.({ block: 'start' })
  }, [focus, loaded])
  if (fallback && (denied || (wiki && !wiki.mayAdd))) return <>{fallback}</>
  return (
    <>
      <section ref={card} className="rk-card" style={{ padding: '12px 16px 16px', display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Added in Mycroft" data-testid="knowledge-added">
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
          <h3 className="rk-h" style={{ margin: 0 }}>
            Added in Mycroft
          </h3>
          <span className="rk-fhint" style={{ flex: '1 1 260px' }}>
            Notes, web pages and documents people add. Admins and SuperAdmins add them here.
          </span>
        </div>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-added-error">
            {error}
          </p>
        )}
        {wiki ? (
          <>
            <WikiAdd wiki={wiki} onAdded={added} onToast={onToast} embedded />
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, borderTop: '1px solid var(--line)', paddingTop: 10 }} data-testid="knowledge-added-list">
              <strong style={{ fontSize: 12.5 }}>What was added</strong>
              <AddedItems wiki={wiki} onChanged={refresh} onToast={onToast} inSettings />
            </div>
          </>
        ) : (
          !error && <p className="rk-fhint" style={{ margin: 0 }}>Loading…</p>
        )}
      </section>
      {after}
    </>
  )
}
