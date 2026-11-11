import { useState } from 'react'
import { ALL_NODES, DEFAULT_REBOOT_MINUTES, MAX_REBOOT_MINUTES, fetchFleets, saveFleetVariables, type AutomationFleets, type VariableChange } from '../../api/automation'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** What a node gets from its fleets: the longest any of them sets, or 0. */
export function fleetRebootMinutes(fleets: AutomationFleets, node: string): number {
  let out = 0
  for (const f of fleets.fleets) {
    const member = f.id === ALL_NODES || f.nodes.some((n) => n.name === node)
    if (member) out = Math.max(out, f.params?.rebootMinutes ?? 0)
  }
  return out
}

/** The reboot timeout a node runs with: its own, then its fleets' longest. */
export function nodeRebootMinutes(fleets: AutomationFleets, node: string): number {
  let own = 0
  for (const f of fleets.fleets) own = Math.max(own, f.params?.nodeReboot?.[node] ?? 0)
  return own || fleetRebootMinutes(fleets, node)
}

/** A typed timeout: a whole number of minutes in range, or blank for none. */
export function parseMinutes(text: string): number | null | 'bad' {
  const t = text.trim()
  if (t === '') return null
  const n = Number(t)
  return Number.isInteger(n) && n >= 1 && n <= MAX_REBOOT_MINUTES ? n : 'bad'
}

type Props = {
  cluster: string
  fleets: AutomationFleets
  fleetId: string
  node: string | null
  onSaved: (change: VariableChange | null, msg: string) => Promise<void> | void
}

/** What is saved for the fleet or the node, as the box shows it. */
function savedText({ fleets, fleetId, node }: Props): string {
  const own = node ? fleets.fleets.find((f) => f.id === ALL_NODES)?.params?.nodeReboot?.[node] : fleets.fleets.find((f) => f.id === fleetId)?.params?.rebootMinutes
  return own ? String(own) : ''
}

/**
 * How long a node has to come back after a script reboots it, for a fleet
 * or for one node, which wins over its fleets. Another fleet, node or
 * saved time starts the box again.
 */
export function RebootTimeout(props: Props) {
  return <RebootForm key={`${props.fleetId}|${props.node ?? ''}|${savedText(props)}`} {...props} />
}

function RebootForm(props: Props) {
  const { cluster, fleets, fleetId, node, onSaved } = props
  const fleet = fleets.fleets.find((f) => f.id === fleetId)
  const shown = savedText(props)
  const [text, setText] = useState(shown)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  if (!fleet) return null
  const parsed = parseMinutes(text)
  const changed = text.trim() !== shown
  const inherited = node ? fleetRebootMinutes(fleets, node) : 0
  const save = async () => {
    if (parsed === 'bad') return
    setBusy(true)
    setErr('')
    try {
      const now = await fetchFleets(cluster)
      const target = node ? ALL_NODES : fleet.id
      const p = now.fleets.find((f) => f.id === target)?.params ?? {}
      let next = { ...p }
      if (node) {
        const nodeReboot = { ...(p.nodeReboot ?? {}) }
        if (parsed === null) delete nodeReboot[node]
        else nodeReboot[node] = parsed
        next = { ...p, nodeReboot }
      } else {
        next = { ...p, rebootMinutes: parsed ?? undefined }
      }
      const { change } = await saveFleetVariables(cluster, target, next)
      const place = node ?? fleet.name
      await onSaved(change ?? null, change?.state === 'waiting' ? `The reboot timeout of ${place} waits for approval.` : `The reboot timeout of ${place} is saved.`)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rk-card" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 10 }} aria-label="Reboot timeout" data-testid="params-reboot">
      <h3 className="rk-h" style={{ margin: 0 }}>
        Reboot timeout
      </h3>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
        <span className="rk-field" style={{ width: 110 }}>
          <input inputMode="numeric" value={text} placeholder={String(inherited || DEFAULT_REBOOT_MINUTES)} aria-label="Reboot timeout in minutes" onChange={(e) => setText(e.target.value)} data-testid="params-reboot-minutes" />
        </span>
        <span style={{ fontSize: 12.5 }}>minutes</span>
        {changed && (
          <button className="rk-btn" data-on disabled={busy || parsed === 'bad'} onClick={() => void save()} data-testid="params-reboot-save">
            {busy ? 'Saving…' : 'Save timeout'}
          </button>
        )}
      </div>
      <p className="tb-hint" style={{ margin: 0 }}>
        {node
          ? `How long ${node} has to come back after a script reboots it. Its own time wins over its fleets'. Left empty: ${inherited ? `${inherited} minutes, the longest of its fleets` : `${DEFAULT_REBOOT_MINUTES} minutes, the default`}.`
          : `How long this fleet's nodes have to come back after a script reboots them. Left empty: ${DEFAULT_REBOOT_MINUTES} minutes. A node in several fleets gets the longest; a node's own time wins over all of them.`}
      </p>
      {parsed === 'bad' && (
        <p className="tb-err" style={{ margin: 0 }}>
          A whole number of minutes, 1 to {MAX_REBOOT_MINUTES}.
        </p>
      )}
      {err && <p className="tb-err">{err}</p>}
    </section>
  )
}
