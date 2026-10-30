import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchAppInspect, fetchAppRefs, fetchDrift, fetchImages, fetchRevisions, inspectRepo, sourceText, updateApp, type AppDrift, type AppImage, type AppInput, type AppRefs, type AppSource, type AppView, type RefKind, type RepoInspection, type RevisionView } from '../../api/apps'
import { useLiveMeta } from '../../api/live'
import { useDetails } from '../../api/details'
import type { ClusterInfo, ReleaseDetails } from '../../api/types'
import type { WatsonItem } from '../../api/watson'
import { behindSource, driftTone, type AppRow } from '../../columns/apps'
import { useWatsonApi } from '../Watson'
import { YamlEditor } from '../YamlEditor'
import { fmtAge, fmtTs, nWord } from '../../lib/format'
import { apiPath, href, type Route } from '../../lib/router'
import { CopyName } from '../CopyName'
import { Card } from '../Drawer'
import { CloseIcon } from '../Icons'
import { ReleaseBody, SUBTABS, type ExtraTab } from '../ReleaseBody'
import { AppToolsButton, type AppAction } from './AppMenu'
import { DiffDialog } from './DiffDialog'
import { DriftDialog } from './DriftDialog'
import { RevisionActions } from './revisionActions'

export const APP_TABS = ['overview', ...SUBTABS, 'images'] as const
export type AppTab = (typeof APP_TABS)[number]

interface Props {
  cluster: ClusterInfo | null
  clusterId: string
  now: number
  route: Route
  row: AppRow
  tab: AppTab
  onTab: (t: AppTab) => void
  canChange: boolean
  onAction: (a: AppAction) => void
  onToast: (msg: string) => void
  /** What a change answered: the card, or the saved record. */
  onResult: (r: WatsonItem | AppView, doing: string) => void
  /** Bumps when a change ran, so the panel reads again. */
  version: number
  onDrift: (d: AppDrift) => void
}

/**
 * The release's panel beside the table (HA-14): everything the Helm tab
 * shows, with an Overview (source, commit, drift) first, the values file
 * editable above the values as deployed, the History's newer-version
 * notice and diffs, and the Images last.
 */
export function AppPanel({ cluster, clusterId, now, route, row, tab, onTab, canChange, onAction, onToast, onResult, version, onDrift }: Props) {
  const app = row.app
  const watson = useWatsonApi()
  const [drift, setDrift] = useState<AppDrift | null>(app?.drift ?? null)
  const [checking, setChecking] = useState(false)
  const [refs, setRefs] = useState<AppRefs | null>(null)
  const [revisions, setRevisions] = useState<RevisionView[] | null>(null)
  const [images, setImages] = useState<{ revision: number; images: AppImage[] } | null>(null)
  const [imageRev, setImageRev] = useState<number | undefined>(undefined)
  const [diffFor, setDiffFor] = useState<number | null>(null)
  const [driftOpen, setDriftOpen] = useState(false)
  const [err, setErr] = useState('')
  const details = useDetails<ReleaseDetails>(row.orphan ? null : apiPath(`helm/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`))
  const d = details.data
  const key = `${row.namespace}/${row.name}`

  const check = useCallback(
    (refresh: boolean) => {
      if (!app || row.orphan) return
      setChecking(true)
      fetchDrift(clusterId, row.namespace, row.name, refresh)
        .then((x) => {
          setDrift(x)
          onDrift(x)
        })
        .catch((e: unknown) => onToast(e instanceof Error ? e.message : String(e)))
        .finally(() => setChecking(false))
    },
    [app, row.orphan, clusterId, row.namespace, row.name, onDrift, onToast],
  )

  // The tab mounts the panel afresh per release and per change that ran
  // (its key), so the state above starts over; the first drift check is
  // asked for once, off the render.
  useEffect(() => {
    if (!row.app || row.app.drift || row.orphan) return
    const t = setTimeout(() => check(false), 0)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, version])

  // The row is an orphan until the release list is in, when the panel
  // mounts before it: the revisions load once it is not (M-03 fix).
  useEffect(() => {
    if ((tab !== 'history' && tab !== 'images') || row.orphan) return
    fetchRevisions(clusterId, row.namespace, row.name)
      .then(setRevisions)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
    if (tab === 'history' && app && !refs) {
      fetchAppRefs(clusterId, row.namespace, row.name)
        .then(setRefs)
        .catch(() => undefined)
    }
    // The live row's revision moves when a deploy lands: the list follows
    // it, so the History tab and the diff dialog never show a stale one
    // (asked October 30, 2026).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, key, version, row.orphan, row.revision])

  useEffect(() => {
    if (tab !== 'images' || row.orphan) return
    fetchImages(clusterId, row.namespace, row.name, imageRev)
      .then(setImages)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
  }, [tab, key, version, imageRev, clusterId, row.namespace, row.name, row.orphan])

  const commit = app?.commit ? app.commit.slice(0, 12) : ''

  const overview = <SourceCard clusterId={clusterId} row={row} app={app} canChange={canChange} onResult={onResult} now={now} />

  const imagesTab = (
    <Card
      title="Images"
      aside={
        revisions && revisions.length > 1 ? (
          <label className="rk-field" style={{ width: 150 }}>
            <select value={imageRev ?? ''} onChange={(e) => setImageRev(e.target.value ? Number(e.target.value) : undefined)} aria-label="Revision" data-testid="app-images-rev">
              <option value="">current revision</option>
              {revisions.map((r) => (
                <option key={r.revision} value={r.revision}>
                  revision {r.revision}
                </option>
              ))}
            </select>
          </label>
        ) : undefined
      }
      pad={false}
    >
      {!images ? (
        <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>{err || 'Loading…'}</p>
      ) : images.images.length === 0 ? (
        <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>The manifest of revision {images.revision} runs no container.</p>
      ) : (
        <div role="table" aria-label="Images" style={{ display: 'grid', gridTemplateColumns: '1fr auto auto 1fr', fontSize: 12.5 }} data-testid="app-images">
          {['Image', 'Tag', 'Registry', 'Used by'].map((h) => (
            <span key={h} role="columnheader" style={{ padding: '8px 12px', fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', borderBottom: '1px solid var(--line)' }}>
              {h}
            </span>
          ))}
          {images.images.map((im, i) => (
            <div key={i} role="row" style={{ display: 'contents' }} data-image={im.image}>
              <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', minWidth: 0, overflowWrap: 'anywhere' }} title={im.image}>
                {im.repository}
                {im.digest ? <span style={{ color: 'var(--fg3)' }}>@{im.digest.slice(0, 19)}…</span> : ''}
              </span>
              <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)' }}>
                {im.tag || (im.digest ? '(digest)' : 'latest')}
              </span>
              <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', color: 'var(--fg3)' }}>
                {im.registry || 'docker.io'}
              </span>
              <span role="cell" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', color: 'var(--fg2)' }}>
                {im.kind} <span className="rk-mono">{im.name}</span> · {im.init ? 'init container ' : ''}
                <span className="rk-mono">{im.container}</span>
              </span>
            </div>
          ))}
        </div>
      )}
      <p style={{ margin: 0, padding: '10px 16px 12px', fontSize: 11.5, color: 'var(--fg3)' }}>
        Every image the manifest of {images ? `revision ${images.revision}` : 'the release'} names: {images ? nWord(new Set(images.images.map((x) => x.image)).size, 'distinct image', 'distinct images') : ''}.
      </p>
    </Card>
  )

  const historyAbove = (
    <>
      {app && refs && (refs.newerTags.length > 0 || refs.moved) && (
        <div className="rk-callout" data-tone="info" data-testid="app-newer">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
            <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{refs.newerTags.length > 0 ? `A newer version is available: ${refs.newerTags[0]}` : `Branch ${app.source.ref} has new commits`}</strong>
            <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
              {refs.newerTags.length > 1 ? `Newer tags: ${refs.newerTags.join(', ')}. ` : ''}
              {refs.moved && refs.head ? `The branch is at ${refs.head.slice(0, 12)}; ${commit || 'nothing'} is deployed. ` : ''}
              {canChange && (
                <>
                  <button className="rk-link" onClick={() => onAction(refs.newerTags.length > 0 ? 'switch' : 'upgrade')}>
                    {refs.newerTags.length > 0 ? 'Switch to it' : 'Redeploy'}
                  </button>
                  , or compare it below first.
                </>
              )}
            </span>
          </div>
        </div>
      )}
    </>
  )

  const before: ExtraTab[] = [{ id: 'overview', label: 'Setup', render: () => overview }]
  const after: ExtraTab[] = [{ id: 'images', label: 'Images', count: images?.images.length, render: () => imagesTab }]

  return (
    <main className="rk-pane" aria-label={`Release ${row.name}`} style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }} data-testid="app-panel">
      <section aria-label={`Release ${row.name}`} style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
          <h2 className="rk-mono" style={{ margin: 0, fontSize: 18, fontWeight: 600 }} data-testid="app-title">
            {row.name}
            <CopyName text={row.name} what="release name" />
          </h2>
          <span className="rk-pill" data-tone={row.tone} title={row.description} data-testid="app-status">
            <span className="rk-dot" />
            {row.status}
          </span>
          {!row.orphan && (
            <span className="rk-tag" style={{ fontSize: 11, padding: '2px 7px' }}>
              revision {row.revision}
            </span>
          )}
          <span style={{ flex: '1 1 auto' }} />
          <AppToolsButton row={row} canChange={canChange} onPick={onAction} />
          <a className="rk-icon-btn" href={href('helm')} aria-label="Close the release" title="Close" data-testid="app-close">
            <CloseIcon />
          </a>
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 18px', fontSize: 12.5, color: 'var(--fg3)' }} data-testid="app-facts">
          <span>
            Namespace{' '}
            <a className="rk-link rk-mono" href={href('namespaces', [row.namespace])} style={{ fontSize: 12.5 }}>
              {row.namespace}
            </a>
          </span>
          {!row.orphan && (
            <>
              <span>
                Chart <span className="rk-mono" style={{ color: 'var(--fg)' }}>{row.chart}-{row.chartVersion}</span>
              </span>
              <span>
                App version <span className="rk-mono" style={{ color: 'var(--fg)' }}>{row.appVersion || '—'}</span>
              </span>
              <span>
                Updated <span className="rk-num" style={{ color: 'var(--fg)' }}>{row.updated ? `${fmtAge(row.updated, now)} ago · ${fmtTs(row.updated)}` : '—'}</span>
              </span>
            </>
          )}
          {behindSource(row) && (
            <span data-testid="app-behind" title={behindSource(row)}>
              <span className="rk-pill" data-tone="warn">
                <span className="rk-dot" />
                behind the source
              </span>
            </span>
          )}
          {drift && driftState(drift) !== 'in sync' && (
            <span data-testid="app-drift" title={drift.objects.filter((o) => o.state !== 'same').map((o) => `${o.kind} ${o.name}: ${o.state}${o.fields?.length ? ' (' + o.fields.join(', ') + ')' : ''}`).join('\n')}>
              <span className="rk-pill" data-tone={driftTone(row) === 'muted' ? 'warn' : driftTone(row)}>
                <span className="rk-dot" />
                {driftState(drift)}
              </span>{' '}
              {drift.changed} changed · {drift.missing} missing{drift.unknown ? ` · ${drift.unknown} unknown` : ''} · checked {fmtAge(drift.checked, now)} ago ·{' '}
              <button className="rk-link" onClick={() => check(true)} disabled={checking} data-testid="app-check-drift">
                {checking ? 'checking…' : 'check again'}
              </button>
              {drift.changed + drift.missing > 0 && (
                <>
                  {' · '}
                  <button className="rk-link" onClick={() => setDriftOpen(true)} data-testid="app-drift-what">
                    what changed
                  </button>
                </>
              )}
            </span>
          )}
          <span data-testid="app-source-line">
            {app ? (
              <>
                Source <span className="rk-mono" style={{ color: 'var(--fg)' }}>{sourceText(app.source)}</span>
                {commit ? (
                  <>
                    {' '}
                    · commit <span className="rk-mono" style={{ color: 'var(--fg)' }}>{commit}</span>
                  </>
                ) : (
                  ' · not deployed from it yet'
                )}
              </>
            ) : (
              <>
                The console keeps no source for this release.{' '}
                {canChange && (
                  <button className="rk-link" onClick={() => onTab('overview')} data-testid="app-set-source">
                    Set its source
                  </button>
                )}
              </>
            )}
          </span>
        </div>
        {row.orphan ? (
          <>
            <div className="rk-callout" data-tone="warn" data-testid="app-orphan">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Not installed</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
                  The console keeps this application's source, values and parameters, but there is no release {row.name} in {row.namespace}: its install failed, or the release was removed another way. Watson has the install's output.
                  {canChange ? ' Install it from the Tools, change its values first, or forget it.' : ''}
                </span>
              </div>
            </div>
            {overview}
            {app && <ValuesEditor clusterId={clusterId} row={row} app={app} canChange={canChange} onResult={onResult} />}
          </>
        ) : (
          <RevisionActions.Provider
            value={{
              diff: (rev) => {
                setDiffFor(rev)
                // Asked before the revisions are in: fetch them now, the dialog opens when they are.
                if (!revisions && !row.orphan)
                  fetchRevisions(clusterId, row.namespace, row.name)
                    .then(setRevisions)
                    .catch((e: unknown) => onToast(e instanceof Error ? e.message : String(e)))
              },
              current: row.revision,
              // An older revision rolls back through Watson, a card to approve (M-03).
              rollback:
                canChange && watson.available && !watson.readOnly
                  ? (rev) => watson.send(`helm rollback ${row.name} ${rev} -n ${row.namespace}`, { from: 'Helm tab', why: `Roll ${row.namespace}/${row.name} back to revision ${rev}.` })
                  : undefined,
              rollbackOff: !canChange ? 'for Admins and SuperAdmins' : !watson.available ? 'Watson is off here' : watson.readOnly ? 'this cluster is connected read-only' : undefined,
              drift: drift && drift.changed + drift.missing > 0 ? () => setDriftOpen(true) : undefined,
              driftOff: !app ? 'no source to check against' : !drift ? 'not checked yet' : drift.unknown > 0 && drift.same + drift.changed + drift.missing === 0 ? 'the check could not read the cluster' : 'the cluster is as deployed',
            }}
          >
          <ReleaseBody
            cluster={cluster}
            now={now}
            onToast={onToast}
            route={route}
            current={row}
            d={d}
            error={details.error}
            sub={tab}
            onSub={(id) => onTab(id as AppTab)}
            before={before}
            after={after}
            above={{
              values: app ? <ValuesEditor clusterId={clusterId} row={row} app={app} canChange={canChange} onResult={onResult} /> : <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>A values file and parameters are kept for releases with a source.</p>,
              history: historyAbove,
            }}
          />
          </RevisionActions.Provider>
        )}
        {diffFor !== null && revisions && <DiffDialog clusterId={clusterId} namespace={row.namespace} name={row.name} revisions={revisions} revision={diffFor} app={app} refs={refs} onClose={() => setDiffFor(null)} />}
        {driftOpen && drift && <DriftDialog name={row.name} drift={drift} onClose={() => setDriftOpen(false)} />}
      </section>
    </main>
  )
}

/**
 * The application's values file and parameters, edited in place (HA-12,
 * HA-13) and saved by themselves a moment after the typing stops, for the
 * next deploy; Redeploy, at the top, deploys them.
 */
function ValuesEditor({ clusterId, row, app, canChange, onResult }: { clusterId: string; row: AppRow; app: AppView; canChange: boolean; onResult: (r: WatsonItem | AppView, doing: string) => void }) {
  const [values, setValues] = useState(app.values ?? '')
  const [params, setParams] = useState((app.params ?? []).join('\n'))
  const [state, setState] = useState<'saved' | 'dirty' | 'saving' | 'error'>('saved')
  const [error, setError] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const queue = useRef<Promise<void>>(Promise.resolve())
  const seq = useRef(0)
  const latest = useRef({ values, params })
  useEffect(() => {
    latest.current = { values, params }
  }, [values, params])
  // A record changed elsewhere (an upgrade, another browser) shows once
  // nothing is being edited here.
  const stateRef = useRef(state)
  useEffect(() => {
    stateRef.current = state
  }, [state])
  useEffect(() => {
    if (stateRef.current !== 'saved') return
    const v = app.values ?? ''
    const p = (app.params ?? []).join('\n')
    if (v !== latest.current.values) setValues(v)
    if (p !== latest.current.params) setParams(p)
  }, [app.values, app.params])
  const save = useCallback(() => {
    const { values: v, params: p } = latest.current
    const list = p
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith('#'))
    for (const x of list) {
      if (!x.includes('=')) {
        setState('error')
        setError(`"${x}" is not key=value`)
        return
      }
    }
    setState('saving')
    setError('')
    const mine = ++seq.current
    queue.current = queue.current
      .then(() => updateApp(clusterId, row.namespace, row.name, { values: v, params: list }))
      .then((r) => {
        // Typing since this save went out keeps the editor dirty.
        if (seq.current === mine) setState('saved')
        onResult(r, 'Values saved')
      })
      .catch((e: unknown) => {
        if (seq.current === mine) {
          setState('error')
          setError(e instanceof Error ? e.message : String(e))
        }
      })
  }, [clusterId, row.namespace, row.name, onResult])
  const touch = () => {
    setState('dirty')
    seq.current++
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(save, 1200)
  }
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])
  const status = state === 'saving' ? 'Saving…' : state === 'dirty' ? 'Unsaved; saves in a moment' : state === 'error' ? error : 'Saved'
  return (
    <Card title="Values file and parameters" aside={<span style={{ fontSize: 12, color: state === 'error' ? 'var(--err)' : 'var(--fg3)' }} role={state === 'error' ? 'alert' : undefined} data-testid="app-values-state">{status}</span>}>
      <YamlEditor
        value={values}
        onChange={({ text, errors }) => {
          setValues(text)
          if (errors.length) {
            // Not saved until it parses; the editor underlines the place.
            if (timer.current) clearTimeout(timer.current)
            seq.current++
            setState('error')
            setError(`Not saved: ${errors[0]}`)
          } else touch()
        }}
        onBlur={() => state === 'dirty' && save()}
        readOnly={!canChange}
        label="Values file"
        minHeight="min(60vh, 640px)"
        placeholder={'replicaCount: 2\nimage:\n  tag: v1.2.0'}
        testId="app-values"
      />
      <div>
        <h4 className="rk-h" style={{ margin: '0 0 6px', fontSize: 12 }}>
          Parameters
        </h4>
        <textarea
          className="rk-area rk-mono"
          value={params}
          onChange={(e) => {
            setParams(e.target.value)
            touch()
          }}
          onBlur={() => state === 'dirty' && save()}
          readOnly={!canChange}
          spellCheck={false}
          aria-label="Parameters"
          style={{ minHeight: 64, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }}
          placeholder="image.tag=v1.2.0 — one key=value per line, as helm --set takes them, applied over the values file"
          data-testid="app-params"
        />
      </div>
      <p className="rk-hint" style={{ margin: 0 }}>
        {canChange ? 'What you type here is kept with the application; Redeploy at the top deploys it, as you, after your approval in Watson. The chart\'s defaults apply underneath.' : 'What the console deploys this application with; the chart\'s defaults apply underneath.'}
      </p>
    </Card>
  )
}

/**
 * The application's source, edited in place and saved when a field is
 * left (HA-05, HA-11): the repository, the chart's path, the branch or
 * tag, a token for a private repository, and whether to make the
 * namespace at install. A release the console did not install gets its
 * record here.
 */
/** What was typed into a Source card and not saved, kept per release while the page is open (round 2 review). */
interface SourceDraft {
  repo: string
  path: string
  ref: string
  refKind: RefKind
}
const draftKey = (clusterId: string, row: AppRow) => `221b.source-draft:${clusterId}/${row.namespace}/${row.name}`
function readDraft(key: string): SourceDraft | null {
  try {
    const raw = sessionStorage.getItem(key)
    return raw ? (JSON.parse(raw) as SourceDraft) : null
  } catch {
    return null
  }
}
function writeDraft(key: string, d: SourceDraft | null) {
  try {
    if (d) sessionStorage.setItem(key, JSON.stringify(d))
    else sessionStorage.removeItem(key)
  } catch {
    // Storage off: the draft lives only in the card.
  }
}

function SourceCard({ clusterId, row, app, canChange, onResult, now }: { clusterId: string; row: AppRow; app?: AppView; canChange: boolean; onResult: (r: WatsonItem | AppView, doing: string) => void; now: number }) {
  const key = draftKey(clusterId, row)
  const [draft] = useState(() => readDraft(key))
  const [repo, setRepo] = useState(draft?.repo ?? app?.source.repo ?? '')
  const [path, setPath] = useState(draft?.path ?? app?.source.path ?? '')
  const [ref, setRef] = useState(draft?.ref ?? app?.source.ref ?? '')
  const [refKind, setRefKind] = useState<RefKind>(draft?.refKind ?? app?.source.refKind ?? 'branch')
  const [token, setToken] = useState('')
  const [createNs, setCreateNs] = useState(app?.createNamespace ?? false)
  const [state, setState] = useState<'saved' | 'dirty' | 'saving' | 'error'>(() => (draft ? 'error' : 'saved'))
  const [error, setError] = useState(() => (draft ? (draft.repo.trim() && draft.ref.trim() ? 'Not saved yet: press Save.' : 'Not saved: needs a repository and a branch or tag.') : ''))
  // The repository is looked over when its URL is typed (M-07): the chart's path and the default branch fill what is empty.
  const [look, setLook] = useState<{ repo: string; state: 'busy' | 'done' | 'error'; text: string } | null>(null)
  const commit = app?.commit ? app.commit.slice(0, 12) : ''
  const saved: AppSource | null = app ? app.source : null
  const queue = useRef<Promise<void>>(Promise.resolve())
  const seq = useRef(0)
  // The record's source shows once nothing is being edited here.
  const stateRef = useRef(state)
  useEffect(() => {
    stateRef.current = state
  }, [state])
  useEffect(() => {
    if (stateRef.current !== 'saved' || !app) return
    setRepo(app.source.repo)
    setPath(app.source.path ?? '')
    setRef(app.source.ref)
    setRefKind(app.source.refKind)
    setCreateNs(app.createNamespace ?? false)
  }, [app])
  // What is typed now, for a save that starts after a repository look-over
  // finished (the blur's closure would be stale).
  const latest = useRef({ repo, path, ref, refKind, token, createNs })
  useEffect(() => {
    latest.current = { repo, path, ref, refKind, token, createNs }
  }, [repo, path, ref, refKind, token, createNs])
  const dirty = (createNsNow: boolean, tok: string) => {
    const l = latest.current
    return !saved || l.repo.trim() !== saved.repo || l.path.trim() !== (saved.path ?? '') || l.ref.trim() !== saved.ref || l.refKind !== saved.refKind || createNsNow !== (app?.createNamespace ?? false) || tok !== ''
  }
  // pressed: Save or Enter, which reports a missing field; quiet: after a
  // look-over, which saves only what is complete.
  const save = (createNsNow = latest.current.createNs, tok = latest.current.token, pressed = false, quiet = false) => {
    if (!canChange || !dirty(createNsNow, tok)) return
    const l = latest.current
    if (!l.repo.trim() || !l.ref.trim()) {
      if (quiet) return
      // Reported, and what was typed is kept (round 2 review).
      setState('error')
      setError(pressed ? 'Not saved: needs a repository and a branch or tag.' : 'Not saved yet: needs a repository and a branch or tag.')
      return
    }
    setState('saving')
    setError('')
    const input = { source: { repo: l.repo.trim(), path: l.path.trim(), ref: l.ref.trim(), refKind: l.refKind }, token: tok || undefined, createNamespace: createNsNow }
    // One save after another: two fields left in quick succession must not
    // race; and an answer marks the card clean only when nothing was typed
    // since it went out.
    const mine = ++seq.current
    queue.current = queue.current
      .then(() => updateApp(clusterId, row.namespace, row.name, input))
      .then((r) => {
        if (seq.current === mine) {
          setState('saved')
          setToken('')
          writeDraft(key, null)
        }
        onResult(r, saved ? 'Source saved' : 'Source set')
      })
      .catch((e: unknown) => {
        if (seq.current === mine) {
          setState('error')
          setError(e instanceof Error ? e.message : String(e))
        }
      })
  }
  const edited = () => {
    seq.current++
    setState('dirty')
  }
  // Every keystroke goes to the draft, so leaving the page loses nothing.
  useEffect(() => {
    if (state === 'saved' || state === 'saving') return
    writeDraft(key, { repo, path, ref, refKind })
  }, [key, state, repo, path, ref, refKind])
  const lookOver = (): Promise<void> => {
    const url = latest.current.repo.trim()
    if (!canChange || !url || (look?.repo === url && look.state !== 'error') || (saved && url === saved.repo && latest.current.path.trim())) return Promise.resolve()
    setLook({ repo: url, state: 'busy', text: 'Looking over the repository…' })
    return inspectRepo(clusterId, url, latest.current.ref.trim(), latest.current.refKind, latest.current.token || undefined)
      .then((d) => {
        const chart = d.charts[0]
        const filled: string[] = []
        // Straight into latest too: the save chained after this look-over
        // runs before React renders the new state (round 2 review: the
        // path showed and went away).
        if (!latest.current.path.trim() && chart) {
          setPath(chart.path)
          latest.current = { ...latest.current, path: chart.path }
          filled.push(`chart path ${chart.path || '/'}`)
        }
        if (!latest.current.ref.trim()) {
          setRef(d.ref)
          setRefKind(d.refKind)
          latest.current = { ...latest.current, ref: d.ref, refKind: d.refKind }
          filled.push(`${d.refKind} ${d.ref}`)
        }
        if (filled.length) edited()
        setLook({ repo: url, state: 'done', text: d.charts.length === 0 ? `No Chart.yaml within four directories of the root at ${d.ref}.` : filled.length ? `Filled from the repository: ${filled.join(', ')}.` : `${d.charts.length === 1 ? 'Chart' : `${d.charts.length} charts; chart`} ${chart!.path || '/'} (${chart!.name} ${chart!.version}) at ${d.ref}.` })
      })
      .catch((e: unknown) => setLook({ repo: url, state: 'error', text: e instanceof Error ? e.message : String(e) }))
  }
  const field = (label: string, value: string, set: (v: string) => void, placeholder: string, testId: string, type = 'text', span = 1) => (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 0, gridColumn: span > 1 ? `span ${span}` : undefined }}>
      {label}
      <span className="rk-field">
        <input
          className="rk-mono"
          type={type}
          autoComplete="off"
          value={value}
          readOnly={!canChange}
          onChange={(e) => (set(e.target.value), edited())}
          onBlur={() => (testId === 'app-src-repo' ? void lookOver().then(() => save(undefined, undefined, false, true)) : save())}
          onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), testId === 'app-src-repo' ? void lookOver().then(() => save(undefined, undefined, true)) : save(undefined, undefined, true))}
          placeholder={placeholder}
          data-testid={testId}
        />
      </span>
    </label>
  )
  // The deploy options (R2-08, R2-09, R2-11) save on their own, at once.
  const meta = useLiveMeta()
  const fleets = (meta.settings?.fleets?.[clusterId] ?? []).filter((f) => f.deploy)
  const [inspect, setInspect] = useState<{ key: string; data?: RepoInspection; error?: string } | null>(null)
  const inspectKey = app ? `${app.source.repo}|${app.source.path ?? ''}|${app.source.ref}` : ''
  useEffect(() => {
    if (!inspectKey) return
    let live = true
    fetchAppInspect(clusterId, row.namespace, row.name)
      .then((d) => live && setInspect({ key: inspectKey, data: d }))
      .catch((e: unknown) => live && setInspect({ key: inspectKey, error: e instanceof Error ? e.message : String(e) }))
    return () => {
      live = false
    }
  }, [inspectKey, clusterId, row.namespace, row.name])
  const chartFiles = inspect?.key === inspectKey && inspect.data ? (inspect.data.charts.find((c) => c.path === (app?.source.path ?? ''))?.valuesFiles ?? inspect.data.charts[0]?.valuesFiles ?? []) : []
  const valuesFiles = app?.valuesFile && !chartFiles.includes(app.valuesFile) ? [app.valuesFile, ...chartFiles] : chartFiles
  const [optState, setOptState] = useState<'' | 'saving' | 'saved' | 'error'>('')
  const [optError, setOptError] = useState('')
  // The choice shows at once; the record's own values take over when it
  // comes back saved (or changed elsewhere).
  const optKey = app ? `${app.valuesFile ?? ''}|${app.ignoreDefaults ? 1 : 0}|${app.fleet ?? ''}` : ''
  const [local, setLocal] = useState<{ key: string; valuesFile: string; ignoreDefaults: boolean; fleet: string } | null>(null)
  const opts = local && local.key === optKey ? local : { key: optKey, valuesFile: app?.valuesFile ?? '', ignoreDefaults: !!app?.ignoreDefaults, fleet: app?.fleet ?? '' }
  const saveOption = (input: AppInput) => {
    setLocal({ ...opts, ...(input.valuesFile !== undefined ? { valuesFile: input.valuesFile } : {}), ...(input.ignoreDefaults !== undefined ? { ignoreDefaults: input.ignoreDefaults } : {}), ...(input.fleet !== undefined ? { fleet: input.fleet } : {}) })
    setOptState('saving')
    queue.current = queue.current
      .then(() => updateApp(clusterId, row.namespace, row.name, input))
      .then((r) => {
        setOptState('saved')
        onResult(r, 'Deploy options saved')
      })
      .catch((e: unknown) => {
        setLocal(null)
        setOptState('error')
        setOptError(e instanceof Error ? e.message : String(e))
      })
  }
  const status = state === 'saving' ? 'Saving…' : state === 'dirty' ? (repo.trim() && ref.trim() ? 'Not saved yet: press Save, or leave the field' : 'Not saved yet: needs a repository and a branch or tag') : state === 'error' ? error : saved ? 'Saved' : 'Not set'
  return (
    <Card
      title="Setup"
      aside={
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontSize: 12, color: state === 'error' ? 'var(--err)' : 'var(--fg3)' }} role={state === 'error' ? 'alert' : undefined} data-testid="app-source-state">
            {status}
          </span>
          {canChange && (
            <button className="rk-btn" data-on={state === 'dirty' && repo.trim() && ref.trim() ? 'true' : undefined} onClick={() => save(undefined, undefined, true)} disabled={state === 'saving' || (state !== 'dirty' && state !== 'error')} style={{ height: 26 }} data-testid="app-src-save">
              {saved ? 'Save' : 'Set source'}
            </button>
          )}
        </span>
      }
    >
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(6, minmax(0, 1fr))', gap: '8px 12px' }}>
        {field('Repository (https:// git URL)', repo, setRepo, 'https://github.com/org/app', 'app-src-repo', 'text', 4)}
        {field('Chart path (empty for the root)', path, setPath, 'charts/app', 'app-src-path', 'text', 2)}
        <label style={{ display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 0, gridColumn: 'span 3' }}>
          {refKind === 'tag' ? 'Tag' : 'Branch'}
          <span style={{ display: 'flex', gap: 6 }}>
            <span className="rk-field" style={{ flex: '1 1 auto' }}>
              <input className="rk-mono" value={ref} readOnly={!canChange} onChange={(e) => (setRef(e.target.value), edited())} onBlur={() => save()} placeholder="main, or v1.2.0" data-testid="app-src-ref" />
            </span>
            <span className="rk-seg" role="group" aria-label="Kind of ref">
              <button type="button" aria-pressed={refKind === 'branch'} disabled={!canChange} onClick={() => (setRefKind('branch'), edited())}>
                Branch
              </button>
              <button type="button" aria-pressed={refKind === 'tag'} disabled={!canChange} onClick={() => (setRefKind('tag'), edited())}>
                Tag
              </button>
            </span>
          </span>
        </label>
        {field(app?.hasToken ? 'GitHub token (one is saved; paste a new one to replace it)' : 'GitHub token (for a private repository; kept in the console, never shown again)', token, setToken, app?.hasToken ? '••••••••' : 'ghp_… or github_pat_…, optional', 'app-src-token', 'password', 3)}
      </div>
      {look && (
        <p style={{ margin: 0, fontSize: 12, color: look.state === 'error' ? 'var(--err)' : 'var(--fg2)' }} data-testid="app-src-inspect" data-state={look.state}>
          {look.text}
        </p>
      )}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 18px', fontSize: 12, color: 'var(--fg3)' }}>
        {row.orphan && (
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5, color: 'var(--fg)' }}>
            <input
              type="checkbox"
              checked={createNs}
              disabled={!canChange}
              onChange={(e) => {
                setCreateNs(e.target.checked)
                save(e.target.checked)
              }}
              data-testid="app-src-create-ns"
            />{' '}
            Create the namespace {row.namespace} at install if it does not exist
          </label>
        )}
        {app && (
          <>
            <span>
              Commit <span className="rk-mono" style={{ color: 'var(--fg)' }}>{commit ? `${commit}${app.commitSubject ? ` — ${app.commitSubject}` : ''}` : 'not deployed from here yet'}</span>
              {app.matchedRevision ? ` · renders what revision ${app.matchedRevision} runs` : app.sourceDiff ? ` · ${app.sourceDiff}` : ''}
            </span>
            {app.chartVersion && (
              <span>
                Chart at commit <span className="rk-mono" style={{ color: 'var(--fg)' }}>{app.chartVersion}</span>
              </span>
            )}
            <span title={`kept by ${app.createdBy || 'the console'}, ${fmtTs(app.created)}`}>
              Last change <span style={{ color: 'var(--fg)' }}>{app.updatedBy || '—'}, {fmtAge(app.updated, now)} ago</span>
            </span>
          </>
        )}
        {!saved && <span>Once set, the release can be upgraded from here and checked for drift.</span>}
      </div>
      {app && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-end', gap: '8px 16px', paddingTop: 8, borderTop: '1px dashed var(--line)' }} data-testid="app-deploy-options">
          <label style={{ display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 220 }}>
            Second values file (from the chart’s directory at {app.source.ref})
            <span className="rk-field">
              <select className="rk-mono" value={opts.valuesFile} disabled={!canChange} onChange={(e) => saveOption({ valuesFile: e.target.value })} data-testid="app-src-values-file">
                <option value="">none: values.yaml only</option>
                {valuesFiles.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </select>
            </span>
          </label>
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5, color: 'var(--fg)', paddingBottom: 6 }}>
            <input type="checkbox" checked={opts.ignoreDefaults} disabled={!canChange} onChange={(e) => saveOption({ ignoreDefaults: e.target.checked })} data-testid="app-src-ignore-defaults" /> Ignore the chart’s values.yaml
          </label>
          <span style={{ fontSize: 12, color: optState === 'error' ? 'var(--err)' : 'var(--fg3)', paddingBottom: 6, flex: '1 1 260px' }} role={optState === 'error' ? 'alert' : undefined} data-testid="app-deploy-options-state">
            {optState === 'saving' ? 'Saving…' : optState === 'error' ? optError : optState === 'saved' ? 'Saved. ' : ''}
            Applied lowest first: {opts.ignoreDefaults ? <s>the chart’s values.yaml</s> : 'the chart’s values.yaml'}
            {opts.valuesFile ? `, ${opts.valuesFile}` : ''}, the Values tab, then its parameters
            {app.fleetLabel ? `; the fleet adds nodeSelector ${app.fleetLabel}` : ''}.
            {inspect?.key === inspectKey && inspect.error ? ` The repository could not be read for its files: ${inspect.error}` : ''}
          </span>
        </div>
      )}
      {app && (
        // Deploy to the fleet (R2-11) on a row of its own, always shown: with
        // no fleet labelled for deploys it says so instead of vanishing
        // (asked October 30, 2026).
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-end', gap: '8px 16px', paddingTop: 8, borderTop: '1px solid var(--line)' }} data-testid="app-fleet-row">
          <label style={{ display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 220 }}>
            Deploy to the fleet
            <span className="rk-field">
              <select value={opts.fleet} disabled={!canChange || fleets.length === 0} onChange={(e) => saveOption({ fleet: e.target.value })} data-testid="app-src-fleet">
                <option value="">any node</option>
                {fleets.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.name} · {f.deploy}
                  </option>
                ))}
              </select>
            </span>
          </label>
          <span style={{ fontSize: 12, color: 'var(--fg3)', paddingBottom: 6, flex: '1 1 260px' }} data-testid="app-fleet-hint">
            {fleets.length === 0 ? 'No fleet has a label configured: give a fleet a deploy label in its editor, and it is offered here.' : app.fleetLabel ? `The deploy adds nodeSelector ${app.fleetLabel}, so the pods run on the fleet’s nodes.` : 'The fleet’s deploy label becomes the pods’ nodeSelector on the next deploy.'}
          </span>
        </div>
      )}
    </Card>
  )
}

function driftState(d: AppDrift): string {
  if (d.changed + d.missing > 0) return 'drifted'
  if (d.unknown > 0 && d.same === 0) return 'unknown'
  return 'in sync'
}
