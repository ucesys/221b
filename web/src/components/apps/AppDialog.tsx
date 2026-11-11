import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import {
  createApp,
  fetchAppRefs,
  fetchChartVersions,
  fetchHelmRepos,
  fetchRepoCharts,
  fetchRepoRefs,
  fromRepository,
  inspectChart,
  inspectRepo,
  isCard,
  sourceText,
  updateApp,
  upgradeApp,
  type AppInput,
  type AppSource,
  type AppView,
  type ChartInfo,
  type HelmRepository,
  type RefKind,
  type RepoInspection,
  type RepoRefs,
  type SourceKind,
} from '../../api/apps'
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
  create: 'New application',
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

// --- Helm repositories (Phase 12, wave 5, HR-05, HR-06) ---

const KINDS: [SourceKind, string, string][] = [
  ['git', 'Git repository', 'A chart in a git repository, at a branch or tag'],
  ['helm', 'Helm repository', 'A chart of a Helm repository Settings › Applications lists'],
  ['oci', 'OCI registry', 'A chart in an OCI registry: oci://registry/path/chart'],
]

/** Splits an OCI chart's address into the registry's path and the chart: oci://r/charts/app is oci://r/charts and app. */
export function splitOCI(ref: string): { repo: string; chart: string } {
  const t = ref.trim().replace(/\/+$/, '')
  const i = t.lastIndexOf('/')
  if (!t.startsWith('oci://') || i <= 'oci://'.length) return { repo: t, chart: '' }
  return { repo: t.slice(0, i), chart: t.slice(i + 1) }
}

/** Picks a repository chart's version from the list, or takes one typed (HR-05). */
function VersionPicker({ versions, value, onChange, id }: { versions: string[]; value: string; onChange: (v: string) => void; id: string }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
      {versions.length > 0 && (
        <label className="rk-field" style={{ minWidth: 220 }}>
          <select value={versions.includes(value) ? value : ''} onChange={(e) => e.target.value && onChange(e.target.value)} aria-label="Version" data-testid={`${id}-version`}>
            <option value="">{versions.includes(value) ? '' : 'Choose a version…'}</option>
            {versions.map((v, i) => (
              <option key={v} value={v}>
                {v}
                {i === 0 ? ' (newest)' : ''}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="rk-field" style={{ minWidth: 160 }}>
        <input className="rk-mono" value={value} onChange={(e) => onChange(e.target.value)} placeholder={versions.length ? 'or type one' : '1.2.0'} aria-label="Version" data-testid={`${id}-version-text`} />
      </label>
    </div>
  )
}

/** The fields a repository's look-over may fill. */
interface Fillable {
  path: string
  ref: string
  name: string
  namespace: string
  values: string
  params: string
}

/** What a repository's look-over fills: the fields empty now, whatever they were when it was asked (M-07). */
export function inspectFill(now: Fillable, d: RepoInspection): Partial<Fillable> & { refKind?: RefKind } {
  const out: Partial<Fillable> & { refKind?: RefKind } = {}
  const chart = d.charts[0]
  if (!now.path.trim() && chart) out.path = chart.path
  if (!now.ref.trim()) {
    out.ref = d.ref
    out.refKind = d.refKind
  }
  const h = d.readme
  if (!h) return out
  if (!now.name.trim() && h.name) out.name = h.name
  if (!now.namespace.trim() && h.namespace) out.namespace = h.namespace
  if (!now.values.trim() && h.values) out.values = h.values
  if (!now.params.trim() && h.params?.length) out.params = h.params.join('\n')
  return out
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
  // --- Helm repositories (Phase 12, wave 5): the chart's source kind, its repository and name.
  const [kind, setKind] = useState<SourceKind>(app?.source.kind ?? 'git')
  const [repository, setRepository] = useState(app?.source.repository ?? '')
  const [chart, setChart] = useState(app?.source.kind === 'oci' ? `${app.source.repo}/${app.source.chart ?? ''}` : (app?.source.chart ?? ''))
  const [helmRepos, setHelmRepos] = useState<HelmRepository[] | null>(null)
  const [chartsOf, setChartsOf] = useState<{ repository: string; list: ChartInfo[] }>({ repository: '', list: [] })
  const charts = chartsOf.repository === repository ? chartsOf.list : []
  const [versions, setVersions] = useState<string[]>([])
  const [chartNote, setChartNote] = useState<{ state: 'busy' | 'done' | 'error'; text: string } | null>(null)
  const repoKind = kind === 'helm' || kind === 'oci'
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
  const inspecting = useRef(0)
  // The fields as they are now, for an answer that comes back seconds later.
  const fields = useRef<Fillable>({ path, ref, name, namespace, values, params })
  useEffect(() => {
    fields.current = { path, ref, name, namespace, values, params }
  })
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
    // Only the answer to the last look counts: the URL may have changed since.
    const asked = ++inspecting.current
    inspectRepo(cluster, url, ref.trim(), refKind, token || undefined)
      .then((d) => {
        if (asked !== inspecting.current) return
        setInspected({ repo: url, state: 'done', data: d })
        const f = inspectFill(fields.current, d)
        if (f.path !== undefined) setPath(f.path)
        if (f.ref !== undefined) setRef(f.ref)
        if (f.refKind) setRefKind(f.refKind)
        if (f.name !== undefined) setName(f.name)
        if (f.namespace !== undefined) setNamespace(f.namespace)
        // The YAML editor takes no new text while it has focus, so what it did not show must not be kept.
        if (f.values !== undefined && !document.activeElement?.closest('.rk-yaml')) setValues(f.values)
        if (f.params !== undefined) setParams(f.params)
        if (d.readme?.createNamespace) setCreateNs(true)
      })
      .catch((e: unknown) => {
        if (asked === inspecting.current) setInspected({ repo: url, state: 'error', message: e instanceof Error ? e.message : String(e) })
      })
  }

  // The repositories, once a repository's chart is asked for; a Helm repository's charts once one is chosen.
  useEffect(() => {
    if (!repoKind || helmRepos || (mode !== 'create' && mode !== 'source')) return
    fetchHelmRepos().then(
      (v) => setHelmRepos(v.repositories ?? []),
      (e: unknown) => (setHelmRepos([]), setError(e instanceof Error ? e.message : String(e))),
    )
  }, [repoKind, helmRepos, mode])
  useEffect(() => {
    if (kind !== 'helm' || !repository) return
    fetchRepoCharts(repository).then(
      (r) => setChartsOf({ repository, list: r.charts ?? [] }),
      (e: unknown) => setChartNote({ state: 'error', text: e instanceof Error ? e.message : String(e) }),
    )
  }, [kind, repository])

  /** The repository chart as the form holds it. */
  const chartSource = (): AppSource => {
    if (kind === 'oci') {
      const o = splitOCI(chart)
      return { kind, repo: o.repo, chart: o.chart, ref: ref.trim(), refKind: 'version' }
    }
    return { kind: 'helm', repo: '', repository, chart: chart.trim(), ref: ref.trim(), refKind: 'version' }
  }

  // Lists the chart's versions, and takes the newest when none is chosen.
  const loadVersions = () => {
    const src = chartSource()
    if (!src.chart || (kind === 'helm' && !repository)) return
    setChartNote({ state: 'busy', text: 'Reading the versions…' })
    fetchChartVersions(src).then(
      (v) => {
        setVersions(v.versions ?? [])
        if (!ref.trim() && v.newest) {
          setRef(v.newest)
          inspectAt(v.newest)
        }
        setRefKind('version')
        setChartNote({ state: 'done', text: `${(v.versions ?? []).length} versions; the newest stable is ${v.newest || 'none'}` })
      },
      (e: unknown) => setChartNote({ state: 'error', text: e instanceof Error ? e.message : String(e) }),
    )
  }

  // A chart at a version is looked over for its values files (R2-08), once a version is chosen.
  const inspectAt = (version: string) => {
    if (mode !== 'create' || !repoKind) return
    const src = { ...chartSource(), ref: version.trim() }
    if (!src.chart || !src.ref || (kind === 'helm' && !repository)) return
    const key = sourceText(src)
    const asked = ++inspecting.current
    setInspected({ repo: key, state: 'busy' })
    inspectChart(cluster, src).then(
      (d) => asked === inspecting.current && setInspected({ repo: key, state: 'done', data: d }),
      (e: unknown) => asked === inspecting.current && setInspected({ repo: key, state: 'error', message: e instanceof Error ? e.message : String(e) }),
    )
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

  const source = (): AppSource => (repoKind ? chartSource() : { repo: repo.trim(), path: path.trim(), ref: ref.trim(), refKind: refKind === 'version' ? 'branch' : refKind })

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
        req = createApp(cluster, {
          namespace: namespace.trim(),
          name: name.trim(),
          source: source(),
          token: (!repoKind && token) || undefined,
          values,
          params: paramList(),
          createNamespace: createNs,
          ...(valuesFile ? { valuesFile } : {}),
          ...(ignoreDefaults ? { ignoreDefaults } : {}),
          ...(fleet ? { fleet } : {}),
        })
        break
      case 'source':
        input.source = source()
        if (token && !repoKind) input.token = token
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
  const sourceReady = repoKind ? chartSource().chart !== '' && (kind !== 'helm' || repository !== '') && ref.trim() !== '' : repo.trim() !== '' && ref.trim() !== ''
  const canSubmit = !busy && !yamlError && (mode === 'create' ? namespace.trim() !== '' && name.trim() !== '' && sourceReady : mode === 'source' ? sourceReady : mode === 'switch' ? ref.trim() !== '' : true)
  // A release whose chart comes from a repository moves between versions, not branches.
  const appRepoKind = fromRepository(app?.source)
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
            <Field label="Where the chart comes from" wide hint={KINDS.find((k) => k[0] === kind)?.[2]}>
              <div className="rk-seg" role="group" aria-label="Where the chart comes from" style={{ alignSelf: 'flex-start' }}>
                {KINDS.map(([k, l]) => (
                  <button
                    key={k}
                    type="button"
                    aria-pressed={kind === k}
                    onClick={() => {
                      if (k === kind) return
                      setKind(k)
                      setRef('')
                      setRefKind(k === 'git' ? 'branch' : 'version')
                      setVersions([])
                      setChartNote(null)
                      setInspected(null)
                    }}
                    data-testid={`app-kind-${k}`}
                  >
                    {l}
                  </button>
                ))}
              </div>
            </Field>
          )}
          {(mode === 'create' || mode === 'source') && kind === 'helm' && (
            <>
              <Field
                label="Helm repository"
                hint={helmRepos && helmRepos.filter((r) => r.kind === 'helm').length === 0 ? 'None yet: a SuperAdmin adds them in Administration › Settings › Applications' : 'As Settings › Applications lists them'}
              >
                <label className="rk-field">
                  <select value={repository} onChange={(e) => (setRepository(e.target.value), setChart(''), setRef(''), setVersions([]))} data-testid="app-repository">
                    <option value="">{helmRepos ? 'Choose a repository…' : 'Loading…'}</option>
                    {(helmRepos ?? [])
                      .filter((r) => r.kind === 'helm')
                      .map((r) => (
                        <option key={r.name} value={r.name}>
                          {r.name} · {r.url}
                        </option>
                      ))}
                  </select>
                </label>
              </Field>
              <Field label="Chart" hint={charts.find((c) => c.name === chart)?.description ?? (charts.length ? `${charts.length} charts in the repository` : undefined)}>
                <label className="rk-field">
                  <input className="rk-mono" list="app-charts" value={chart} onChange={(e) => (setChart(e.target.value), setVersions([]))} onBlur={loadVersions} placeholder="nginx" data-testid="app-chart" />
                  <datalist id="app-charts">
                    {charts.map((c) => (
                      <option key={c.name} value={c.name}>
                        {c.latest}
                      </option>
                    ))}
                  </datalist>
                </label>
              </Field>
            </>
          )}
          {(mode === 'create' || mode === 'source') && kind === 'oci' && (
            <Field label="Chart" hint="oci:// with the registry, the path and the chart; an OCI registry Settings › Applications lists signs in for it" wide>
              <label className="rk-field">
                <input className="rk-mono" value={chart} onChange={(e) => (setChart(e.target.value), setVersions([]))} onBlur={loadVersions} placeholder="oci://registry.example.com/charts/app" data-testid="app-oci" />
              </label>
            </Field>
          )}
          {(mode === 'create' || mode === 'source') && repoKind && (
            <Field label="Version" wide hint={chartNote?.text ?? 'Leave the chart field to list its versions'}>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
                <VersionPicker versions={versions} value={ref} onChange={(v) => (setRef(v), setRefKind('version'), versions.includes(v) && inspectAt(v))} id="app" />
                <button type="button" className="rk-btn" onClick={loadVersions} disabled={!chartSource().chart || (kind === 'helm' && !repository)} data-testid="app-load-versions">
                  List versions
                </button>
              </div>
              {inspected && inspected.state !== 'busy' && (
                <span style={{ fontSize: 12, color: inspected.state === 'error' ? 'var(--err)' : 'var(--fg2)' }} data-testid="app-chart-inspect" data-state={inspected.state}>
                  {inspected.state === 'error'
                    ? inspected.message
                    : inspected.data?.charts[0]
                      ? `Chart ${inspected.data.charts[0].name} ${inspected.data.charts[0].version}${inspected.data.charts[0].appVersion ? ` (app ${inspected.data.charts[0].appVersion})` : ''}`
                      : ''}
                </span>
              )}
            </Field>
          )}
          {(mode === 'create' || mode === 'source') && kind === 'git' && (
            <>
              <Field label="Repository" hint="An https:// git URL; GitHub, GitLab and Gitea work" wide>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  <label className="rk-field" style={{ flex: '1 1 320px' }}>
                    <input
                      ref={mode === 'source' ? (first as never) : undefined}
                      className="rk-mono"
                      value={repo}
                      onChange={(e) => setRepo(e.target.value)}
                      onBlur={inspect}
                      onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), inspect())}
                      placeholder="https://github.com/org/app"
                      data-testid="app-repo"
                    />
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
                          {inspected.data!.charts.length === 1 ? 'Chart' : `${inspected.data!.charts.length} charts, chart`} <span className="rk-mono">{inspected.data!.charts[0]!.path || '/'}</span> ({inspected.data!.charts[0]!.name}{' '}
                          {inspected.data!.charts[0]!.version}) at {inspected.data!.ref}
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
              <Field
                label="Token"
                hint={
                  app?.hasToken && repo.trim() !== app.source.repo
                    ? 'The saved token is removed with the repository change; paste one for the new repository'
                    : app?.hasToken
                      ? 'A token is saved; paste a new one to replace it'
                      : 'Only for a private repository; it is kept in the console and never shown again'
                }
              >
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
          {mode !== 'values' &&
            !((mode === 'create' || mode === 'source') && repoKind) &&
            ((mode === 'switch' || mode === 'upgrade' || mode === 'install') && appRepoKind ? (
              <Field
                label={mode === 'upgrade' ? 'Version to deploy: the current one again, a newer one (upgrade) or an older one (rollback)' : 'Version'}
                wide
                hint={loadingRefs ? 'Reading the repository…' : refs ? `${refs.tags.length} versions` : undefined}
              >
                <VersionPicker versions={refs?.tags ?? []} value={ref} onChange={(v) => (setRef(v), setRefKind('version'))} id="app" />
              </Field>
            ) : (
              <Field
                label={mode === 'upgrade' ? 'Branch or tag to deploy: the current one again, a newer one (upgrade) or an older one (rollback)' : 'Branch or tag'}
                wide
                hint={loadingRefs ? 'Reading the repository…' : refs ? `${refs.branches.length} branches, ${refs.tags.length} tags` : mode === 'create' || mode === 'source' ? 'List branches and tags above to choose from them' : undefined}
              >
                <RefPicker refs={refs} value={ref} kind={refKind === 'version' ? 'branch' : refKind} onChange={(r, k) => (setRef(r), setRefKind(k))} id="app" />
              </Field>
            ))}
          {(mode === 'create' || mode === 'values') && (
            <>
              <Field label="Values file" hint="YAML, as helm -f takes it; the chart's defaults apply underneath" wide>
                <YamlEditor
                  value={values}
                  onChange={({ text, errors }) => (setValues(text), setYamlError(errors[0] ?? ''))}
                  label="Values file"
                  testId="app-values"
                  minHeight={mode === 'values' ? 360 : 200}
                  placeholder={'replicaCount: 2\nimage:\n  tag: v1.2.0'}
                />
              </Field>
              <Field label="Parameters" hint="One key=value per line, as helm --set takes them; applied over the values file" wide>
                <textarea
                  className="rk-area rk-mono"
                  value={params}
                  onChange={(e) => setParams(e.target.value)}
                  spellCheck={false}
                  style={{ minHeight: 72, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }}
                  placeholder="image.tag=v1.2.0"
                  data-testid="app-params"
                />
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
