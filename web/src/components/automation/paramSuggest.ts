// Run parameters typed with suggestions (FU-03): what each kind of
// parameter a script declares is, in words, and what the Run dialog
// offers while one is typed. A script's `# 221b:param` line makes a
// parameter one of two kinds: a variable, a value the script reads from
// its environment that fleets and nodes set on the Variables page, or a
// secret (`secret`), a reference to a Kubernetes Secret whose value the
// run reads as the person who starts it. Suggestions never carry a
// Secret's value (X-01): a secret parameter is offered Secrets by
// namespace, name and key alone.
//
// A variable parameter's box searches every variable of the cluster by
// name and value (FU-04): picking one binds the parameter to it by name,
// so each node gets its own value of it; anything typed is a constant,
// the same on every node. A sensitive or secret-looking variable is
// offered by its name alone. A parameter whose own name a fleet or a node
// sets for the run's nodes starts bound to that variable (FU-06), which is
// what each node inherits anyway, so nothing is sent for it.
import { ALL_NODES, bindValue, boundTo, constantOf, inherited, type AutomationFleet, type Param, type Run, type SecretRef } from '../../api/automation'

export type ParamKind = 'variable' | 'secret'

export const paramKind = (p: Pick<Param, 'secret'>): ParamKind => (p.secret ? 'secret' : 'variable')

/** Each kind in words: its name, and what a value of it is. */
export const PARAM_KINDS: Record<ParamKind, { label: string; help: string }> = {
  variable: { label: 'Variable', help: 'A value the script reads from its environment; fleets and nodes set it on the Variables page.' },
  secret: { label: 'Secret', help: 'A reference to a Kubernetes Secret, secret:namespace/name/key; its value is read as you when the run starts and never shown.' },
}

/** A parameter's line under its name: "Variable · required · The disk to wipe". */
export function paramTypeText(p: Pick<Param, 'secret' | 'required' | 'desc'>): string {
  return [PARAM_KINDS[paramKind(p)].label, p.required ? 'required' : 'optional', p.desc ?? ''].filter(Boolean).join(' · ')
}

/** One suggestion: the value it puts in the field, and where it comes from. */
export interface ParamSuggestion {
  /** What it gives the run: the text, or var:NAME for a variable. */
  value: string
  note: string
  /** What the list shows instead of the value: a variable's name. */
  label?: string
  /** A variable's values on the run's nodes. */
  detail?: string
  /** The variable picking it binds to (FU-04). */
  bind?: string
}

/** At most this many suggestions show at once. */
export const MAX_SUGGESTIONS = 8

// --- FU-04: the variables a parameter may bind to ---

/** What a variable is on the run's nodes: its values, how many nodes have one, those without, and those whose fleets clash. */
export interface VariableSpread {
  values: string[]
  set: number
  unset: string[]
  conflict: string[]
}

export function variableSpread(fleets: AutomationFleet[], nodes: string[], name: string): VariableSpread {
  const values = new Set<string>()
  const out: VariableSpread = { values: [], set: 0, unset: [], conflict: [] }
  for (const n of nodes) {
    const got = inherited(fleets, n, name)
    if (got.conflict) out.conflict.push(n)
    else if (got.value === undefined) out.unset.push(n)
    else {
      out.set++
      values.add(got.value)
    }
  }
  out.values = [...values].sort()
  return out
}

/** The value every node of the run gets, when they all get the same one: what a parameter's box is filled with. */
export function uniformValue(fleets: AutomationFleet[], nodes: string[], name: string): string | undefined {
  const s = variableSpread(fleets, nodes, name)
  return nodes.length > 0 && s.unset.length === 0 && s.conflict.length === 0 && s.values.length === 1 ? s.values[0] : undefined
}

/** Every variable a fleet or a node of the cluster sets, by name. */
export function variableNames(fleets: AutomationFleet[]): string[] {
  const names = new Set<string>()
  for (const f of fleets) {
    for (const k of Object.keys(f.params?.values ?? {})) names.add(k)
    for (const m of Object.values(f.params?.nodes ?? {})) for (const k of Object.keys(m ?? {})) names.add(k)
  }
  return [...names].sort()
}

/** Where a variable is set: "fleet Workers", "node w1". */
export function placesOf(fleets: AutomationFleet[], name: string): string[] {
  const out: string[] = []
  for (const f of fleets) {
    if (f.params?.values && Object.prototype.hasOwnProperty.call(f.params.values, name)) out.push(f.id === ALL_NODES ? f.name : `fleet ${f.name}`)
    for (const [n, m] of Object.entries(f.params?.nodes ?? {})) if (m && Object.prototype.hasOwnProperty.call(m, name)) out.push(`node ${n}`)
  }
  return [...new Set(out)]
}

/** Every value a variable has anywhere in the cluster. */
function valuesOf(fleets: AutomationFleet[], name: string): string[] {
  const out: string[] = []
  for (const f of fleets) {
    const v = f.params?.values?.[name]
    if (v !== undefined) out.push(v)
    for (const m of Object.values(f.params?.nodes ?? {})) {
      const nv = m?.[name]
      if (nv !== undefined) out.push(nv)
    }
  }
  return out
}

const SECRET_NAME = /PASS(WORD|WD)?|SECRET|TOKEN|CREDENTIAL|PRIVATE|API_?KEY|(^|_)KEY$/

/** A value that looks like a secret: a Secret's reference, or a key. */
export const secretLikeValue = (v: string) => v.startsWith('secret:') || v.includes('-----BEGIN ')

/** A variable whose values are never offered: marked sensitive on a fleet, named like a secret, or holding one. */
export function hiddenVariable(fleets: AutomationFleet[], name: string): boolean {
  return fleets.some((f) => (f.params?.sensitive ?? []).includes(name)) || SECRET_NAME.test(name) || valuesOf(fleets, name).some(secretLikeValue)
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`

/** A variable's values on the run's nodes, in words: "chrony", "varies: 3 values", "not set on 2 nodes". */
export function spreadText(s: VariableSpread, nodes: number, hidden: boolean): string {
  if (nodes === 0) return 'no node picked'
  if (s.set === 0 && s.conflict.length === 0) return 'not set on this run’s nodes'
  const parts: string[] = []
  if (hidden) parts.push(s.set === nodes ? (nodes === 1 ? 'value hidden' : `value hidden, set on all ${nodes} nodes`) : `value hidden, set on ${s.set} of ${nodes} nodes`)
  else if (s.values.length === 1) parts.push(s.values[0]!)
  else if (s.values.length > 1) parts.push(`varies: ${plural(s.values.length, 'value')}`)
  if (!hidden && s.unset.length) parts.push(`not set on ${plural(s.unset.length, 'node')}`)
  if (s.conflict.length) parts.push(`on two fleets for ${plural(s.conflict.length, 'node')}`)
  return parts.join(' · ')
}

// --- FU-06: a bound variable's chip ---

/** Whether a fleet or a node sets a variable for any node of the run: then a parameter of its name is bound to it as the dialog opens. */
export function setInScope(fleets: AutomationFleet[], nodes: string[], name: string): boolean {
  const s = variableSpread(fleets, nodes, name)
  return s.set > 0 || s.conflict.length > 0
}

/** What a bound variable's chip says after its name: "3 values", its one value, "value hidden", "not set". */
export function chipDetail(s: VariableSpread, hidden: boolean): string {
  if (s.values.length > 1) return plural(s.values.length, 'value')
  if (s.values.length === 1) return hidden ? 'value hidden' : s.values[0]!
  return s.conflict.length ? 'on two fleets' : 'not set'
}

/** A bound variable's chip's tooltip: its value on each node of the run (at most 12), then where it is set; a hidden one's places alone. */
export function chipTitle(fleets: AutomationFleet[], nodes: string[], name: string, hidden: boolean): string {
  const places = placesOf(fleets, name)
  const where = places.length ? `Set on ${places.join(', ')}.` : 'Set nowhere.'
  const named = nodes.filter(Boolean)
  if (hidden || named.length === 0) return where
  const lines = named.slice(0, 12).map((n) => {
    const got = inherited(fleets, n, name)
    return `${n}: ${got.conflict ? `on ${got.conflict.join(' and ')}` : (got.value ?? 'not set')}`
  })
  if (named.length > 12) lines.push(`… and ${named.length - 12} more`)
  return [...lines, where].join('\n')
}

/**
 * What a variable parameter's box offers (FU-04): every variable of the
 * cluster whose name or value holds what was typed, case aside, its own
 * name first, then names, then values; then this script's earlier runs'
 * own values, newest first. A variable shows its values on the run's
 * nodes and where it is set; a hidden one (hiddenVariable) its name
 * alone, and its values are not searched.
 */
export function variableSuggestions(param: string, fleets: AutomationFleet[], nodes: string[], runs: Run[], script: string, typed: string, limit = MAX_SUGGESTIONS): ParamSuggestion[] {
  const t = typed.trim().toLowerCase()
  const ranked: { rank: number; s: ParamSuggestion }[] = []
  for (const name of variableNames(fleets)) {
    const hidden = hiddenVariable(fleets, name)
    const lower = name.toLowerCase()
    let rank = -1
    if (name === param && (!t || lower.includes(t))) rank = 0
    else if (!t) rank = 3
    else if (lower.startsWith(t)) rank = 1
    else if (lower.includes(t)) rank = 2
    else if (!hidden && valuesOf(fleets, name).some((v) => v.toLowerCase().includes(t))) rank = 4
    if (rank < 0) continue
    const places = placesOf(fleets, name)
    ranked.push({
      rank,
      s: {
        value: bindValue(name),
        label: name,
        bind: name,
        detail: spreadText(variableSpread(fleets, nodes, name), nodes.length, hidden),
        note: places.length > 2 ? `${places.slice(0, 2).join(', ')} +${places.length - 2}` : places.join(', '),
      },
    })
  }
  ranked.sort((a, b) => a.rank - b.rank || (a.s.label ?? '').localeCompare(b.s.label ?? ''))
  const out = ranked.map((r) => r.s)
  const mine = runs.filter((r) => r.script === script && r.overrides && Object.prototype.hasOwnProperty.call(r.overrides, param)).sort((a, b) => b.id - a.id)
  const texts = new Set<string>()
  for (const r of mine) {
    const v = r.overrides![param]!
    // A binding names a variable the list offers already, or one gone.
    if (boundTo(v)) continue
    const text = constantOf(v)
    if (!text || texts.has(text) || text === typed || secretLikeValue(text) || (t && !text.toLowerCase().includes(t))) continue
    texts.add(text)
    out.push({ value: text, note: `run #${r.id}` })
  }
  return out.slice(0, limit)
}

/** The reference a key of a Secret is named by. */
export const secretRefText = (ns: string, name: string, key: string) => `secret:${ns}/${name}/${key}`

/**
 * What a secret parameter is offered (FU-03): the reference that applies
 * to the first node, then every key of every Secret the person may list,
 * as secret:namespace/name/key. Names only: no value is ever fetched.
 * What was typed matches with or without its "secret:".
 */
export function secretSuggestions(name: string, fleets: AutomationFleet[], node: string, refs: SecretRef[], typed: string, limit = MAX_SUGGESTIONS): ParamSuggestion[] {
  const t = typed
    .trim()
    .toLowerCase()
    .replace(/^secret:/, '')
  const holds = (value: string) =>
    !t ||
    value
      .toLowerCase()
      .replace(/^secret:/, '')
      .includes(t)
  const out: ParamSuggestion[] = []
  const seen = new Set<string>()
  const add = (s: ParamSuggestion) => {
    if (out.length >= limit || seen.has(s.value) || s.value === typed || !holds(s.value)) return
    seen.add(s.value)
    out.push(s)
  }
  const now = node ? inherited(fleets, node, name) : {}
  if (now.value) add({ value: now.value, note: `applies to ${node}${now.from ? `, from ${now.from}` : ''}` })
  for (const r of refs) for (const k of r.keys) add({ value: secretRefText(r.namespace, r.name, k), note: `key ${k} of Secret ${r.namespace}/${r.name}` })
  return out
}
