import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { createApp, fetchAppRefs, fetchRepoRefs, inspectRepo, isCard, sourceText, updateApp, upgradeApp, type AppInput, type AppSource, type AppView, type RefKind, type RepoInspection, type RepoRefs } from '../../api/apps'
import { YamlEditor } from '../YamlEditor'
import { useLiveMeta } from '../../api/live'
import type { WatsonItem } from '../../api/watson'
import type { AppRow } from '../../columns/apps'
import { trapTab } from '../../lib/focus'
import { layer } from '../../lib/layer'

/** What the dialog does (HA-05, HA-10 to HA-13). */
export type DialogMode = 'create' | 'source' | 'switch' | 'upgrade' | 'install' | 'values'

interface Props {
  cluster: string
  mode: DialogMode
  /** The release, for every mode but create. */
  row?: AppRow
  /** Namespaces the person sees, for the create dialog's list. */
  namespaces?: string[]
  onClose: () => void
  /** What the server answered: the Watson card of a change, or the saved record. */
  onDone: (result: WatsonItem | AppView, mode: DialogMode) => void
}

const TITLES: Record<DialogMode, string> = {
  create: 'New application from a git repository',
  source: 'The source of the release',
  switch: 'Switch to another branch or tag',
  upgrade: 'Redeploy from the source',
  install: 'Install from the source',
  values: 'Values and parameters',
}

/** Picks a branch or a tag from the repository's refs, or takes one typed (HA-05). */
function RefPicker({ refs, value, kind, onChange, id }: { refs: RepoRefs | null; value: string; kind: RefKind; onChange: (ref: string, kind: RefKind) => void; id: string }) {
  const listed = refs && (refs.branches.length > 0 || refs.tags.length > 0)
  const inList = !!refs && (kind === 'tag' ? refs.tags : refs.branches).includes(value)
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
      {listed && (
        <label className="rk-field" style={{ minWidth: 260 }}>
          <select
            value={inList ? `${kind}:${value}` : ''}
            onChange={(e) => {
              const [k, ...rest] = e.target.value.split(':')
              if (k === 'branch' || k === 'tag') onChange(rest.join(':'), k)
            }}
            aria-label="Branch or tag"
            data-testid={`${id}-ref`}
          >
            <option value="">{inList ? '' : 'Choose a branch or tag…'}</option>
            {refs.branches.length > 0 && (
              <optgroup label="Branches">
                {refs.branches.map((b) => (
                  <option key={'b' + b} value={'branch:' + b}>
                    {b}
                    {refs.default === b ? ' (default)' : ''}
                  </option>
                ))}
              </optgroup>
            )}
            {refs.tags.length > 0 && (
              <optgroup label="Tags">
                {refs.tags.map((t) => (
                  <option key={'t' + t} value={'tag:' + t}>
                    {t}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </label>
      )}
      <label className="rk-field" style={{ minWidth: 200 }}>
        <input className="rk-mono" value={value} onChange={(e) => onChange(e.target.value, kind)} placeholder={listed ? 'or type one' : 'main, or v1.2.0'} aria-label="Branch or tag name" data-testid={`${id}-ref-text`} />
      </label>
      <div className="rk-seg" role="group" aria-label="Kind of ref">
        <button type="button" aria-pressed={kind === 'branch'} onClick={() => onChange(value, 'branch')}>
          Branch
        </button>
        <button type="button" aria-pressed={kind === 'tag'} onClick={() => onChange(value, 'tag')}>
          Tag
        </button>
      </div>
    </div>
  )
}

function Field({ label, hint, children, wide }: { label: string; hint?: string; children: ReactNode; wide?: boolean }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, gridColumn: wide ? '1 / -1' : undefined, minWidth: 0 }}>
      <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>{label}</span>
      {children}
      {hint && <span className="rk-fhint">{hint}</span>}
    </div>
  )
}

export function AppDialog({ cluster, mode, row, namespaces = [], onClose, onDone }: Props) {
  const app = row?.app
  const [namespace, setNamespace] = useState(row?.namespace ?? '')
  const [name, setName] = useState(row?.name ?? '')
  const [repo, setRepo] = useState(app?.source.repo ?? '')
  const [path, setPath] = useState(app?.source.path ?? '')
  const [ref, setRef] = useState(app?.source.ref ?? '')
  const [refKind, setRefKind] = useState<RefKind>(app?.source.refKind ?? 'branch')
  const [token, setToken] = useState('')
  const [clearToken, setClearToken] = useState(false)
  const [values, setValues] = useState(app?.values ?? '')
  const [params, setParams] = useState((app?.params ?? []).join('\n'))
  const [createNs, setCreateNs] = useState(false)
  const [valuesFile, setValuesFile] = useState('')
  const [ignoreDefaults, setIgnoreDefaults] = useState(false)
  const [fleet, setFleet] = useState('')
  const meta = useLiveMeta()
  const fleets = (meta.settings?.fleets?.[cluster] ?? []).filter((f) => f.deploy)
  const [refs, setRefs] = useState<RepoRefs | null>(null)
  const [loadingRefs, setLoadingRefs] = useState(() => (mode === 'switch' || mode === 'upgrade') && !!app)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [yamlError, setYamlError] = useState('')
  // What the repository offered the form (M-07): looked up once the URL is typed.
  const [inspected, setInspected] = useState<{ repo: string; state: 'busy' | 'done' | 'error'; data?: RepoInspection; message?: string } | null>(null)
  const first = useRef<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | null>(null)
  const box = useRef<HTMLDivElement>(null)

  // Focus starts in the dialog, so Escape and Tab are its own: the first
  // field where there is one, else the dialog itself (an upgrade's refs
  // are still loading).
  useEffect(() => {
    ;(first.current ?? box.current)?.focus()
  }, [])

  // Switch and upgrade list the repository's refs at once; the others on request.
  useEffect(() => {
    if ((mode === 'switch' || mode === 'upgrade') && row && app) {
      fetchAppRefs(cluster, row.namespace, row.name)
        .then((r) => setRefs(r))
        .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => setLoadingRefs(false))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Looks over the repository and fills what is still empty: the chart's
  // path, the branch, and what the README's install example says (M-07).
  const inspect = () => {
    const url = repo.trim()
    if (mode !== 'create' || !url || (inspected?.repo === url && inspected.state !== 'error')) return
    setInspected({ repo: url, state: 'busy' })
    inspectRepo(cluster, url, ref.trim(), refKind, token || undefined)
      .then((d) => {
        setInspected({ repo: url, state: 'done', data: d })
        const chart = d.charts[0]
        if (!path.trim() && chart) setPath(chart.path)
        if (!ref.trim()) {
          setRef(d.ref)
          setRefKind(d.refKind)
        }
        const h = d.readme
        if (!h) return
        if (!name.trim() && h.name) setName(h.name)
        if (!namespace.trim() && h.namespace) setNamespace(h.namespace)
        if (!values.trim() && h.values) setValues(h.values)
        if (!params.trim() && h.params?.length) setParams(h.params.join('\n'))
        if (h.createNamespace) setCreateNs(true)
      })
      .catch((e: unknown) => setInspected({ repo: url, state: 'error', message: e instanceof Error ? e.message : String(e) }))
  }

  const loadRefs = () => {
    setError('')
    setLoadingRefs(true)
    fetchRepoRefs(cluster, repo, token)
      .then((r) => {
        setRefs(r)
        if (!ref && r.default) {
          setRef(r.default)
          setRefKind('branch')
        }
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoadingRefs(false))
  }

  const paramList = () =>
    params
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith('#'))

  const source = (): AppSource => ({ repo: repo.trim(), path: path.trim(), ref: ref.trim(), refKind })

  const submit = (deployNow: boolean) => {
    setError('')
    for (const p of paramList()) {
      if (!p.includes('=')) {
        setError(`"${p}" is not key=value`)
        return
      }
    }
    setBusy(true)
    let req: Promise<WatsonItem | AppView>
    const input: AppInput = {}
    switch (mode) {
      case 'create':
        req = createApp(cluster, { namespace: namespace.trim(), name: name.trim(), source: source(), token: token || undefined, values, params: paramList(), createNamespace: createNs, ...(valuesFile ? { valuesFile } : {}), ...(ignoreDefaults ? { ignoreDefaults } : {}), ...(fleet ? { fleet } : {}) })
        break
      case 'source':
        input.source = source()
        if (token) input.token = token
        if (clearToken) input.clearToken = true
        if (deployNow) {
          input.upgrade = true
          input.install = true
        }
        req = updateApp(cluster, row!.namespace, row!.name, input)
        break
      case 'switch':
        req = upgradeApp(cluster, row!.namespace, row!.name, { ref: ref.trim(), refKind })
        break
      case 'upgrade':
      case 'install':
        req = upgradeApp(cluster, row!.namespace, row!.name, { ...(ref.trim() && (ref.trim() !== app?.source.ref || refKind !== app?.source.refKind) ? { ref: ref.trim(), refKind } : {}), ...(row?.orphan ? { install: true } : {}) })
        break
      case 'values':
        req = updateApp(cluster, row!.namespace, row!.name, { values, params: paramList(), upgrade: deployNow })
        break
    }
    req.then(
      (r) => {
        setBusy(false)
        onDone(r, mode)
      },
      (e: unknown) => {
        setBusy(false)
        setError(e instanceof Error ? e.message : String(e))
      },
    )
  }

  const primary = { create: 'Install', source: 'Save', switch: 'Switch and upgrade', upgrade: row?.orphan ? 'Install' : 'Deploy this branch or tag', install: 'Install', values: 'Save' }[mode]
  // Save keeps the change for the next deploy; the second button deploys it at once.
  const alsoDeploy = mode === 'source' || mode === 'values' ? (row?.orphan || !app ? 'Save and install' : 'Save and upgrade') : ''
  const canSubmit = !busy && !yamlError && (mode === 'create' ? namespace.trim() !== '' && name.trim() !== '' && repo.trim() !== '' && ref.trim() !== '' : mode === 'source' ? repo.trim() !== '' && ref.trim() !== '' : mode === 'switch' ? ref.trim() !== '' : true)
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()

  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Cancel" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={mode === 'upgrade' && row?.orphan ? TITLES.install : TITLES[mode]}
        className="rk-card"
        data-testid="app-dialog"
        data-mode={mode}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(760px, 100%)', maxHeight: 'calc(100vh - 32px)', overflow: 'auto', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{mode === 'upgrade' && row?.orphan ? TITLES.install : TITLES[mode]}</h2>
        {row && (
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
            Release <span className="rk-mono">{row.name}</span> in <span className="rk-mono">{row.namespace}</span>
            {app ? <> · {sourceText(app.source)}</> : ' · no source kept yet'}
          </p>
        )}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: '12px 16px' }}>
          {mode === 'create' && (
            <>
              <Field label="Namespace" hint="An existing one, or a new one with the box below">
                <label className="rk-field">
                  <input ref={first as never} className="rk-mono" list="app-namespaces" value={namespace} onChange={(e) => setNamespace(e.target.value)} placeholder="namespace" data-testid="app-namespace" />
                  <datalist id="app-namespaces">
                    {namespaces.map((n) => (
                      <option key={n} value={n} />
                    ))}
                  </datalist>
                </label>
              </Field>
              <Field label="Release name" hint="Lower-case letters, digits and dashes">
                <label className="rk-field">
                  <input className="rk-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-app" data-testid="app-name" />
                </label>
              </Field>
            </>
          )}
          {(mode === 'create' || mode === 'source') && (
            <>
              <Field label="Repository" hint="An https:// git URL; GitHub, GitLab and Gitea work" wide>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  <label className="rk-field" style={{ flex: '1 1 320px' }}>
                    <input ref={mode === 'source' ? (first as never) : undefined} className="rk-mono" value={repo} onChange={(e) => setRepo(e.target.value)} onBlur={inspect} onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), inspect())} placeholder="https://github.com/org/app" data-testid="app-repo" />
                  </label>
                  <button type="button" className="rk-btn" onClick={loadRefs} disabled={!repo.trim() || loadingRefs} title="Lists the repository's branches and tags" data-testid="app-load-refs">
                    {loadingRefs ? 'Reading…' : 'List branches and tags'}
                  </button>
                </div>
                {inspected && (
                  <div style={{ fontSize: 12, color: inspected.state === 'error' ? 'var(--err)' : 'var(--fg2)', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }} data-testid="app-inspect" data-state={inspected.state}>
                    {inspected.state === 'busy' ? (
                      'Looking over the repository…'
                    ) : inspected.state === 'error' ? (
                      inspected.message
                    ) : inspected.data && inspected.data.charts.length === 0 ? (
                      `No Chart.yaml within four directories of the root at ${inspected.data.ref}.`
                    ) : (
                      <>
                        <span>
                          {inspected.data!.charts.length === 1 ? 'Chart' : `${inspected.data!.charts.length} charts, chart`}{' '}
                          <span className="rk-mono">{inspected.data!.charts[0]!.path || '/'}</span> ({inspected.data!.charts[0]!.name} {inspected.data!.charts[0]!.version}) at {inspected.data!.ref}
                          {inspected.data!.readme ? `; ${inspected.data!.readme.file}'s helm install example filled what was empty` : ''}
                        </span>
                        {inspected.data!.charts.length > 1 && (
                          <label className="rk-field" style={{ width: 'auto' }}>
                            <select value={path} onChange={(e) => setPath(e.target.value)} aria-label="Chart in the repository" data-testid="app-chart-pick">
                              {inspected.data!.charts.map((c) => (
                                <option key={c.path} value={c.path}>
                                  {c.path || '/'} · {c.name} {c.version}
                                </option>
                              ))}
                            </select>
                          </label>
                        )}
                      </>
                    )}
                  </div>
                )}
              </Field>
              <Field label="Chart path" hint="The chart's directory in the repository; empty for the root">
                <label className="rk-field">
                  <input className="rk-mono" value={path} onChange={(e) => setPath(e.target.value)} placeholder="charts/app" data-testid="app-path" />
                </label>
              </Field>
              <Field label="Token" hint={app?.hasToken ? 'A token is saved; paste a new one to replace it' : 'Only for a private repository; it is kept in the console and never shown again'}>
                <label className="rk-field">
                  <input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder={app?.hasToken ? '•••••••• saved' : 'optional'} data-testid="app-token" />
                </label>
                {app?.hasToken && (
                  <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
                    <input type="checkbox" checked={clearToken} onChange={(e) => setClearToken(e.target.checked)} /> Remove the saved token
                  </label>
                )}
              </Field>
            </>
          )}
          {mode !== 'values' && (
            <Field label={mode === 'upgrade' ? 'Branch or tag to deploy: the current one again, a newer one (upgrade) or an older one (rollback)' : 'Branch or tag'} wide hint={loadingRefs ? 'Reading the repository…' : refs ? `${refs.branches.length} branches, ${refs.tags.length} tags` : mode === 'create' || mode === 'source' ? 'List branches and tags above to choose from them' : undefined}>
              <RefPicker refs={refs} value={ref} kind={refKind} onChange={(r, k) => (setRef(r), setRefKind(k))} id="app" />
            </Field>
          )}
          {(mode === 'create' || mode === 'values') && (
            <>
              <Field label="Values file" hint="YAML, as helm -f takes it; the chart's defaults apply underneath" wide>
                <YamlEditor value={values} onChange={({ text, errors }) => (setValues(text), setYamlError(errors[0] ?? ''))} label="Values file" testId="app-values" minHeight={mode === 'values' ? 360 : 200} placeholder={'replicaCount: 2\nimage:\n  tag: v1.2.0'} />
              </Field>
              <Field label="Parameters" hint="One key=value per line, as helm --set takes them; applied over the values file" wide>
                <textarea className="rk-area rk-mono" value={params} onChange={(e) => setParams(e.target.value)} spellCheck={false} style={{ minHeight: 72, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }} placeholder="image.tag=v1.2.0" data-testid="app-params" />
              </Field>
            </>
          )}
          {mode === 'create' && (
            <>
              {(() => {
                const files = inspected?.state === 'done' && inspected.data ? (inspected.data.charts.find((c) => c.path === path.trim())?.valuesFiles ?? []) : []
                return files.length > 0 || valuesFile ? (
                  <Field label="Second values file" hint="From the chart's directory, applied over the chart's values.yaml and under the values above (R2-08)">
                    <label className="rk-field">
                      <select className="rk-mono" value={valuesFile} onChange={(e) => setValuesFile(e.target.value)} data-testid="app-values-file">
                        <option value="">none: values.yaml only</option>
                        {[...new Set([...(valuesFile ? [valuesFile] : []), ...files])].map((f) => (
                          <option key={f} value={f}>
                            {f}
                          </option>
                        ))}
                      </select>
                    </label>
                  </Field>
                ) : null
              })()}
              {fleets.length > 0 && (
                <Field label="Deploy to the fleet" hint="The fleet's deploy label becomes the pods' nodeSelector">
                  <label className="rk-field">
                    <select value={fleet} onChange={(e) => setFleet(e.target.value)} data-testid="app-fleet">
                      <option value="">any node</option>
                      {fleets.map((f) => (
                        <option key={f.id} value={f.id}>
                          {f.name} · {f.deploy}
                        </option>
                      ))}
                    </select>
                  </label>
                </Field>
              )}
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px 18px', gridColumn: '1 / -1' }}>
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5 }}>
                  <input type="checkbox" checked={createNs} onChange={(e) => setCreateNs(e.target.checked)} data-testid="app-create-ns" /> Create the namespace if it does not exist
                </label>
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5 }} title="The chart's own values.yaml is left out: only the files and values given here apply">
                  <input type="checkbox" checked={ignoreDefaults} onChange={(e) => setIgnoreDefaults(e.target.checked)} data-testid="app-ignore-defaults" /> Ignore the chart’s values.yaml
                </label>
              </div>
            </>
          )}
        </div>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <p className="rk-hint" style={{ margin: 0 }}>
          {alsoDeploy ? 'Save keeps the change for the next deploy; ' : ''}A deploy runs as you, through Watson, which shows how it goes; on a production cluster with the two-person rule a second person approves it first.
        </p>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button type="button" className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="button" className="rk-btn" data-on={alsoDeploy ? undefined : 'true'} onClick={() => submit(false)} disabled={!canSubmit} aria-busy={busy} data-testid="app-submit">
            {busy ? 'Sending…' : primary}
          </button>
          {alsoDeploy && (
            <button type="button" className="rk-btn" data-on="true" onClick={() => submit(true)} disabled={!canSubmit} aria-busy={busy} data-testid="app-submit-deploy">
              {alsoDeploy}
            </button>
          )}
        </div>
      </div>
    </div>,
    layer(),
  )
}

export { isCard }
