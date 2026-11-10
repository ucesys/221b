// New Case and On behalf of… in AIOps › Inbox (CW-02, CS-15, CU-02): New
// Case opens the create-case form, for the people who work the cases;
// people who use the services report a problem (a case too) from the
// report page, the avatar menu and Sherlock, as before. On behalf of lets support (an
// operator, as IS-03 allows) open the report page for the person whose
// problem it is, whom the report then records as its reporter, and them
// as who filed it.
import { useEffect, useState } from 'react'
import { fetchUsers } from '../../api/admin'
import type { CasesView } from '../../api/casework'
import { mayReport, useIssueMe, useShared } from '../../lib/cases'
import { casesHref } from '../../lib/router'
import { WAITING_CASES, loadWaiting } from './InboxCases'
import { ReportPageLink } from './ReportPageLink'

/** New Case: the create-case form (#/cases/inbox/case/new), for those who may open cases (the server says, with the list). */
export function NewCaseButton() {
  const cases = useShared<CasesView>(WAITING_CASES, loadWaiting)
  if (!cases.value?.mayChange) return null
  return (
    <a className="rk-btn" href={casesHref('inbox', ['case', 'new'])} style={{ background: 'var(--accent)', borderColor: 'var(--accent)', color: 'var(--accent-fg)' }} data-testid="cases-new-case">
      New Case
    </a>
  )
}

export function NewReport({ operator }: { operator: boolean }) {
  const me = useIssueMe()
  const [open, setOpen] = useState(false)
  const [user, setUser] = useState('')
  const [known, setKnown] = useState<{ id: string; label: string }[]>([])
  // The people the console knows, to pick from; a SuperAdmin's list, so
  // anyone else types the user name.
  useEffect(() => {
    if (!open) return
    let gone = false
    fetchUsers(true).then(
      (r) => !gone && setKnown((r.users ?? []).map((u) => ({ id: u.username, label: u.name ? `${u.name} (${u.username})` : u.username }))),
      () => undefined,
    )
    return () => {
      gone = true
    }
  }, [open])
  return (
    <span style={{ display: 'inline-flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
      <NewCaseButton />
      {operator && mayReport(me) && (
        <button className="rk-btn" aria-expanded={open} onClick={() => setOpen(!open)} data-testid="cases-on-behalf">
          On behalf of…
        </button>
      )}
      {operator && mayReport(me) && open && (
        <span className="cs-inline" data-testid="cases-on-behalf-form">
          <input className="cs-in" list="cs-users" aria-label="The person whose problem it is: their user name" placeholder="their user name" value={user} onChange={(e) => setUser(e.target.value)} data-testid="cases-on-behalf-user" style={{ width: 200 }} />
          <datalist id="cs-users">
            {known.map((u) => (
              <option key={u.id} value={u.id}>
                {u.label}
              </option>
            ))}
          </datalist>
          {user.trim() ? (
            <ReportPageLink forUser={user.trim()} testId="cases-on-behalf-open" title={`The report page, filing for ${user.trim()}`} onOpened={() => setOpen(false)}>
              Open the report page ↗
            </ReportPageLink>
          ) : (
            <button className="rk-btn" disabled>
              Open the report page ↗
            </button>
          )}
        </span>
      )}
    </span>
  )
}
