// Mycroft › Exceptions › New exception (build 14): an exception made by
// hand, not from a case. The approvers pick a connected cluster by its
// name, name the object (or leave it empty for the cluster as a whole),
// pick a validation rule seen on that cluster or type one (an id, or a
// finding's title in words, which the server makes plain as it does for a
// finding), give the justification (required) and, if it should end, its
// last day. The server writes the object one way, so it matches the
// findings, refuses one an active exception already covers, and audits it
// as exception.add. The object is suggested as it is typed (ObjectField);
// anything else may still be typed.
import { useEffect, useId, useState } from 'react'
import { createException, fetchCaseObjects, type AcceptedException, type CaseObject, type ExceptionCluster } from '../../api/lessons'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { errText } from './parts'

/** Today in the browser, as a date input reads it. */
function today(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

/** How long typing pauses before the suggestions are asked for. */
const SUGGEST_DELAY = 200

/**
 * The Object field: from two characters on, the objects of the chosen
 * cluster whose name or kind holds what was typed are suggested under it,
 * those the cases and exceptions name first (a combobox: arrows move,
 * Enter picks, Escape closes). Picking fills the field; anything else may
 * be typed, a custom resource or an object the console does not keep.
 */
function ObjectField({ cluster, value, onChange }: { cluster: string; value: string; onChange: (v: string) => void }) {
  const [found, setFound] = useState<{ key: string; objects: CaseObject[] }>({ key: '', objects: [] })
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  // The object last picked: not asked about again until it is changed.
  const [picked, setPicked] = useState('')
  const labelId = useId()
  const listId = useId()
  const q = value.trim()
  const key = cluster && [...q].length >= 2 && q !== picked ? `${cluster}\n${q}` : ''
  useEffect(() => {
    if (!key) return
    let current = true
    const t = setTimeout(() => {
      fetchCaseObjects(cluster, q).then(
        (r) => {
          if (!current) return
          setFound({ key, objects: r.objects })
          setActive(-1)
        },
        // No suggestions then: the object can still be typed.
        () => current && setFound({ key, objects: [] }),
      )
    }, SUGGEST_DELAY)
    return () => {
      current = false
      clearTimeout(t)
    }
  }, [key, cluster, q])
  const items = key && found.key === key ? found.objects : []
  const shown = open && items.length > 0
  const pick = (o: CaseObject) => {
    setPicked(o.object)
    onChange(o.object)
    setOpen(false)
    setActive(-1)
  }
  return (
    <span className="cs-lbl">
      <span id={labelId}>Object (optional)</span>
      <span className="rk-dd" style={{ display: 'block' }}>
        <input
          className="cs-in rk-mono"
          style={{ width: '100%', boxSizing: 'border-box' }}
          role="combobox"
          aria-labelledby={labelId}
          aria-expanded={shown}
          aria-controls={shown ? listId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={shown && active >= 0 ? `${listId}-${active}` : undefined}
          autoComplete="off"
          spellCheck={false}
          value={value}
          maxLength={300}
          onChange={(e) => {
            onChange(e.target.value)
            setOpen(true)
            setActive(-1)
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' && items.length) {
              e.preventDefault()
              setOpen(true)
              setActive((a) => (shown ? (a + 1) % items.length : 0))
            } else if (e.key === 'ArrowUp' && items.length) {
              e.preventDefault()
              setOpen(true)
              setActive((a) => (shown && a > 0 ? a - 1 : items.length - 1))
            } else if (e.key === 'Enter' && shown && active >= 0) {
              e.preventDefault()
              const o = items[active]
              if (o) pick(o)
            } else if (e.key === 'Escape' && shown) {
              // Closes the suggestions, not the drawer.
              e.preventDefault()
              e.stopPropagation()
              setOpen(false)
            }
          }}
          placeholder="shop/Deployment/web, Node/worker-3"
          data-testid="exception-new-object"
        />
        {shown && (
          <span
            id={listId}
            role="listbox"
            aria-label="Objects"
            className="rk-ddmenu"
            data-testid="exception-new-objects"
            style={{ right: 0, maxWidth: 'none', maxHeight: 260, padding: 4 }}
            // Mouse down on an option must not blur the field before the click.
            onMouseDown={(e) => e.preventDefault()}
          >
            {items.map((o, i) => (
              <span
                key={o.object}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                className="rk-ddopt"
                data-active={i === active}
                data-testid="exception-new-object-option"
                onClick={() => pick(o)}
              >
                <span className="rk-mono" style={{ flex: '1 1 auto', overflow: 'hidden', textOverflow: 'ellipsis', fontSize: 12 }}>
                  {o.object}
                </span>
                <span style={{ fontSize: 11, color: 'var(--fg3)', whiteSpace: 'nowrap' }}>
                  {o.kind}
                  {o.from === 'cases' ? ' · in cases' : ''}
                </span>
              </span>
            ))}
          </span>
        )}
      </span>
      <span className="tb-hint">namespace/Kind/name or Kind/name, suggested as you type. Empty: the cluster as a whole.</span>
    </span>
  )
}

export function NewExceptionDrawer({ clusters, onClose, onCreated }: { clusters: ExceptionCluster[]; onClose: () => void; onCreated: (e: AcceptedException) => void }) {
  const [cluster, setCluster] = useState(clusters.length === 1 ? clusters[0]!.id : '')
  const [object, setObject] = useState('')
  const [rule, setRule] = useState('')
  const [reason, setReason] = useState('')
  const [expires, setExpires] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const listId = useId()
  const rules = clusters.find((c) => c.id === cluster)?.rules ?? []
  const meaning = rules.find((r) => r.id === rule.trim())?.meaning
  const ready = !!cluster && !!rule.trim() && !!reason.trim()
  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      const e = await createException({ cluster, check: rule.trim(), reason: reason.trim(), ...(object.trim() ? { resource: object.trim() } : {}), ...(expires ? { expires } : {}) })
      onCreated(e)
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }
  return (
    <Drawer
      label="New exception"
      kicker="Mycroft › Exceptions"
      title="New exception"
      onClose={onClose}
      width={640}
      copy={false}
      testId="exception-new"
      footer={
        <span style={{ display: 'flex', gap: 8, width: '100%', alignItems: 'center' }}>
          <span className="tb-hint" style={{ fontSize: 12 }}>
            The agents stop reporting this validation rule on this object while it is active.
          </span>
          <span className="cs-grow" />
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="rk-btn" data-on disabled={busy || !ready} onClick={() => void save()} data-testid="exception-new-save">
            Create exception
          </button>
        </span>
      }
    >
      <div className="cs-sec" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <span className="cs-lbl">
          Cluster
          <Select label="Cluster" value={cluster} options={[...(clusters.length === 1 ? [] : [['', 'Pick a cluster'] as [string, string]]), ...clusters.map((c): [string, string] => [c.id, c.name || c.id])]} onChange={setCluster} testId="exception-new-cluster" />
        </span>
        <ObjectField cluster={cluster} value={object} onChange={setObject} />
        <label className="cs-lbl">
          Validation rule
          <input className="cs-in rk-mono" list={listId} value={rule} maxLength={200} onChange={(e) => setRule(e.target.value)} placeholder="pod.root-user" data-testid="exception-new-rule" />
          <datalist id={listId} data-testid="exception-new-rules">
            {rules.map((r) => (
              <option key={r.id} value={r.id}>
                {r.meaning ?? r.id}
              </option>
            ))}
          </datalist>
          <span className="tb-hint" data-testid="exception-new-rule-hint">
            {meaning
              ? `Means: ${meaning}.`
              : !cluster
                ? 'Pick the cluster first: the rules seen on it are offered.'
                : rules.length
                  ? `${rules.length} ${rules.length === 1 ? 'rule' : 'rules'} seen on this cluster are offered; or type one: an id, or a finding’s title.`
                  : 'No validation rules seen on this cluster yet: type one, an id or a finding’s title.'}
          </span>
        </label>
        <label className="cs-lbl">
          Justification (required)
          <textarea className="cs-ta" style={{ minHeight: 64 }} value={reason} maxLength={1000} onChange={(e) => setReason(e.target.value)} placeholder="What makes this intended or acceptable here" data-testid="exception-new-reason" />
        </label>
        <label className="cs-lbl">
          Last day (optional)
          <input className="cs-in" type="date" style={{ width: 180 }} min={today()} value={expires} onChange={(e) => setExpires(e.target.value)} data-testid="exception-new-expires" />
          <span className="tb-hint">Empty: until someone turns it off.</span>
        </label>
        {err && (
          <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="exception-new-error">
            {err}
          </p>
        )}
      </div>
    </Drawer>
  )
}
