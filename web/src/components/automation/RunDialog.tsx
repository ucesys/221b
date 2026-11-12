// The Run dialog: which version, which nodes, how, and with what values.
import { useEffect, useState } from 'react'
import {
  allows,
  runProfilesFor,
  approvedOf,
  batches,
  fetchFleets,
  fetchScript,
  ceilingText,
  privilegeText,
  missingParams,
  paramClashes,
  ALL_NODES,
  CUSTOM_NODES,
  fleetOf,
  mayRun,
  pairedNames,
  rolloutHelp,
  resolveParams,
  fetchProfiles,
  type RunProfile,
  startRun,
  type AutomationFleets,
  type Param,
  type Run,
  type RolloutMode,
  type Script,
  type ScriptSummary,
} from '../../api/automation'
import { Seg } from './Seg'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'
import { ScrollBox } from '../Table'
import { ROLLOUTS, errText, Field } from './parts'

// ---------- running ----------

export function RunDialog({
  cluster,
  script,
  dryRun: dry0,
  scripts,
  off,
  maxTimeout,
  onClose,
  onStarted,
}: {
  cluster: string
  script: string
  dryRun: boolean
  scripts: ScriptSummary[]
  off: string
  maxTimeout: number
  onClose: () => void
  onStarted: (r: Run) => void
}) {
  const [sc, setSc] = useState<Script | null>(null)
  const [fleets, setFleets] = useState<AutomationFleets | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [wantDry, setWantDry] = useState(dry0)
  const [versionN, setVersionN] = useState<number | null>(null)
  const [profiles, setProfiles] = useState<RunProfile[]>([])
  const [profileId, setProfileId] = useState('')
  const [drain, setDrain] = useState(false)
  const [hookBefore, setHookBefore] = useState('')
  const [hookAfter, setHookAfter] = useState('')
  const [fleetId, setFleetId] = useState('') // CUSTOM_NODES: picked by hand
  const [listed, setListed] = useState('') // the fleet whose nodes the dialog lists
  const [picked, setPicked] = useState<string[]>([])
  const [overrides, setOverrides] = useState<Record<string, string>>({})
  const [mode, setMode] = useState<RolloutMode>('canary')
  const [size, setSize] = useState(5)
  const [stopAfter, setStopAfter] = useState(1)
  const [timeout, setTimeout_] = useState(10)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    let gone = false
    Promise.all([fetchScript(script), fetchFleets(cluster), fetchProfiles()])
      .then(([s, f, c]) => {
        if (gone) return
        setProfiles(c.profiles)
        setSc(s)
        setFleets(f)
        setFleetId(f.fleets[0]?.id ?? '')
        setListed(f.fleets[0]?.id ?? '')
        setPicked(pairedNames(f.fleets[0]))
      })
      .catch((e: unknown) => !gone && setLoadErr(errText(e)))
    return () => {
      gone = true
    }
  }, [script, cluster])

  const approvedVersion = sc ? approvedOf(sc) : undefined
  const newestFirst = [...(sc?.versions ?? [])].reverse()
  const canDry = newestFirst.some((v) => v.header.dryRun)
  const dryRun = wantDry && canDry
  // Off production a run takes the version chosen, approved or not, and
  // starts from the newest; a rejected version never runs. On production
  // only approved versions run, starting from the newest approved.
  const anyVersion = fleets?.production === false
  const offered = dryRun ? newestFirst.filter((v) => v.may?.dry) : newestFirst.filter((v) => mayRun(v, !anyVersion) || (!anyVersion && v.state === 'approved'))
  const version = offered.find((v) => v.n === versionN) ?? (dryRun || anyVersion ? offered[0] : (approvedVersion ?? offered[0]))
  const onCluster = version?.header.target === 'kubernetes'
  const hookScripts = scripts.filter((x) => x.target === 'kubernetes' && x.approved)
  const everyNode = fleets?.fleets.find((f) => f.id === ALL_NODES)
  const custom = fleetId === CUSTOM_NODES
  // Picked by hand, the run is no fleet's: it takes All nodes' values and ceiling.
  const fleet = custom ? everyNode : fleets?.fleets.find((f) => f.id === fleetId)
  const nodes = pairedNames(everyNode).filter((n) => picked.includes(n))
  const leftOut = custom ? 0 : (fleet?.nodes ?? []).filter((n) => !n.paired).length
  const listedFleet = fleets?.fleets.find((f) => f.id === listed)
  const pickFleet = (id: string) => {
    setFleetId(id)
    setListed(id)
    setPicked(pairedNames(fleets?.fleets.find((f) => f.id === id)))
  }
  const toggleNode = (name: string, on: boolean) => {
    const next = on ? [...picked, name] : picked.filter((x) => x !== name)
    setPicked(next)
    // The list stays as it is under the pointer, and its own fleet is
    // named first when another fleet reaches the same nodes.
    setFleetId(fleetOf([...(listedFleet ? [listedFleet] : []), ...(fleets?.fleets ?? [])], next))
  }
  const params = version?.header.params ?? []
  // On production a real run takes its values from the fleets alone: a
  // value of its own would skip the approval a change needs there.
  const fleetValuesOnly = fleets?.production !== false && !dryRun
  const runValues = fleetValuesOnly ? {} : Object.fromEntries(Object.entries(overrides).filter(([, v]) => v !== ''))
  // One name per opening of the dialog: a second click, or a retry of a
  // request that did reach the server, starts no second run.
  const [request] = useState(() => `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`)
  const missing = missingParams(params, fleets?.fleets ?? [], onCluster ? [''] : nodes, runValues)
  const clashes = onCluster ? [] : paramClashes(params, fleets?.fleets ?? [], nodes, runValues)
  const plan = batches(nodes, { mode, size })
  const runAs = runProfilesFor(profiles, {
    onCluster,
    dryRun,
    privilege: version?.header.privilege ?? '',
    fleet: fleet?.id,
    fp: fleet?.params,
  })
  const profile = runAs.options.find((c) => c.id === profileId) ?? runAs.fit
  const blocked: string[] = []
  if (off && !onCluster) blocked.push(off)
  if (fleets?.readOnly) blocked.push(fleets.readOnly)
  if (!version) blocked.push(dryRun ? 'The script has no version.' : 'No version is approved: submit one and have it approved, or run a dry run.')
  else {
    if ((version.findings ?? []).some((f) => f.level === 'error')) blocked.push(`v${version.n} has errors to fix first.`)
    if (!dryRun && !anyVersion && !mayRun(version, true)) blocked.push(`v${version.n} was approved only by the person who wrote it: someone else must approve it.`)
    if (!dryRun && !onCluster && !allows(fleet?.params, version.header.privilege)) blocked.push(`The script needs privilege elevated; the ceiling of fleet ${fleet?.name} is ${ceilingText('none')}: read-only. Raise it under Variables.`)
  }
  if (version && profiles.length && !profile) blocked.push('No run profile fits this script on this fleet.')
  if (nodes.length === 0 && !onCluster) blocked.push('No paired node is picked.')
  if (clashes.length) blocked.push(`Defined on more than one fleet: ${clashes.slice(0, 4).join('; ')}${clashes.length > 4 ? '…' : ''}. Keep it on one fleet, or set it on the node or below.`)
  if (missing.length) blocked.push(`Required parameters have no value: ${missing.slice(0, 4).join('; ')}${missing.length > 4 ? '…' : ''}`)
  if (mode === 'size' && size < 1) blocked.push('A batch has at least one node.')

  // A real run asks first; a dry run changes nothing.
  const [confirming, setConfirming] = useState(false)
  const start = async () => {
    if (!fleet || !version) return
    setBusy(true)
    setErr('')
    try {
      const r = await startRun(cluster, {
        script,
        version: version.n,
        dryRun,
        fleet: onCluster ? '_cluster' : fleet.id,
        nodes: onCluster ? undefined : nodes,
        config: profile?.id,
        hooks: onCluster
          ? undefined
          : {
              before: [...(drain ? ['drain'] : []), ...(hookBefore ? [`script:${hookBefore}`] : [])],
              after: [...(hookAfter ? [`script:${hookAfter}`] : []), ...(drain ? ['uncordon'] : [])],
            },
        params: Object.keys(runValues).length ? runValues : undefined,
        request: `${request}-${dryRun ? 'dry' : 'run'}`,
        rollout: {
          mode,
          size: mode === 'all' ? undefined : size,
          stopAfter,
          timeoutMinutes: timeout,
        },
      })
      onStarted(r)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const first = nodes[0]
  const resolved = resolveParams(params, fleets?.fleets ?? [], first ?? '', runValues)

  return (
    <>
      <Drawer
        label="Run a script"
        kicker={dryRun ? 'Dry run' : 'Run'}
        title={script}
        copy={false}
        width={1400}
        onClose={onClose}
        testId="run-dialog"
        facts={
          version ? (
            <span style={{ fontSize: 12.5 }}>
              v{version.n} · privilege <span className="rk-mono">{dryRun ? 'nobody (dry run)' : privilegeText(version.header.privilege)}</span> · sha256 <span className="rk-mono">{version.hash.slice(0, 12)}</span>
            </span>
          ) : undefined
        }
        footer={
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {err && (
              <p className="tb-err" style={{ margin: 0 }} data-testid="run-error">
                {err}
              </p>
            )}
            {blocked.length > 0 && (
              <ul className="tb-hint" style={{ margin: 0, paddingLeft: 18 }} data-testid="run-blocked">
                {blocked.map((b) => (
                  <li key={b}>{b}</li>
                ))}
              </ul>
            )}
            <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
                {nodes.length} {nodes.length === 1 ? 'node' : 'nodes'} in {plan.length} {plan.length === 1 ? 'batch' : 'batches'}
              </span>
              <button className="rk-btn" style={{ marginLeft: 'auto' }} onClick={onClose}>
                Cancel
              </button>
              <button className="rk-btn" data-on onClick={() => (dryRun ? void start() : setConfirming(true))} disabled={busy || blocked.length > 0} data-testid="run-start">
                {busy ? 'Starting…' : dryRun ? 'Start dry run' : `Run v${version?.n ?? ''}`}
              </button>
            </div>
          </div>
        }
      >
        {loadErr && <p className="tb-err">{loadErr}</p>}
        {sc && fleets && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <div
              style={{
                display: 'flex',
                gap: 16,
                flexWrap: 'wrap',
                alignItems: 'flex-end',
              }}
            >
              <Field label="Kind" hint={canDry ? undefined : 'This script does not declare dry-run: yes, so it has no dry run.'}>
                <Seg label="Kind of run" value={dryRun ? 'dry' : 'run'} onChange={(v) => setWantDry(v === 'dry')} options={[...(canDry ? [{ id: 'dry' as const, label: 'Dry run' }] : []), { id: 'run' as const, label: 'Run' }]} />
              </Field>
              <Field label="Version">
                <span className="rk-field" style={{ width: 170 }}>
                  <select value={version?.n ?? ''} disabled={offered.length < 2} onChange={(e) => setVersionN(Number(e.target.value))} data-testid="run-version">
                    {offered.length === 0 && <option value="">none</option>}
                    {offered.map((v) => (
                      <option key={v.n} value={v.n}>
                        v{v.n} ({v.state})
                      </option>
                    ))}
                  </select>
                </span>
              </Field>
              <Field label="Run as">
                <span className="rk-field" style={{ width: 200 }}>
                  <select value={profile?.id ?? ''} onChange={(e) => setProfileId(e.target.value)} disabled={runAs.options.length < 2} data-testid="run-config">
                    {!profile && <option value="">none fits</option>}
                    {runAs.options.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                </span>
              </Field>
              {!onCluster && (
                <Field label="Fleet">
                  <span className="rk-field" style={{ width: 240 }}>
                    <select value={fleetId} onChange={(e) => pickFleet(e.target.value)} data-testid="run-fleet">
                      {custom && <option value={CUSTOM_NODES}>Custom selection ({nodes.length})</option>}
                      {fleets.fleets.map((f) => (
                        <option key={f.id} value={f.id}>
                          {f.name} ({f.nodes.length})
                        </option>
                      ))}
                    </select>
                  </span>
                </Field>
              )}
            </div>
            {onCluster && (
              <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="run-on-cluster">
                This script acts on the cluster, not on nodes. Watson runs its kubectl lines
                {version?.header.runsAs ? ` as ${version.header.runsAs}` : ' as you'}, and only the actions it declares: {(version?.header.k8s ?? []).map((a) => `${a.verb} ${a.resource}`).join(', ')}.
              </p>
            )}
            {fleets.production && !dryRun && (
              <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="run-production">
                This is a production cluster: only an approved version runs.
              </p>
            )}
            {anyVersion && !dryRun && version?.state !== 'approved' && version && (
              <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="run-unapproved">
                v{version.n} is {version.state}, not approved. This cluster is not production, so it can run anyway.
              </p>
            )}
            {!onCluster && (
              <div>
                <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                  Nodes{' '}
                  <span style={{ fontWeight: 400, color: 'var(--fg3)' }} data-testid="run-node-count">
                    {nodes.length} of {listedFleet?.nodes.length ?? 0} selected
                  </span>
                </h3>
                <ScrollBox label="Nodes" style={{ maxHeight: 190, overflowY: 'auto' }}>
                  <div
                    style={{
                      display: 'grid',
                      gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
                      gap: 4,
                      fontSize: 12.5,
                    }}
                    data-testid="run-nodes"
                  >
                    {(listedFleet?.nodes ?? []).map((n) => (
                      <label
                        key={n.name}
                        title={n.why}
                        style={{
                          display: 'flex',
                          gap: 6,
                          alignItems: 'center',
                          color: n.paired ? 'var(--fg)' : 'var(--fg3)',
                        }}
                      >
                        <input type="checkbox" disabled={!n.paired} checked={n.paired && nodes.includes(n.name)} onChange={(e) => toggleNode(n.name, e.target.checked)} />
                        <span className="rk-mono">{n.name}</span>
                        {!n.paired && <span style={{ fontSize: 11.5 }}>not paired</span>}
                      </label>
                    ))}
                  </div>
                </ScrollBox>
                {custom && (
                  <p className="tb-hint" style={{ margin: '6px 0 0' }} data-testid="run-custom">
                    Picked by hand, this run is not configured for a fleet: it takes the values and the privilege ceiling of All nodes. Choose a fleet to go back to its nodes.
                  </p>
                )}
                {leftOut > 0 && (
                  <p className="tb-hint" style={{ margin: '6px 0 0' }} data-testid="run-left-out">
                    {leftOut} of this fleet’s nodes {leftOut === 1 ? 'is' : 'are'} not paired for SSH and will be left out.
                  </p>
                )}
              </div>
            )}
            {!onCluster && (
              <div data-testid="run-hooks">
                <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                  Around each node
                </h3>
                <label style={{ display: 'flex', gap: 6, fontSize: 12.5 }}>
                  <input type="checkbox" checked={drain} onChange={(e) => setDrain(e.target.checked)} data-testid="run-hook-drain" />
                  Cordon and drain each node first, and uncordon it after a healthy result. A node that fails stays cordoned.
                </label>
                {hookScripts.length > 0 && (
                  <div
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      gap: 12,
                      marginTop: 6,
                    }}
                  >
                    <Field label="Before each node">
                      <span className="rk-field" style={{ width: 230 }}>
                        <select value={hookBefore} onChange={(e) => setHookBefore(e.target.value)} data-testid="run-hook-before">
                          <option value="">Nothing</option>
                          {hookScripts.map((x) => (
                            <option key={x.name} value={x.name}>
                              {x.name} v{x.approved}
                            </option>
                          ))}
                        </select>
                      </span>
                    </Field>
                    <Field label="After each node">
                      <span className="rk-field" style={{ width: 230 }}>
                        <select value={hookAfter} onChange={(e) => setHookAfter(e.target.value)} data-testid="run-hook-after">
                          <option value="">Nothing</option>
                          {hookScripts.map((x) => (
                            <option key={x.name} value={x.name}>
                              {x.name} v{x.approved}
                            </option>
                          ))}
                        </select>
                      </span>
                    </Field>
                  </div>
                )}
              </div>
            )}
            {params.length > 0 && (
              <div>
                <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                  Parameters
                </h3>
                <p className="tb-hint" style={{ margin: '0 0 6px' }} data-testid="run-params-hint">
                  {fleetValuesOnly
                    ? `On production a run takes each node’s values from its fleets; change them on the Variables page, where a change that matters waits for approval${first ? `. Shown for ${first}` : ''}.`
                    : `A value here is this run’s and wins over the node’s and the fleet’s. Left empty, each node takes its own value, then the fleet’s${first ? `; shown for ${first}` : ''}.`}
                </p>
                <table className="tb-table" data-testid="run-params">
                  <tbody>
                    {resolved.map((p) => (
                      <tr key={p.name}>
                        <td className="rk-mono" style={{ whiteSpace: 'nowrap' }}>
                          {p.name}
                          {p.required && <span style={{ color: 'var(--err)' }}> *</span>}
                          {p.desc && (
                            <div
                              style={{
                                color: 'var(--fg3)',
                                fontSize: 12,
                                fontFamily: 'inherit',
                              }}
                            >
                              {p.desc}
                            </div>
                          )}
                        </td>
                        <td style={{ width: '50%' }}>
                          <span className="rk-field">
                            <input
                              value={fleetValuesOnly ? '' : (overrides[p.name] ?? '')}
                              disabled={fleetValuesOnly}
                              onChange={(e) =>
                                setOverrides({
                                  ...overrides,
                                  [p.name]: e.target.value,
                                })
                              }
                              placeholder={secretOf(params, p.name) && !p.value ? 'secret:namespace/name/key' : p.source && p.source !== 'run' ? p.value : p.required ? 'required' : 'empty'}
                              aria-label={p.name}
                              data-testid={`run-param-${p.name}`}
                            />
                          </span>
                        </td>
                        <td style={{ color: 'var(--fg3)', whiteSpace: 'nowrap' }}>
                          {p.conflict ? (
                            <span style={{ color: 'var(--err)' }} data-testid="run-param-conflict">
                              on {p.conflict.join(' and ')}
                            </span>
                          ) : p.from ? (
                            `from ${p.from}`
                          ) : p.source ? (
                            `from the ${p.source}`
                          ) : p.required ? (
                            'no value'
                          ) : (
                            ''
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div>
              <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                Rollout
              </h3>
              <div
                style={{
                  display: 'flex',
                  flexWrap: 'wrap',
                  gap: '12px 32px',
                  alignItems: 'flex-start',
                }}
              >
                <fieldset
                  style={{
                    flex: '1 1 340px',
                    minWidth: 0,
                    border: 0,
                    padding: 0,
                    margin: 0,
                  }}
                  data-testid="run-rollout"
                >
                  <legend className="rk-sr">Rollout</legend>
                  {ROLLOUTS.map(([id, label]) => (
                    <label key={id} className="tb-radio">
                      <input type="radio" name="run-rollout" checked={mode === id} onChange={() => setMode(id)} data-testid={`run-rollout-${id}`} />
                      <span>
                        <b style={{ fontWeight: 500 }}>{label}</b>
                        <small>{rolloutHelp(id, size)}</small>
                      </span>
                    </label>
                  ))}
                </fieldset>
                <div
                  style={{
                    flex: '0 0 auto',
                    display: 'grid',
                    gridTemplateColumns: '190px 90px',
                    gap: '8px 12px',
                    alignItems: 'center',
                    fontSize: 12.5,
                  }}
                  data-testid="run-rollout-inputs"
                >
                  {mode !== 'all' && (
                    <RolloutInput
                      label={mode === 'canary' ? 'Then batches of' : 'Batch size'}
                      hint={mode === 'canary' ? '0 runs the rest together' : undefined}
                      min={mode === 'canary' ? 0 : 1}
                      value={size}
                      onChange={setSize}
                      testId="run-size"
                    />
                  )}
                  <RolloutInput label="Stop after failures" hint="Failed or unreachable nodes; 0 sets no limit" min={0} value={stopAfter} onChange={setStopAfter} testId="run-stop-after" />
                  <RolloutInput label="Timeout per node (min)" min={1} max={maxTimeout} value={timeout} onChange={setTimeout_} testId="run-timeout" />
                </div>
              </div>
              {plan.length > 1 && (
                <p className="tb-hint" style={{ margin: '4px 0 0' }} data-testid="run-plan">
                  {plan.map((b, i) => `${i + 1}: ${b.length === 1 ? b[0] : `${b.length} nodes`}`).join(' → ')}.
                </p>
              )}
            </div>
          </div>
        )}
      </Drawer>
      {confirming && version && (
        <ConfirmDialog
          title={`Run ${script} v${version.n}?`}
          confirm={`Run v${version.n}`}
          busy={busy}
          error={err}
          onConfirm={() => {
            setConfirming(false)
            void start()
          }}
          onCancel={() => setConfirming(false)}
        >
          <p style={{ margin: 0 }}>
            {onCluster ? 'It runs on the cluster' : `It runs on ${nodes.length} ${nodes.length === 1 ? 'node' : 'nodes'} of ${fleet?.name ?? 'the fleet'}, ${plan.length} ${plan.length === 1 ? 'batch' : 'batches'}`}
            {profile ? `, as ${profile.name}` : ''}. This is not a dry run: it changes what the script changes.
            {version.state === 'approved' ? '' : ' This version is not approved, so it runs only off production.'}
          </p>
        </ConfirmDialog>
      )}
    </>
  )
}

export const secretOf = (params: Param[], name: string) => params.some((p) => p.name === name && p.secret)

/** One of the numbers beside the rollout, its label left of its box. */
export function RolloutInput({ label, hint, min, max, value, onChange, testId }: { label: string; hint?: string; min: number; max?: number; value: number; onChange: (v: number) => void; testId: string }) {
  return (
    <label style={{ display: 'contents' }}>
      <span>
        {label}
        {hint && <small style={{ display: 'block', color: 'var(--fg3)' }}>{hint}</small>}
      </span>
      <span className="rk-field">
        <input type="number" min={min} max={max} value={value} onChange={(e) => onChange(Math.min(max ?? Infinity, Math.max(min, Number(e.target.value) || min)))} data-testid={testId} />
      </span>
    </label>
  )
}
