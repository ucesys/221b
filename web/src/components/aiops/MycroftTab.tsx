// AIOps › Mycroft (Phase 12, wave 5, MY-01, ADR-118): what the console
// knows and what people decided, in three sub-tabs. Wiki is Mycroft's wiki
// (its pages, the drafts to review, its sources, and Add for a note, a web
// address or documents) at #/aiops/mycroft/wiki[/...]; Known issues at
// #/aiops/mycroft/known[/<id>]; Exceptions, the agent-found cases people
// accepted as they are (the console's records, not the wiki's), at
// #/aiops/mycroft/exceptions[/<id>]. Since wave 6e there is no RCAs tab:
// an RCA is part of the case it was fixed with, found by searching the
// closed cases in the Inbox (lib/router.ts sends its old addresses there).
// Since wave 6g, Fixes at #/aiops/mycroft/fixes[/<id>]: what worked
// before, saved when a case is closed as Fixed, given to the agents.
// Validation rules, a sub-tab here since ADR-138, moved to AIOps › Jobs
// in CF-89 (#/aiops/jobs/rules; the old addresses lead there).
// Since CF-55's layout pass the tab is one fixed column, as wide as the
// other Cases tabs', and every sub-tab is a MycroftPage in it: the same
// width, intro, toolbar row and table whatever it holds.
import { useCallback, useEffect, useState } from 'react'
import { fetchFixes } from '../../api/fixes'
import { fetchExceptions } from '../../api/lessons'
import type { CasesAccess } from '../../lib/cases'
import { navigate, type Route } from '../../lib/router'
import { KnowledgeTab } from './KnowledgeTab'
import { KnownTab } from './KnownTab'
import { FixesView } from './FixesView'
import { ExceptionsView } from './LessonsViews'

export type MycroftSub = 'wiki' | 'known' | 'exceptions' | 'fixes'

const SUBS: { id: MycroftSub; label: string; tip: string }[] = [
  { id: 'wiki', label: 'Wiki', tip: 'Mycroft’s wiki: its pages, the changes waiting for review, and its sources' },
  { id: 'known', label: 'Known issues', tip: 'The known issues: symptoms, workaround and status' },
  { id: 'exceptions', label: 'Exceptions', tip: 'What people accepted as it is: the agents stop reporting it' },
  { id: 'fixes', label: 'Fixes', tip: 'What worked before: saved when a case is closed as Fixed, given to the agents for the cases like it' },
]

/** A sub-tab's address. */
export const mycroftHref = (sub: MycroftSub) => `#/aiops/mycroft/${sub}`

/** The sub-tab a route opens: #/aiops/mycroft is the Wiki. */
export function mycroftSub(route: Route): MycroftSub {
  const s = route.path[1] ?? 'wiki'
  return SUBS.some((x) => x.id === s) ? (s as MycroftSub) : 'wiki'
}

export function MycroftTab({ route, access, onToast }: { route: Route; access: CasesAccess; onToast?: (msg: string) => void }) {
  const sub = mycroftSub(route)
  const rest = route.path.slice(2)
  const [counts, setCounts] = useState<Partial<Record<MycroftSub, number>>>({})
  const count = useCallback((k: MycroftSub) => (n: number) => setCounts((c) => (c[k] === n ? c : { ...c, [k]: n })), [])
  const onExceptions = useCallback((n: number) => count('exceptions')(n), [count])
  const onPages = useCallback((n: number) => count('wiki')(n), [count])
  const onFixes = useCallback((n: number) => count('fixes')(n), [count])
  // The counts on the sub-tabs, read once; a person who may not see
  // exceptions or fixes gets no count, and the tab says why when opened.
  useEffect(() => {
    let stop = false
    fetchExceptions(true).then(
      (r) => !stop && onExceptions(r.exceptions.filter((e) => e.state === 'active').length),
      () => {},
    )
    fetchFixes(true).then(
      (r) => !stop && onFixes(r.fixes.filter((f) => f.status === 'active').length),
      () => {},
    )
    return () => {
      stop = true
    }
  }, [onExceptions, onFixes])
  return (
    <div className="cs-col cs-col-inbox" data-testid="mycroft-tab">
      <div className="rk-seg" role="group" aria-label="Mycroft" style={{ alignSelf: 'flex-start' }}>
        {SUBS.map((s) => (
          <button key={s.id} aria-pressed={sub === s.id} title={s.tip} onClick={() => (sub !== s.id || rest.length > 0) && navigate(mycroftHref(s.id))} data-testid={`mycroft-sub-${s.id}`}>
            {s.label}
            {counts[s.id] !== undefined && <span className="rk-cnt">{counts[s.id]}</span>}
          </button>
        ))}
      </div>
      {sub === 'wiki' && <KnowledgeTab route={{ ...route, path: ['wiki', ...rest] }} onToast={onToast} onPages={onPages} />}
      {sub === 'known' && <KnownTab route={{ ...route, path: ['known', ...rest] }} access={access} onToast={onToast} />}
      {sub === 'exceptions' && <ExceptionsView open={rest[0] ?? ''} onToast={onToast} onCount={onExceptions} />}
      {sub === 'fixes' && <FixesView open={rest[0] ?? ''} onToast={onToast} onCount={onFixes} />}
    </div>
  )
}
