// What a proposed change links to (Phase 12, IB-04, IB-06; wave 6b,
// CF-25): the case it was made for, the cases and known issues it
// resolves, and the objects its actions act on.
import { caseOf, proposalId, targetResource, type Proposal } from '../../api/cards'
import { caseHref } from '../../api/casework'
import { aiopsHref } from '../../lib/router'
import { knownHref } from './FindingParts'

/** The objects a card acts on, as resources the Object link reads (IB-06). */
export function objectsOf(p: Pick<Proposal, 'plans'>): string[] {
  const out: string[] = []
  for (const pl of p.plans ?? [])
    for (const t of pl.targets ?? []) {
      const r = targetResource(t)
      if (r && !out.includes(r)) out.push(r)
    }
  return out
}

/** Where a proposed change opens: its drawer in the Inbox, as a notification links it, by its PC-<n> (CF-56). */
export const proposalHref = (id: string) => `#/aiops/inbox/proposal/${encodeURIComponent(proposalId(id))}`

/** The case a card was made for, linked (IB-04). */
export function CaseLink({ p }: { p: Pick<Proposal, 'source'> }) {
  const c = caseOf(p)
  if (!c) return null
  return (
    <a className="rk-link" href={aiopsHref('cases', [String(c.number)])} title={c.label} data-testid="proposal-case">
      {c.id}
    </a>
  )
}

/** Where what a change resolves opens: a case's drawer, or a known issue in Mycroft; null for anything else. */
export function resolvedHref(id: string): string | null {
  if (/^C-\d+$/i.test(id)) return caseHref(id)
  if (/^KI-\d+$/i.test(id)) return knownHref(id.toUpperCase())
  return null
}

/** What a change resolves in words, each with its title when known: "C-12 · web has no limits, KI-3" (CF-39). */
export function resolvesText(ids: string[], titles?: Record<string, string>): string {
  return ids.map((id) => (titleOf(id, titles) ? `${id.toUpperCase()} · ${titleOf(id, titles)}` : id.toUpperCase())).join(', ')
}

/** An id's title, whatever case the id was written in. */
function titleOf(id: string, titles?: Record<string, string>): string {
  if (!titles) return ''
  return titles[id] ?? titles[id.toUpperCase()] ?? ''
}

/**
 * "Resolves C-12 · web has no limits, KI-3 · Certificates renewed by
 * hand": what a change resolves, each linked (CF-25) and followed by its
 * title when the card or the page knows it (CF-39); nothing when it
 * resolves nothing.
 */
export function Resolves({ ids, testId = 'resolves', by, label = true, titles }: { ids?: string[]; testId?: string; by?: Record<string, string>; label?: boolean; titles?: Record<string, string> }) {
  if (!ids?.length) return null
  return (
    <span data-testid={testId} style={{ display: 'inline-flex', gap: 4, flexWrap: 'wrap', alignItems: 'baseline' }}>
      {label && <span>Resolves </span>}
      {ids.map((id, i) => {
        const href = resolvedHref(id)
        const who = by?.[id]
        const tip = who ? (who === 'agent' ? 'Named by the agent that proposed it' : `Linked by ${who}`) : undefined
        const title = titleOf(id, titles)
        return (
          <span key={id}>
            {href ? (
              <a className="rk-link rk-mono" href={href} title={tip} data-testid={`${testId}-link`}>
                {id.toUpperCase()}
              </a>
            ) : (
              <span className="rk-mono">{id}</span>
            )}
            {title && <span data-testid={`${testId}-title`}> · {title}</span>}
            {i < ids.length - 1 && ', '}
          </span>
        )
      })}
    </span>
  )
}
