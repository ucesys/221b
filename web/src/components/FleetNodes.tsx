import { useEffect, useRef, useState } from 'react'
import type { Fleet, FleetRow, NodeRow } from '../api/types'
import { copyText } from '../lib/clipboard'
import { has, joinCommands, joinNotes, LABEL_KEY_RE, LABEL_VAL_RE, labelValueOf, leaveCommands, leaveNotes, listText, newId, nextColor, ownLabels, reqText, splitLabel } from '../lib/fleets'
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
  /** The fleet the page shows, for removing from it and as the first choice to add to. */
  current?: Fleet
  onClose: () => void
  /** Saves a new fleet; the page then shows it. */
  onCreate: (f: Fleet) => Promise<void>
  onDone: (message: string) => void
}

/**
 * Picked nodes into a new fleet, into a fleet, or out of the fleet shown
 * (asked after 0.7.1). A fleet is its labels and taints, so a node joins
 * or leaves one only by its own: the dialog says which commands that takes
 * and what else they change, and hands them to Watson, where nothing runs
 * until someone approves each card.
 */
export function FleetNodes({ mode, nodes, all, fleets, rows, current, onClose, onCreate, onDone }: Props) {
  const watson = useWatsonApi()
  const runs = watson.available && !watson.readOnly
  // Adding: the fleet shown, when some picked node is not in it; else the first one they are not all in.
  const open = fleets.filter((f) => f.labels.length + f.taints.length > 0)
  const [fleetId, setFleetId] = useState(() => (current && nodes.some((n) => !has(current, n)) ? current.id : (open.find((f) => nodes.some((n) => !has(f, n)))?.id ?? open[0]?.id ?? '')))
  const own = ownLabels(nodes, all)
  const [name, setName] = useState('')
  const [how, setHow] = useState<'own' | 'new'>(own.length ? 'own' : 'new')
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

  const value = valTouched ? val : labelValueOf(name)
  const nameTaken = fleets.some((f) => f.name.toLowerCase() === name.trim().toLowerCase())
  const keyBad = !LABEL_KEY_RE.test(key.trim())
  const valBad = !LABEL_VAL_RE.test(value)
  const label = how === 'own' ? ownPick : `${key.trim()}=${value}`
  const draft: Fleet | null = mode === 'new' ? { id: newId(name.trim(), fleets), name: name.trim(), color: nextColor(fleets), labels: label ? [label] : [], taints: [] } : null
  const fleet = mode === 'remove' ? current : mode === 'add' ? fleets.find((f) => f.id === fleetId) : (draft ?? undefined)

  const inIt = fleet && mode !== 'new' ? nodes.filter((n) => has(fleet, n)) : []
  const commands = !fleet ? [] : mode === 'remove' ? leaveCommands(fleet, inIt) : joinCommands(fleet, nodes)
  const notes = !fleet ? [] : mode === 'remove' ? leaveNotes(fleet, inIt, rows) : joinNotes(fleet, nodes, rows)
  // Nodes outside the selection that already have a new fleet's label join it too.
  const alsoIn = mode === 'new' && how === 'new' && !keyBad && !valBad ? all.filter((n) => !nodes.some((m) => m.name === n.name) && n.labels[key.trim()] === value && key.trim() in n.labels) : []
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
          ? 'No fleet to add to: every fleet needs a label or a taint first.'
          : 'Pick one fleet on the left to remove nodes from it.'
        : mode === 'remove' && !inIt.length
          ? `None of these nodes is in ${fleet.name}.`
          : mode === 'add' && !commands.length
            ? `Every one of these nodes is in ${fleet.name} already.`
            : ''

  const title = mode === 'new' ? `New fleet from ${nWord(nodes.length, 'node', 'nodes')}` : mode === 'add' ? `Add ${nWord(nodes.length, 'node', 'nodes')} to a fleet` : `Remove ${nWord(inIt.length || nodes.length, 'node', 'nodes')} from ${current?.name ?? 'a fleet'}`
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
      if (commands.length && runs) watson.send(commands.join('\n'), { from: 'Fleets', why })
      const n = commands.length
      const sent = n ? (runs ? ` Watson has ${nWord(n, 'command', 'commands')} for approval; ${mode === 'remove' ? 'the nodes leave' : 'the nodes join'} once ${n === 1 ? 'it runs' : 'they run'}.` : ` Run the ${nWord(n, 'command', 'commands')} to ${mode === 'remove' ? 'take the nodes out' : 'bring the nodes in'}.`) : ''
      if (n && !runs) await copyText(commands.join('\n'))
      onDone(mode === 'new' ? `Created the fleet “${draft?.name}”.${sent}${n && !runs ? ' The commands are copied.' : ''}` : `${sent.trim()}${n && !runs ? ' The commands are copied.' : ''}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  const goLabel = mode === 'new' ? (commands.length && runs ? 'Create and send to Watson' : commands.length ? 'Create and copy the commands' : 'Create fleet') : runs ? 'Send to Watson' : 'Copy the commands'

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
            <fieldset style={{ border: 0, margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <legend style={{ fontSize: 12, color: 'var(--fg3)', marginBottom: 6 }}>The fleet takes the nodes that have</legend>
              {own.length > 0 && (
                <label style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
                  <input type="radio" name="fleet-how" checked={how === 'own'} onChange={() => setHow('own')} data-testid="fleet-nodes-own" />
                  A label only these nodes have, so no node changes:
                  {own.length === 1 ? (
                    <span className="rk-mono">{labelText(...splitLabel(own[0]!))}</span>
                  ) : (
                    <Select label="Label" value={ownPick} options={own.map((l): [string, string] => [l, l])} onChange={(v) => setOwnPick(v)} disabled={how !== 'own'} testId="fleet-nodes-own-pick" />
                  )}
                </label>
              )}
              <label style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
                <input type="radio" name="fleet-how" checked={how === 'new'} onChange={() => setHow('new')} data-testid="fleet-nodes-newlabel" />
                A new label on them
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

        {mode === 'add' && (
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
            Fleet
            <Select label="Fleet" value={fleetId} options={open.length ? open.map((f): [string, string] => [f.id, `${f.name} — ${reqText(f)}`]) : [['', 'No fleet with labels or taints']]} onChange={setFleetId} disabled={!open.length} width="100%" testId="fleet-nodes-fleet" />
          </label>
        )}
        {mode === 'remove' && fleet && (
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
            {fleet.name} takes the nodes that have {reqText(fleet)}. To take nodes out, these come off them.
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
        {mode === 'new' && !problem && commands.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>No node changes: the fleet is saved in this console and takes these nodes at once.</p>}
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
              ? 'Watson makes a card for each command, with what it changes and a dry run. Nothing changes until someone approves it. Fleets follow the nodes’ labels and taints, so the change shows here once it runs.'
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
