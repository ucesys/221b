// Who did it (Phase 12, wave 6c, CF-35): every change to a case or a
// proposed change records its actor, a person, an agent (its name, e.g.
// "Auditor") or the console itself, the system.
import { fmtMinutes, minutesSince } from '../lib/format'

export type ActorKind = 'person' | 'agent' | 'system'

export interface Actor {
  name: string
  kind: ActorKind
}

/** An actor as the server sends it now, or a name as it sent one before: both read the same. */
export type ActorLike = Actor | string | null | undefined

const isActor = (a: ActorLike): a is Actor => !!a && typeof a === 'object'

/** Who, by name: "Pat", "Auditor", "System"; "" for nobody. */
export function actorName(a: ActorLike): string {
  if (!a) return ''
  if (!isActor(a)) return a
  if (a.kind === 'system') return 'System'
  return a.name
}

/** The kind of an actor; a bare name is a person's (or the system's, when it says so). */
export function actorKind(a: ActorLike): ActorKind | '' {
  if (!a) return ''
  if (isActor(a)) return a.kind
  return a.toLowerCase() === 'system' ? 'system' : 'person'
}

/** Who, as a row says it: "Pat", "Auditor (agent)", "System". */
export function actorText(a: ActorLike): string {
  const name = actorName(a)
  if (!name) return ''
  return actorKind(a) === 'agent' ? `${name} (agent)` : name
}

/** Who, with what they are, as a history entry says it: "Pat (person)", "Auditor (agent)", "System". */
export function actorFull(a: ActorLike): string {
  const name = actorName(a)
  if (!name) return ''
  const k = actorKind(a)
  return k === 'system' ? 'System' : `${name} (${k})`
}

/** The last change of a row, in a line: "Pat · 5m ago", "Auditor (agent) · 2h ago"; "" when it says nothing. */
export function lastChangeText(by: ActorLike, at: string | undefined, now: number): string {
  const who = actorText(by)
  const when = at ? `${fmtMinutes(minutesSince(at, now))} ago` : ''
  return [who, when].filter(Boolean).join(' · ')
}
