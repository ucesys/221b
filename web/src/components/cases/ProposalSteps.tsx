// A proposed change's steps, one line each (Phase 12, CF-92): "1. Create
// fleet burst pool: labels power=on-demand · 3 nodes now · risk low", or
// the exact command Watson runs; under the line only what Watson read that
// the line does not say already, and the rest (the agent's own words, what
// it acts on, the fingerprint) folded in Details. The steps the console
// makes itself are headed "What it changes in the console", the commands
// "What Watson will run", both when a proposal has both.
import { ACTION_KINDS, commandOf, isWatsonCommand, targetResource, type CardAction, type CardPlan } from '../../api/cards'
import { ObjectLink } from './ObjectLink'

const RISK = { low: 'ok', medium: 'warn', high: 'err' } as const

/** A fact on a step's line, with the line Watson wrote as its tooltip. */
export interface StepFact {
  text: string
  title?: string
  tone?: 'warn'
}

/** What a step's line says, built from its kind, its arguments and its plan. */
export interface StepParts {
  /** The exact command, for a kubectl or helm step; "" otherwise. */
  command: string
  /** "Create fleet", "Label host"; the plan's summary for a kind without its own words. */
  verb: string
  /** The object, in bold: the fleet's or the host's name. */
  name: string
  /** Its key arguments: "labels power=on-demand", "adds zone=b". */
  args: string
  facts: StepFact[]
  /** What Watson read that the line does not say. */
  notes: string[]
  /** What Watson read that the line says already, kept in Details. */
  more: string[]
}

const str = (x: unknown) => (typeof x === 'string' ? x.trim() : '')
const list = (x: unknown) => (Array.isArray(x) ? x.filter((v): v is string => typeof v === 'string' && v.trim() !== '') : [])
const pairs = (x: unknown) =>
  x && typeof x === 'object' && !Array.isArray(x)
    ? Object.entries(x as Record<string, unknown>)
        .filter(([, v]) => typeof v === 'string')
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => `${k}=${v as string}`)
    : []

/** "3 nodes now: a, b, c" (a fleet's members as they are now) becomes the fact "3 nodes now". */
const MEMBERS = /^(?:(\d+)|no) (?:nodes?|hosts?|members?)\b[^:]*\bnow\b/i

function membersFact(line: string): StepFact | null {
  const m = MEMBERS.exec(line.trim())
  if (!m) return null
  const short = line.split(':')[0]!.trim()
  return { text: short, title: short === line.trim() ? undefined : line, tone: !m[1] || m[1] === '0' ? 'warn' : undefined }
}

/** A fleet's selection from its arguments: "labels a=b and taints c", "nodes a, b", or "every node". */
function fleetArgs(args: Record<string, unknown>): string {
  const nodes = list(args.nodes)
  if (str(args.kind) === 'nodes') return nodes.length ? `nodes ${nodes.join(', ')}` : 'no node yet'
  const parts = []
  if (list(args.labels).length) parts.push(`labels ${list(args.labels).join(', ')}`)
  if (list(args.taints).length) parts.push(`taints ${list(args.taints).join(', ')}`)
  return parts.length ? parts.join(' and ') : 'every node'
}

/** A fleet change's arguments, where no plan says them. */
function fleetUpdateArgs(args: Record<string, unknown>): string {
  const out: string[] = []
  if (str(args.name)) out.push(`renames it ${str(args.name)}`)
  if (Array.isArray(args.labels)) out.push(`labels ${list(args.labels).join(', ') || 'none'}`)
  if (Array.isArray(args.taints)) out.push(`taints ${list(args.taints).join(', ') || 'none'}`)
  if (Array.isArray(args.nodes)) out.push(`nodes ${list(args.nodes).join(', ') || 'none'}`)
  if (list(args.addNodes).length) out.push(`adds ${list(args.addNodes).join(', ')}`)
  if (list(args.removeNodes).length) out.push(`removes ${list(args.removeNodes).join(', ')}`)
  if (typeof args.deploy === 'string') out.push(`pins its applications to ${args.deploy || 'no label'}`)
  if (typeof args.desc === 'string') out.push('changes its description')
  if (typeof args.color === 'number') out.push('changes its colour')
  return out.join(', ')
}

/**
 * One step's line (CF-92). A fleet change, a host's labels and Automation
 * parameters take their key arguments from Watson's reading where there is
 * one (its short lines, "adds zone=b", "retries: "2" → "3""), so the line
 * says what changes against what is there; its sentences stay under the
 * line. A fleet's members now become a fact on the line.
 */
export function stepParts(a: CardAction, plan?: CardPlan): StepParts {
  const args = a.args ?? {}
  const impact = plan?.impact ?? []
  const p: StepParts = { command: commandOf(a), verb: '', name: '', args: '', facts: [], notes: [], more: [] }
  // The lines left for under the line, once the facts are taken out.
  let rest: string[] = []
  for (const l of impact) {
    const f = membersFact(l)
    if (f) p.facts.push(f)
    else rest.push(l)
  }
  // Lines the step's line says already are moved to Details.
  const hide = (re: RegExp) => {
    p.more.push(...rest.filter((l) => re.test(l)))
    rest = rest.filter((l) => !re.test(l))
  }
  // The short lines of a change list (lower case: "adds label x") become the line's arguments.
  const fromImpact = (fallback: string) => {
    const short = rest.filter((l) => !/^[A-Z]/.test(l))
    rest = rest.filter((l) => /^[A-Z]/.test(l))
    return short.length ? short.join(', ') : fallback
  }
  switch (a.kind) {
    case 'kubectl':
    case 'helm':
      break
    case 'fleet.create':
      p.verb = 'Create fleet'
      p.name = str(args.name)
      p.args = fleetArgs(args)
      hide(/^Creates the fleet /)
      break
    case 'fleet.update':
      p.verb = 'Change fleet'
      p.name = plan?.summary?.startsWith('Change fleet ') ? plan.summary.slice('Change fleet '.length) : str(args.name) || str(args.id)
      p.args = fromImpact(fleetUpdateArgs(args))
      hide(/^Afterwards it holds /)
      break
    case 'host.add': {
      p.verb = 'Add host'
      p.name = str(args.name)
      const labels = pairs(args.labels)
      p.args = [str(args.address), labels.length ? `labels ${labels.join(', ')}` : ''].filter(Boolean).join(', ')
      hide(/^Adds host /)
      break
    }
    case 'host.label':
      p.verb = 'Label host'
      p.name = str(args.name)
      p.args = fromImpact([...pairs(args.set), ...list(args.remove).map((k) => `removes ${k}`)].join(', '))
      break
    case 'host.remove':
      p.verb = 'Remove host'
      p.name = str(args.name)
      break
    case 'automation.params':
      p.verb = 'Set Automation parameters of fleet'
      p.name = str(args.fleet)
      p.args = fromImpact([...pairs(args.values), ...list(args.unset).map((k) => `unsets ${k}`)].join(', '))
      break
    default:
      // A console record, or a kind this page does not know: Watson's summary says it.
      p.verb = plan?.summary || ACTION_KINDS[a.kind] || a.kind
  }
  if (!p.command && !p.name && !p.verb) p.verb = ACTION_KINDS[a.kind] ?? a.kind
  p.notes = rest
  return p
}

/** One step on one line, with what Watson read under it and the rest in Details (CF-92). */
export function StepLine({ a, plan, n, of, cluster }: { a: CardAction; plan?: CardPlan; n: number; of: number; cluster: string }) {
  const s = stepParts(a, plan)
  const targets = plan?.targets ?? []
  const linked = (t: string) => {
    // Each object linked to its page, with how long it has run (IB-06).
    const r = targetResource(t)
    return r ? <ObjectLink cluster={cluster} resource={r} label={t} /> : t
  }
  const sep = <span style={{ color: 'var(--fg3)' }}> · </span>
  const folded = !!a.impact || s.more.length > 0 || (!s.command && targets.length > 0) || !!plan?.fingerprint
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="proposal-action" data-kind={a.kind}>
      <div style={{ fontSize: 13, lineHeight: 1.5 }} data-testid="proposal-step">
        {of > 1 && <strong>{n}. </strong>}
        {s.command ? (
          <code className="rk-mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }} data-testid="proposal-step-command">
            $ {s.command}
          </code>
        ) : (
          <>
            {s.verb}
            {s.name && (
              <>
                {' '}
                <strong>{s.name}</strong>
              </>
            )}
            {s.args && `: ${s.args}`}
          </>
        )}
        {/* A command's objects on its line, linked; a console step names its object already. */}
        {s.command &&
          targets.map((t) => (
            <span key={t} data-testid="proposal-targets">
              {sep}
              {linked(t)}
            </span>
          ))}
        {s.facts.map((f, i) => (
          <span key={i}>
            {sep}
            <span className={f.tone === 'warn' ? 'tb-err' : undefined} title={f.title} data-testid="proposal-step-fact">
              {f.text}
            </span>
          </span>
        ))}
        {a.risk && (
          <>
            {sep}
            <span className="rk-tag" data-tone={RISK[a.risk]}>
              risk {a.risk}
            </span>
          </>
        )}
      </div>
      {s.notes.map((l, i) => (
        <span key={i} className={/approving fails/.test(l) ? 'tb-err' : undefined} style={{ fontSize: 12.5, color: /approving fails/.test(l) ? undefined : 'var(--fg2)', paddingLeft: of > 1 ? 18 : 0 }} data-testid="proposal-step-note">
          {l}
        </span>
      ))}
      {plan?.unread && (
        <span className="tb-err" style={{ fontSize: 12.5, paddingLeft: of > 1 ? 18 : 0 }}>
          Watson could not read its target when it was proposed: {plan.unread}
        </span>
      )}
      {plan?.warning && (
        <span style={{ fontSize: 12.5, color: 'var(--warn)' }} data-testid="proposal-plan-warning">
          {plan.warning}
        </span>
      )}
      {folded && (
        <details style={{ fontSize: 12.5, color: 'var(--fg2)', paddingLeft: of > 1 ? 18 : 0 }} data-testid="proposal-step-details">
          <summary style={{ cursor: 'pointer', color: 'var(--fg3)' }}>Details</summary>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2, marginTop: 4 }}>
            {a.impact && <span data-testid="proposal-step-agent">The agent says: {a.impact}</span>}
            {s.more.map((l, i) => (
              <span key={i}>{l}</span>
            ))}
            {!s.command && targets.length > 0 && (
              <span>
                Acts on:{' '}
                {targets.map((t, i) => (
                  <span key={t}>
                    {linked(t)}
                    {i < targets.length - 1 && ', '}
                  </span>
                ))}
              </span>
            )}
            {plan?.fingerprint && (
              <span>
                Fingerprint when proposed: <span className="rk-mono">{plan.fingerprint}</span>
              </span>
            )}
          </div>
        </details>
      )}
    </div>
  )
}

/**
 * The steps under their heading (CF-92): "What it changes in the console"
 * when no step is a Watson command, "What Watson will run" when all are,
 * both sections, each in the proposal's order, when it has both. Once
 * decided they say what it was to do.
 */
export function ProposalSteps({ actions, plans, cluster, open }: { actions: CardAction[]; plans: CardPlan[]; cluster: string; open: boolean }) {
  const steps = actions.map((a, i) => ({ a, i }))
  const groups = [
    { label: open ? 'What it changes in the console' : 'What it was to change in the console', steps: steps.filter((s) => !isWatsonCommand(s.a)), kind: 'console' },
    { label: open ? 'What Watson will run' : 'What Watson was to run', steps: steps.filter((s) => isWatsonCommand(s.a)), kind: 'watson' },
  ].filter((g) => g.steps.length > 0)
  return (
    <>
      {groups.map((g) => (
        <section key={g.kind} style={{ display: 'flex', flexDirection: 'column', gap: 10 }} aria-label={g.label} data-testid="proposal-steps" data-kind={g.kind}>
          <h3 className="rk-h">{g.label}</h3>
          {g.steps.map(({ a, i }) => (
            <StepLine key={i} a={a} plan={plans[i]} n={i + 1} of={actions.length} cluster={cluster} />
          ))}
        </section>
      ))}
    </>
  )
}
