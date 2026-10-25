import { useEffect, useRef, useState } from 'react'
import type { Fleet, FleetRow, NodeRow } from '../api/types'
import { copyText } from '../lib/clipboard'
import { has, isList, joinCommands, joinNotes, LABEL_KEY_RE, LABEL_VAL_RE, labelValueOf, leaveCommands, leaveNotes, listText, newId, nextColor, ownLabels, reqText, splitLabel } from '../lib/fleets'
import { nWord } from '../lib/format'
import { labelText } from '../lib/nodes'
import { CopyIcon } from './Icons'
import { Select } from './Select'
import { useWatsonApi } from './Watson'

export type FleetNodesMode = 'new' | 'add' | 'remove'

interface Props {
  mode: FleetNodesMode
  /** The picked nodes. */
  nodes: NodeRow[]
  all: NodeRow[]
  fleets: Fleet[]
  rows: FleetRow[]
  /** The fleet the page shows: the first choice to add to or remove from. */
  current?: Fleet
  onClose: () => void
  /** Saves a new fleet; the page then shows it. */
  onCreate: (f: Fleet) => Promise<void>
  /** Saves the fleets after a list changed. */
  onSave: (next: Fleet[]) => Promise<void>
  onDone: (message: string) => void
}

/**
 * Picked nodes into a new fleet, into a fleet, or out of one (asked after
 * 0.7.1). A list of nodes changes in the console alone, and a node may be
 * on any number of lists. A fleet of labels and taints takes a node only
 * by its own labels and taints: the dialog says which commands that takes
 * and what else they change, and hands them to Watson, where nothing runs
 * until someone approves each card.
 */
export function FleetNodes({ mode, nodes, all, fleets, rows, current, onClose, onCreate, onSave, onDone }: Props) {
  const watson = useWatsonApi()
  const runs = watson.available && !watson.readOnly
  // The fleets to choose from: to add to, those not every picked node is in; to remove from, those one of them is in.
  const choices = mode === 'remove' ? fleets.filter((f) => nodes.some((n) => has(f, n))) : fleets.filter((f) => nodes.some((n) => !has(f, n)))
  const [fleetId, setFleetId] = useState(() => (current && choices.some((f) => f.id === current.id) ? current.id : (choices[0]?.id ?? '')))
  const own = ownLabels(nodes, all)
  const [name, setName] = useState('')
  const [how, setHow] = useState<'list' | 'own' | 'new'>('list')
  const [ownPick, setOwnPick] = useState(own[0] ?? '')
  const [key, setKey] = useState('fleet')
  const [val, setVal] = useState('')
  const [valTouched, setValTouched] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const first = useRef<HTMLInputElement>(null)
  useEffect(() => {
    first.current?.focus()
  }, [])

  const names = nodes.map((n) => n.name)
  const value = valTouched ? val : labelValueOf(name)
  const nameTaken = fleets.some((f) => f.name.toLowerCase() === name.trim().toLowerCase())
  const keyBad = !LABEL_KEY_RE.test(key.trim())
  const valBad = !LABEL_VAL_RE.test(value)
  const label = how === 'own' ? ownPick : `${key.trim()}=${value}`
  const draft: Fleet | null =
    mode !== 'new' ? null : how === 'list' ? { id: newId(name.trim(), fleets), name: name.trim(), color: nextColor(fleets), labels: [], taints: [], kind: 'nodes', nodes: names } : { id: newId(name.trim(), fleets), name: name.trim(), color: nextColor(fleets), labels: label ? [label] : [], taints: [] }
  const fleet = mode === 'new' ? (draft ?? undefined) : fleets.find((f) => f.id === fleetId)
  const list = !!fleet && isList(fleet)

  const inIt = fleet && mode !== 'new' ? nodes.filter((n) => has(fleet, n)) : []
  const outOf = fleet && mode !== 'new' ? nodes.filter((n) => !has(fleet, n)) : []
  const commands = !fleet || list ? [] : mode === 'remove' ? leaveCommands(fleet, inIt) : joinCommands(fleet, nodes)
  const notes = !fleet || list ? [] : mode === 'remove' ? leaveNotes(fleet, inIt, rows) : joinNotes(fleet, nodes, rows)
  // Nodes outside the selection that already have a new fleet's label join it too.
  const alsoIn = mode === 'new' && how === 'new' && !keyBad && !valBad ? all.filter((n) => !names.includes(n.name) && key.trim() in n.labels && n.labels[key.trim()] === value) : []
  if (alsoIn.length) notes.push(`${listText(alsoIn.map((n) => n.name))} already ${alsoIn.length === 1 ? 'has' : 'have'} ${label}, so ${alsoIn.length === 1 ? 'it is' : 'they are'} in the fleet too.`)

  const problem =
    mode === 'new'
      ? !name.trim()
        ? 'Name the fleet.'
        : nameTaken
          ? 'Another fleet has this name.'
          : how === 'new' && keyBad
            ? 'The label key is not valid: letters, digits, dashes, dots and underscores, with an optional prefix such as example.com/.'
            : how === 'new' && valBad
              ? 'The label value is not valid: up to 63 letters, digits, dashes, dots and underscores.'
              : ''
      : !fleet
        ? mode === 'add'
          ? 'Every one of these nodes is in every fleet already.'
          : 'None of these nodes is in a fleet.'
        : ''

  const pickedText = nWord(nodes.length, 'node', 'nodes')
  const title = mode === 'new' ? `New fleet from ${pickedText}` : mode === 'add' ? `Add ${pickedText} to a fleet` : `Remove ${pickedText} from a fleet`
  const why = mode === 'new' ? `Puts ${nodes.length === 1 ? 'this node' : 'these nodes'} in the new fleet “${draft?.name}”.` : mode === 'add' ? `Adds ${nodes.length === 1 ? 'this node' : 'these nodes'} to the fleet “${fleet?.name}”.` : `Takes ${inIt.length === 1 ? 'this node' : 'these nodes'} out of the fleet “${fleet?.name}”.`

  const copy = async () => {
    if (await copyText(commands.join('\n'))) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }
  const go = async () => {
    if (problem || !fleet) return
    setBusy(true)
    setError('')
    try {
      if (mode === 'new' && draft) await onCreate(draft)
      if (mode !== 'new' && list) {
        const next = mode === 'add' ? [...(fleet.nodes ?? []), ...outOf.map((n) => n.name)] : (fleet.nodes ?? []).filter((n) => !names.includes(n))
        await onSave(fleets.map((f) => (f.id === fleet.id ? { ...f, nodes: next } : f)))
        onDone(mode === 'add' ? `Added ${nWord(outOf.length, 'node', 'nodes')} to “${fleet.name}”.` : `Removed ${nWord(inIt.length, 'node', 'nodes')} from “${fleet.name}”.`)
        return
      }
      if (commands.length && runs) watson.send(commands.join('\n'), { from: 'Fleets', why })
      if (commands.length && !runs) await copyText(commands.join('\n'))
      const n = commands.length
      const sent = n ? (runs ? ` Watson has ${nWord(n, 'command', 'commands')} for approval; ${mode === 'remove' ? 'the nodes leave' : 'the nodes join'} once ${n === 1 ? 'it runs' : 'they run'}.` : ` The ${nWord(n, 'command is', 'commands are')} copied: run ${n === 1 ? 'it' : 'them'} to ${mode === 'remove' ? 'take the nodes out' : 'bring the nodes in'}.`) : ''
      onDone(mode === 'new' ? `Created the fleet “${draft?.name}”.${sent}` : sent.trim())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  const goLabel = mode === 'new' ? (commands.length && runs ? 'Create and send to Watson' : commands.length ? 'Create and copy the commands' : 'Create fleet') : list ? (mode === 'add' ? 'Add to fleet' : 'Remove from fleet') : runs ? 'Send to Watson' : 'Copy the commands'
  const fleetOption = (f: Fleet): [string, string] => [f.id, `${f.name} — ${reqText(f)}`]

  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 50, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16 }}>
      <button aria-label="Close" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <section
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="rk-card"
        data-testid="fleet-nodes"
        data-mode={mode}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: 'min(620px, 100%)', maxHeight: '100%', overflow: 'auto', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{title}</h2>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }} data-testid="fleet-nodes-picked">
          {nodes.map((n) => {
            const isIn = fleet && mode !== 'new' && has(fleet, n)
            return (
              <span key={n.name} className="rk-chip" data-member={n.name}>
                {n.name}
                {mode === 'add' && isIn && <em>already in</em>}
                {mode === 'remove' && !isIn && <em>not in it</em>}
              </span>
            )
          })}
        </div>

        {mode === 'new' && (
          <>
            <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
              Name
              <span className="rk-field" style={{ width: '100%' }}>
                <input ref={first} value={name} onChange={(e) => setName(e.target.value)} placeholder="Database nodes" data-testid="fleet-nodes-name" />
              </span>
            </label>
            <fieldset style={{ border: 0, margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 10 }}>
              <legend style={{ fontSize: 12, color: 'var(--fg3)', marginBottom: 6 }}>The fleet is</legend>
              <label style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '2px 8px', fontSize: 12.5 }}>
                <input type="radio" name="fleet-how" checked={how === 'list'} onChange={() => setHow('list')} data-testid="fleet-nodes-list" />
                <span>
                  A list of {nodes.length === 1 ? 'this node' : 'these nodes'}, kept in 221B
                  <span className="rk-fhint" style={{ display: 'block' }}>
                    Nothing changes in the cluster. A node may be on any number of lists; a new node joins only when someone adds it.
                  </span>
                </span>
              </label>
              {own.length > 0 && (
                <label style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
                  <input type="radio" name="fleet-how" checked={how === 'own'} onChange={() => setHow('own')} data-testid="fleet-nodes-own" />
                  Every node with a label only these have:
                  {own.length === 1 ? (
                    <span className="rk-mono">{labelText(...splitLabel(own[0]!))}</span>
                  ) : (
                    <Select label="Label" value={ownPick} options={own.map((l): [string, string] => [l, l])} onChange={(v) => setOwnPick(v)} disabled={how !== 'own'} testId="fleet-nodes-own-pick" />
                  )}
                </label>
              )}
              <label style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
                <input type="radio" name="fleet-how" checked={how === 'new'} onChange={() => setHow('new')} data-testid="fleet-nodes-newlabel" />
                Every node with a new label, put on these
                <span className="rk-field" style={{ width: 150 }}>
                  <span className="rk-sr">Label key</span>
                  <input className="rk-mono" value={key} onChange={(e) => setKey(e.target.value)} onFocus={() => setHow('new')} aria-invalid={how === 'new' && keyBad} data-testid="fleet-nodes-key" />
                </span>
                =
                <span className="rk-field" style={{ width: 170 }}>
                  <span className="rk-sr">Label value</span>
                  <input
                    className="rk-mono"
                    value={value}
                    onChange={(e) => {
                      setVal(e.target.value)
                      setValTouched(true)
                    }}
                    onFocus={() => setHow('new')}
                    aria-invalid={how === 'new' && valBad}
                    data-testid="fleet-nodes-value"
                  />
                </span>
              </label>
            </fieldset>
          </>
        )}

        {mode !== 'new' && (
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
            Fleet
            <Select label="Fleet" value={fleetId} options={choices.length ? choices.map(fleetOption) : [['', mode === 'add' ? 'No fleet to add them to' : 'They are in no fleet']]} onChange={setFleetId} disabled={!choices.length} width="100%" testId="fleet-nodes-fleet" />
          </label>
        )}
        {mode !== 'new' && fleet && (
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }} data-testid="fleet-nodes-how">
            {list
              ? `${fleet.name} is a list kept in 221B: ${mode === 'add' ? `${nWord(outOf.length, 'node goes', 'nodes go')} on it` : `${nWord(inIt.length, 'node comes', 'nodes come')} off it`}, and nothing changes in the cluster.`
              : `${fleet.name} takes the nodes that have ${reqText(fleet)}. ${mode === 'add' ? 'To bring nodes in, these go on them.' : 'To take nodes out, these come off them.'}`}
          </p>
        )}

        {commands.length > 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{commands.length === 1 ? 'The command' : 'The commands'}</span>
            <pre className="rk-mono" data-testid="fleet-nodes-commands" style={{ margin: 0, padding: '10px 12px', fontSize: 12, background: 'var(--surface2)', border: '1px solid var(--line)', borderRadius: 8, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
              {commands.join('\n')}
            </pre>
          </div>
        )}
        {mode === 'new' && how === 'own' && !problem && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>No node changes: the fleet is saved in this console and takes these nodes at once.</p>}
        {notes.length > 0 && (
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, color: 'var(--fg2)' }} data-testid="fleet-nodes-notes">
            {notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        )}
        {commands.length > 0 && (
          <p className="rk-fhint" style={{ margin: 0 }}>
            {runs
              ? 'Watson makes a card for each command, with what it changes and a dry run. Nothing changes until someone approves it. Fleets of labels and taints follow the nodes’ own, so the change shows here once it runs.'
              : watson.available
                ? `Watson changes nothing on this cluster${watson.how ? `: ${watson.how}` : ''}. Run the commands yourself; the button copies them.`
                : `${watson.off?.text ?? 'Watson is not on for this cluster.'} The button copies the commands to run yourself.`}
          </p>
        )}
        {problem && (
          <span className="rk-t" data-tone="warn" style={{ fontSize: 12.5 }} data-testid="fleet-nodes-problem">
            {problem}
          </span>
        )}
        {error && (
          <span className="rk-t" data-tone="err" style={{ fontSize: 12.5 }}>
            {error}
          </span>
        )}
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'flex-end', gap: 8 }}>
          {commands.length > 0 && runs && (
            <button className="rk-btn" onClick={() => void copy()} title="Copy the commands to run them yourself">
              <CopyIcon />
              {copied ? 'Copied' : 'Copy'}
            </button>
          )}
          <span style={{ flex: '1 1 auto' }} />
          <button className="rk-btn" onClick={onClose}>
            Cancel
          </button>
          <button className="rk-btn" data-on="true" disabled={!!problem || busy} onClick={() => void go()} data-testid="fleet-nodes-go">
            {goLabel}
          </button>
        </div>
      </section>
    </div>
  )
}
