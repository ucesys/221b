// The AIOps area (Phase 12, CS-01, ADR-88): the third area beside
// Kubernetes and Automation, for everything agents and people raise that
// needs following up, across clusters. Its tabs are in the top bar; this
// is the page under it. Not admin-only: each tab is offered to whoever has
// something in it (lib/cases.ts), and each part asks the server, which
// decides again.
import { useEffect } from 'react'
import '../../styles/cases.css'
import type { Me } from '../../api/types'
import { casesAccess, useIssueMe, useIssueMeSettled } from '../../lib/cases'
import { AIOPS_TABS, navigate, withParams, type AIOpsTab, type Route } from '../../lib/router'
import { InboxTab } from './InboxTab'
import { JobsArea } from './JobsArea'
import { MycroftTab } from './MycroftTab'

interface Props {
  route: Route
  me: Me | null
  onToast: (msg: string) => void
  /** The alerts going on in the cluster open, and its name, for the Inbox's link (CF-81). */
  alerts?: number
  clusterName?: string
}

export function AIOpsPage({ route, me, onToast, alerts, clusterName }: Props) {
  const im = useIssueMe()
  const settled = useIssueMeSettled()
  const access = casesAccess(me, im)
  const asked = (route.path[0] ?? 'inbox') as AIOpsTab
  // A tab the person is not offered opens the first one they are.
  const tab: AIOpsTab | null = access.tabs.includes(asked) ? asked : (access.tabs[0] ?? null)
  const label = AIOPS_TABS.find((t) => t.id === tab)?.label ?? 'AIOps'
  // The old Administration › Approvals link (CS-06) shows the Inbox's
  // address once it has opened it; an old Mycroft › Validation rules link
  // the rules' address in Jobs (CF-89); and an address of before CF-104,
  // #/cases/…, its address under #/aiops.
  const old = typeof window !== 'undefined' && /^#\/(c\/[^/]+\/)?(admin\/approvals|cases(\/|\?|$)|aiops\/mycroft\/rules)/.test(window.location.hash)
  useEffect(() => {
    if (old) navigate(withParams(route, {}), true)
  }, [old, route])
  // The Inbox, Mycroft and Jobs take the wide column (CF-66).
  const wide = tab === 'inbox' || tab === 'mycroft' || tab === 'jobs'
  return (
    <main className={'rk-main cs-page' + (wide ? ' cs-page-inbox' : '')} aria-label="AIOps" data-testid="cases-page">
      <h1 className="rk-sr">AIOps: {label}</h1>
      {me === null || !settled ? (
        <p className="tb-hint">Loading…</p>
      ) : tab === null ? (
        <section className="rk-card" style={{ padding: '20px 24px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="cases-refusal">
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Nothing in Cases is for you</h2>
          <p style={{ margin: 0, color: 'var(--fg2)' }}>Cases holds approvals, problem reports and what the agents found. Your role includes none of them; ask a SuperAdmin if you need it.</p>
        </section>
      ) : (
        <>
          {tab === 'inbox' && <InboxTab route={route} access={access} onToast={onToast} alerts={alerts} clusterName={clusterName} />}
          {tab === 'jobs' && <JobsArea route={route} onToast={onToast} rules={access.superAdmin} />}
          {tab === 'mycroft' && <MycroftTab route={route} access={access} onToast={onToast} />}
        </>
      )}
    </main>
  )
}
