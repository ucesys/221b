import { useMemo, type ReactNode } from 'react'
import { useView } from '../api/live'
import type { AccessRow, BindingRow, NamespaceRow, RoleRow, SARow } from '../api/types'
import { BINDING_COLUMNS, bindingTerms, GRANT_COLUMNS, grantTerms, IdentityName, ROLE_COLUMNS, roleTerms, Sev, WHO_COLUMNS, whoTerms, type BindingView, type GrantView, type RoleView, type WhoView } from '../columns/access'
import { fmtAge, nWord } from '../lib/format'
import { FixCommand } from './Watson'
import { AskSherlock } from './Sherlock'
import {
  AC_RES,
  AC_VERBS,
  acResource,
  asArgs,
  canICommand,
  canText,
  check,
  flagOf,
  grantsOf,
  identities,
  isDefault,
  isMasters,
  listText,
  review,
  reviewText,
  roleIndex,
  SEV_TEXT,
  SEV_TONE,
  splitIdentityKey,
  subjectKey,
  subjectText,
  summary,
  whoAll,
  whoCan,
  type Identity,
  type Level,
  type ReviewItem,
} from '../lib/rbac'
import { href, navigate, withParams } from '../lib/router'
import { useTable } from '../lib/useTable'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { CopyIcon, SearchIcon, ShieldIcon, WarnIcon } from './Icons'
import { RbacPanel } from './RbacPanel'
import { Select } from './Select'
import type { TabProps } from './tabs'
import { objectLink } from '../lib/peek'

type KindFilter = 'all' | 'users' | 'groups' | 'sas'
const KIND_OF: Record<KindFilter, string | null> = { all: null, users: 'User', groups: 'Group', sas: 'ServiceAccount' }
const GROUPS: [string, string][] = [
  ['User', 'Users'],
  ['Group', 'Groups'],
  ['ServiceAccount', 'Service accounts'],
]

function isRole(r: AccessRow): r is RoleRow {
  return r.key.startsWith('role:')
}
function isBinding(r: AccessRow): r is BindingRow {
  return r.key.startsWith('binding:')
}
function isSA(r: AccessRow): r is SARow {
  return r.key.startsWith('sa:')
}

function copy(text: string, onToast: (m: string) => void, what = 'Copied the command') {
  void navigator.clipboard?.writeText(text).then(
    () => onToast(what),
    () => onToast('Could not copy: the clipboard is not available.'),
  )
}

/** The Access tab (P4-03): who can do what, worked out from roles and bindings. */
export function AccessTab({ cluster, now, onToast, route }: TabProps) {
  const view = useView<AccessRow>('access')
  const nsView = useView<NamespaceRow>('namespaces')
  const p = route.params
  const set = (changes: Record<string, string | undefined>) => navigate(withParams(route, changes), true)

  const { roles, bindings, sas } = useMemo(() => {
    const roles: RoleRow[] = []
    const bindings: BindingRow[] = []
    const sas: SARow[] = []
    for (const r of view.rows) {
      if (isRole(r)) roles.push(r)
      else if (isBinding(r)) bindings.push(r)
      else if (isSA(r)) sas.push(r)
    }
    return { roles, bindings, sas }
  }, [view.rows])
  const roleIdx = useMemo(() => roleIndex(roles), [roles])
  const items = useMemo(() => review(bindings), [bindings])
  const ids = useMemo(() => identities(bindings, sas), [bindings, sas])
  const idByKey = useMemo(() => new Map(ids.map((i) => [i.key, i])), [ids])
  const namespaces = useMemo(() => {
    const set = new Set<string>(nsView.rows.map((n) => n.name))
    for (const b of bindings) if (b.namespace) set.add(b.namespace)
    return [...set].sort()
  }, [nsView.rows, bindings])

  const sel = p.get('id') ?? ''
  const selId = sel ? (idByKey.get(sel) ?? identityFromKey(sel)) : null
  const showSys = p.get('sys') === '1' || !!selId?.system
  const open = p.get('open') ?? ''

  return (
    <>
      <Aside ids={ids} items={items} sel={sel} showSys={showSys} route={route} set={set} loaded={view.loaded} />
      <section className="rk-pane" aria-label="Access Control" style={{ flex: '999 1 640px', minWidth: 0, padding: '20px 24px 32px', display: 'flex', flexDirection: 'column', gap: 16 }} data-testid="access-main">
        {selId ? (
          <IdentityPage id={selId} bindings={bindings} roles={roleIdx} items={items} namespaces={namespaces} now={now} route={route} set={set} onToast={onToast} cluster={cluster?.name} />
        ) : (
          <Overview roles={roles} bindings={bindings} ids={ids} items={items} roleIdx={roleIdx} namespaces={namespaces} now={now} route={route} set={set} onToast={onToast} cluster={cluster?.name} clusterName={cluster?.name ?? 'the cluster'} loaded={view.loaded} />
        )}
      </section>
      {open && <RbacPanel keyOf={open} roles={roles} bindings={bindings} sas={sas} now={now} onClose={() => set({ open: undefined })} onOpen={(k) => set({ open: k })} onToast={onToast} />}
    </>
  )
}

/** An identity no row names any more, from its key, so a link to it still opens. */
function identityFromKey(key: string): Identity | null {
  const s = splitIdentityKey(key)
  if (!s) return null
  return { key, kind: s.kind, name: s.name, namespace: s.namespace, exists: s.kind !== 'ServiceAccount', system: s.name.startsWith('system:'), bindings: [] }
}

const SEV_RANK: Record<string, number> = { high: 0, medium: 1, low: 2 }

function Aside({ ids, items, sel, showSys, route, set, loaded }: { ids: Identity[]; items: ReviewItem[]; sel: string; showSys: boolean; route: TabProps['route']; set: (c: Record<string, string | undefined>) => void; loaded: boolean }) {
  const p = route.params
  const query = p.get('q') ?? ''
  const kind = (['all', 'users', 'groups', 'sas'].includes(p.get('kind') ?? '') ? p.get('kind') : 'all') as KindFilter
  const sysCount = ids.filter((i) => i.system).length
  const pool = showSys ? ids : ids.filter((i) => !i.system || i.key === sel)
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const matches = pool.filter((i) => {
    const hay = `${i.name} ${i.namespace ?? ''} ${i.kind} ${summary(i)}`.toLowerCase()
    return words.every((w) => hay.includes(w))
  })
  const count = (k: string | null) => matches.filter((i) => !k || i.kind === k).length
  const shown = matches.filter((i) => !KIND_OF[kind] || i.kind === KIND_OF[kind])
  const rank = (i: Identity) => {
    const f = flagOf(i.key, items)
    if (f) return SEV_RANK[f.severity] ?? 9
    return !i.exists ? 1.5 : 9
  }
  const linkTo = (key: string) => withParams(route, { id: key || undefined, scope: undefined, open: undefined })
  return (
    <aside className="rk-pane" aria-label="Identities" data-testid="access-aside" style={{ flex: '1 1 320px', maxWidth: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)' }}>
      <div style={{ padding: '14px 16px 12px', display: 'flex', flexDirection: 'column', gap: 10, borderBottom: '1px solid var(--line)' }}>
        <h2 className="rk-h">
          Identities <span style={{ fontWeight: 500, letterSpacing: 0 }}>{pool.length}</span>
        </h2>
        <label className="rk-field">
          <SearchIcon />
          <span className="rk-sr">Find a user, group or service account</span>
          <input type="search" placeholder="Find a user, group or service account" value={query} onChange={(e) => set({ q: e.target.value || undefined })} data-testid="access-search" />
        </label>
        <div className="rk-seg" role="group" aria-label="Kind of identity" style={{ flexWrap: 'wrap' }}>
          {(
            [
              ['all', 'All', null],
              ['users', 'Users', 'User'],
              ['groups', 'Groups', 'Group'],
              ['sas', 'Service accounts', 'ServiceAccount'],
            ] as [KindFilter, string, string | null][]
          ).map(([k, label, kk]) => (
            <button key={k} aria-pressed={kind === k} onClick={() => set({ kind: k === 'all' ? undefined : k })} data-kind={k}>
              {label} <span className="rk-cnt">{count(kk)}</span>
            </button>
          ))}
        </div>
        <label className="rk-check" title="Kubernetes’ own components: system: users and groups, and the controllers’ service accounts in kube-system" style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
          <input type="checkbox" checked={showSys} onChange={(e) => set({ sys: e.target.checked ? '1' : undefined })} data-testid="access-sys" />
          Show Kubernetes system identities <span className="rk-cnt">{sysCount}</span>
        </label>
      </div>
      <div style={{ flex: '1 1 auto' }}>
        <a className="rk-rrow" href={linkTo('')} data-sel={!sel} aria-current={!sel ? 'page' : undefined} style={{ display: 'flex', flexDirection: 'column', gap: 3, padding: '11px 16px', textDecoration: 'none', color: 'inherit' }} data-testid="access-overview-link">
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, fontWeight: 600 }}>
            <ShieldIcon />
            Review and tables
          </span>
          <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{loaded ? reviewText(items) : 'Reading roles and bindings…'}</span>
        </a>
        {GROUPS.map(([k, title]) => {
          const rows = shown.filter((i) => i.kind === k).sort((a, b) => rank(a) - rank(b) || (a.namespace ?? '').localeCompare(b.namespace ?? '') || a.name.localeCompare(b.name))
          if (!rows.length) return null
          return (
            <div key={k}>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, padding: '14px 16px 6px', borderTop: '1px solid var(--line)' }}>
                <h3 className="rk-h">{title}</h3>
                <span style={{ fontSize: 11, color: 'var(--fg3)' }}>{rows.length}</span>
              </div>
              {rows.map((i) => {
                const f = flagOf(i.key, items)
                return (
                  <a key={i.key} className="rk-rrow" href={linkTo(i.key)} data-sel={sel === i.key} aria-current={sel === i.key ? 'page' : undefined} data-identity={i.key} style={{ display: 'flex', flexDirection: 'column', gap: 3, padding: '9px 16px', borderTop: '1px solid var(--line)', textDecoration: 'none', color: 'inherit' }}>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                      <span className="rk-mono" style={{ fontSize: 12.5, fontWeight: 500, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {i.kind === 'ServiceAccount' && <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{i.namespace}/</span>}
                        {i.name}
                      </span>
                      <span style={{ flex: '1 1 auto' }} />
                      {f ? (
                        <Sev f={f} />
                      ) : !i.exists ? (
                        <span className="rk-t" data-tone="warn" title="This ServiceAccount doesn’t exist" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11.5, fontWeight: 500 }}>
                          <span className="rk-dot" />
                          missing
                        </span>
                      ) : null}
                    </span>
                    <span style={{ fontSize: 11.5, color: 'var(--fg3)', display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>{summary(i)}</span>
                  </a>
                )
              })}
            </div>
          )
        })}
        {loaded && !shown.length && <p style={{ margin: 0, padding: '24px 16px', fontSize: 12.5, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>No identity matches.</p>}
      </div>
      <p style={{ margin: 'auto 0 0', padding: '10px 16px 14px', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>Kubernetes keeps no list of users or groups. They appear here because a binding names them; the identity provider decides who belongs to a group.</p>
    </aside>
  )
}

interface OverviewProps {
  roles: RoleRow[]
  bindings: BindingRow[]
  ids: Identity[]
  items: ReviewItem[]
  roleIdx: ReturnType<typeof roleIndex>
  namespaces: string[]
  now: number
  route: TabProps['route']
  set: (c: Record<string, string | undefined>) => void
  onToast: (m: string) => void
  cluster?: string
  clusterName: string
  loaded: boolean
}

function Overview({ roles, bindings, ids, items, roleIdx, namespaces, now, route, set, onToast, cluster, loaded }: OverviewProps) {
  return (
    <>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
          <h1 style={{ margin: 0, fontSize: 19, fontWeight: 600, letterSpacing: '-.01em' }}>Access Control</h1>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="access-counts">
            {nWord(bindings.length, 'binding', 'bindings')} · {nWord(roles.length, 'role', 'roles')} · {nWord(ids.length, 'identity', 'identities')}
          </span>
        </div>
      </div>
      <ReviewCard items={items} onToast={onToast} open={(k) => set({ open: k })} loaded={loaded} />
      <WhoCanCard bindings={bindings} ids={ids} items={items} roleIdx={roleIdx} namespaces={namespaces} now={now} route={route} set={set} onToast={onToast} cluster={cluster} />
      <TablesCard roles={roles} bindings={bindings} items={items} now={now} route={route} set={set} onToast={onToast} cluster={cluster} />
    </>
  )
}

function FindingBlock({ f, path, onToast, card = false }: { f: ReviewItem; path?: ReactNode; onToast: (m: string) => void; card?: boolean }) {
  return (
    <div className={'rk-find' + (card ? ' rk-card' : '')} data-sev={f.severity} data-testid="finding">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone={SEV_TONE[f.severity]}>
          <span className="rk-dot" />
          {SEV_TEXT[f.severity]}
        </span>
        <strong style={{ fontSize: 13, fontWeight: 600 }}>{f.title}</strong>
      </div>
      <p style={{ margin: 0, maxWidth: 880, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg2)' }}>{f.text}</p>
      {path}
      {f.command && <FixCommand command={f.command} from={`Access finding: ${f.title}`} why={f.text} onCopy={() => copy(f.command!, onToast)} />}
    </div>
  )
}

function ReviewCard({ items, onToast, open, loaded }: { items: ReviewItem[]; onToast: (m: string) => void; open: (key: string) => void; loaded: boolean }) {
  return (
    <section className="rk-card" aria-label="Review" style={{ overflow: 'hidden', flex: 'none' }} data-testid="access-review">
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
        <h2 className="rk-h">Review</h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{loaded ? reviewText(items) : ''}</span>
      </div>
      {items.map((f, i) => (
        <FindingBlock
          key={f.binding.key + f.title + i}
          f={f}
          onToast={onToast}
          path={
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
              <button className="rk-chip rk-chipbtn" onClick={() => open(f.binding.key)} title="Open the binding">
                {f.binding.kind} {f.binding.namespace ? f.binding.namespace + '/' : ''}
                {f.binding.name}
              </button>
              <span style={{ color: 'var(--fg3)' }}>→</span>
              <button className="rk-chip rk-chipbtn" onClick={() => open(f.binding.roleKind === 'ClusterRole' ? `role:ClusterRole/${f.binding.roleName}` : `role:Role/${f.binding.namespace}/${f.binding.roleName}`)} title="Open the role">
                {f.binding.roleKind} {f.binding.roleName}
              </button>
              {f.subject && (
                <>
                  <span style={{ color: 'var(--fg3)' }}>→</span>
                  <a className="rk-chip rk-chipbtn" href={href('access', [], { id: subjectKey(f.subject) })} style={{ textDecoration: 'none' }}>
                    {subjectText(f.subject)}
                  </a>
                </>
              )}
            </div>
          }
        />
      ))}
      {loaded && !items.length && <p style={{ margin: 0, padding: '14px 16px', fontSize: 12.5, color: 'var(--fg3)' }}>Nothing to review: no broad, stale or personal access was found.</p>}
    </section>
  )
}

function WhoCanCard({ bindings, ids, items, roleIdx, namespaces, now, route, set, onToast, cluster }: { bindings: BindingRow[]; ids: Identity[]; items: ReviewItem[]; roleIdx: ReturnType<typeof roleIndex>; namespaces: string[]; now: number; route: TabProps['route']; set: (c: Record<string, string | undefined>) => void; onToast: (m: string) => void; cluster?: string }) {
  const p = route.params
  const verb = (AC_VERBS as readonly string[]).includes(p.get('verb') ?? '') ? p.get('verb')! : 'get'
  const res = AC_RES.some((r) => r.res === p.get('res')) ? p.get('res')! : 'secrets'
  const r = acResource(res)
  const nsSel = p.get('wns') ?? '*'
  const ns = r.cluster ? '*' : nsSel
  const showSys = p.get('sys') === '1'
  const sysOf = useMemo(() => new Map(ids.map((i) => [i.key, i.system])), [ids])
  const all = useMemo(() => whoCan(verb, res, ns, bindings, roleIdx, (k) => sysOf.get(k) ?? k.split(':')[1]!.startsWith('system:')), [verb, res, ns, bindings, roleIdx, sysOf])
  const rows = useMemo<WhoView[]>(() => all.filter((w) => showSys || !w.system).map((w) => ({ ...w, flag: flagOf(w.key, items), cluster: r.cluster, ns })), [all, showSys, items, r.cluster, ns])
  const can = rows.filter(whoAll)
  const hidden = all.filter((w) => w.system && !showSys).length
  const table = useTable<WhoView>({ id: 'access-who', columns: WHO_COLUMNS, rows, terms: whoTerms, now, defaultSort: { col: 'where', dir: 'asc' } })
  const where = r.cluster ? '' : ns === '*' ? ' in at least one namespace' : ` in ${ns}`
  return (
    <section className="rk-card" aria-label="Who can" style={{ overflow: 'hidden', flex: 'none' }} data-testid="access-who">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10, padding: '12px 16px', borderBottom: rows.length ? '1px solid var(--line)' : 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <h2 className="rk-h">Who can</h2>
          <Select label="Verb" value={verb} options={AC_VERBS.map((v) => [v, v])} onChange={(v) => set({ verb: v === 'get' ? undefined : v })} testId="who-verb" />
          <Select label="Resource" value={res} options={AC_RES.map((x) => [x.res, x.res])} onChange={(v) => set({ res: v === 'secrets' ? undefined : v })} testId="who-res" />
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>in</span>
          <Select label="Namespace" value={nsSel} disabled={r.cluster} options={[['*', 'any namespace'], ...namespaces.map((n): [string, string] => [n, n])]} onChange={(v) => set({ wns: v === '*' ? undefined : v })} testId="who-ns" />
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>?</span>
          <span style={{ marginLeft: 'auto' }}>
            <ExportMenu onExport={(f) => table.exportRows(f, cluster, `who-can-${verb}-${res.replace('/', '-')}`, onToast)} disabled={!rows.length} what="these results" />
          </span>
        </div>
        <p style={{ margin: 0, fontSize: 12.5, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'baseline' }} data-testid="who-answer">
          <strong style={{ fontWeight: 600 }}>{can.length ? `${canText(can.length)} ${verb} ${res}${where}` : `Nobody${showSys ? '' : ' outside Kubernetes’ own components'} can ${verb} ${res}${where}.`}</strong>
          {rows.length > can.length && <span style={{ color: 'var(--fg3)' }}>· {nWord(rows.length - can.length, 'more only for objects it names', 'more only for objects they name')}</span>}
          {hidden > 0 && (
            <span style={{ color: 'var(--fg3)' }}>
              and {nWord(hidden, 'Kubernetes system identity', 'Kubernetes system identities')}{' '}
              <button className="rk-link" onClick={() => set({ sys: '1' })} data-testid="who-show-sys">
                Show them
              </button>
            </span>
          )}
          {r.cluster && <span style={{ color: 'var(--fg3)' }}>· {res} are cluster-scoped, so only ClusterRoleBindings count</span>}
        </p>
      </div>
      {rows.length > 0 && <DataTable<WhoView> label={`Who can ${verb} ${res}`} rows={table.shown} cols={table.cols} rowKey={(x) => x.key} rowLabel={(x) => `${x.name}: ${x.wide ? 'everywhere' : x.namespaces.join(', ') || 'named objects'}`} sort={table.sort} onSort={table.onSort} onOpen={(k) => navigate(href('access', [], { id: k }))} now={now} empty="" rowAttr="who" />}
    </section>
  )
}

function TablesCard({ roles, bindings, items, now, route, set, onToast, cluster }: { roles: RoleRow[]; bindings: BindingRow[]; items: ReviewItem[]; now: number; route: TabProps['route']; set: (c: Record<string, string | undefined>) => void; onToast: (m: string) => void; cluster?: string }) {
  const p = route.params
  const tbl = p.get('tbl') === 'roles' ? 'roles' : 'bindings'
  const withDefaults = p.get('defaults') === '1'
  const tq = p.get('tq') ?? ''
  const always = new Set(['cluster-admin', 'admin', 'edit', 'view'])
  const bindingRows = useMemo<BindingView[]>(() => bindings.map((b) => ({ ...b, risk: items.find((f) => f.binding.key === b.key && f.severity !== 'info') })), [bindings, items])
  const bindingCount = useMemo(() => {
    const m = new Map<string, number>()
    for (const b of bindings) {
      const k = b.roleKind === 'ClusterRole' ? `ClusterRole/${b.roleName}` : `Role/${b.namespace}/${b.roleName}`
      m.set(k, (m.get(k) ?? 0) + 1)
    }
    return m
  }, [bindings])
  const roleRows = useMemo<RoleView[]>(() => roles.map((r) => ({ ...r, bindings: bindingCount.get(r.kind === 'ClusterRole' ? `ClusterRole/${r.name}` : `Role/${r.namespace}/${r.name}`) ?? 0 })), [roles, bindingCount])
  const shownBindings = withDefaults ? bindingRows : bindingRows.filter((b) => !isDefault(b.source))
  const shownRoles = withDefaults ? roleRows : roleRows.filter((r) => !isDefault(r.source) || (r.kind === 'ClusterRole' && always.has(r.name)))
  const bt = useTable<BindingView>({ id: 'access-bindings', columns: BINDING_COLUMNS, rows: shownBindings, terms: bindingTerms, now, defaultSort: { col: 'risk', dir: 'asc' }, query: tq, onQuery: (q) => set({ tq: q || undefined }) })
  const rt = useTable<RoleView>({ id: 'access-roles', columns: ROLE_COLUMNS, rows: shownRoles, terms: roleTerms, now, defaultSort: { col: 'notable', dir: 'asc' }, query: tq, onQuery: (q) => set({ tq: q || undefined }) })
  const hiddenDefaults = tbl === 'bindings' ? bindingRows.length - bindingRows.filter((b) => !isDefault(b.source)).length : roleRows.length - roleRows.filter((r) => !isDefault(r.source) || (r.kind === 'ClusterRole' && always.has(r.name))).length
  return (
    <section className="rk-card" aria-label="Bindings and roles" style={{ overflow: 'hidden', flex: 'none' }} data-testid="access-tables">
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
        <div className="rk-seg" role="group" aria-label="Table">
          <button aria-pressed={tbl === 'bindings'} onClick={() => set({ tbl: undefined })} data-testid="tbl-bindings">
            Bindings <span className="rk-cnt">{shownBindings.length}</span>
          </button>
          <button aria-pressed={tbl === 'roles'} onClick={() => set({ tbl: 'roles' })} data-testid="tbl-roles">
            Roles <span className="rk-cnt">{shownRoles.length}</span>
          </button>
        </div>
        <label className="rk-field" style={{ width: 240 }}>
          <SearchIcon />
          <span className="rk-sr">Filter by name, role or subject</span>
          <input type="search" placeholder="Filter by name, role or subject" value={tq} onChange={(e) => set({ tq: e.target.value || undefined })} data-testid="tbl-filter" />
        </label>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
          <input type="checkbox" checked={withDefaults} onChange={(e) => set({ defaults: e.target.checked ? '1' : undefined })} data-testid="tbl-defaults" />
          Kubernetes and kubeadm defaults ({withDefaults ? 'shown' : hiddenDefaults})
        </label>
        <span style={{ marginLeft: 'auto' }}>
          {tbl === 'bindings' ? (
            <ExportMenu onExport={(f) => bt.exportRows(f, cluster, 'rbac-bindings', onToast)} disabled={!bt.shown.length} what="the bindings in view" />
          ) : (
            <ExportMenu onExport={(f) => rt.exportRows(f, cluster, 'rbac-roles', onToast)} disabled={!rt.shown.length} what="the roles in view" />
          )}
        </span>
      </div>
      {tbl === 'bindings' ? (
        <DataTable<BindingView> label="Bindings" rows={bt.shown} cols={bt.cols} rowKey={(r) => r.key} rowLabel={(r) => `${r.kind} ${r.name} — open details`} sort={bt.sort} onSort={bt.onSort} onOpen={(k) => set({ open: k })} now={now} empty={tq ? `Nothing matches “${tq}”.` : 'No bindings.'} rowAttr="binding" />
      ) : (
        <DataTable<RoleView> label="Roles" rows={rt.shown} cols={rt.cols} rowKey={(r) => r.key} rowLabel={(r) => `${r.kind} ${r.name} — open details`} sort={rt.sort} onSort={rt.onSort} onOpen={(k) => set({ open: k })} now={now} empty={tq ? `Nothing matches “${tq}”.` : 'No roles.'} rowAttr="role" />
      )}
    </section>
  )
}

function cellText(level: Level, c: ReturnType<typeof check>, scope: string, cluster: boolean): string {
  switch (level) {
    case 'none':
      return 'not allowed'
    case 'named':
      return `only ${c.names.join(', ')}`
    case 'some':
      return `in ${listText(c.namespaces)}`
  }
  return cluster ? 'cluster-wide' : scope === '*' ? 'in every namespace' : `in ${scope}`
}

function IdentityPage({ id, bindings, roles, items, namespaces, now, route, set, onToast, cluster }: { id: Identity; bindings: BindingRow[]; roles: ReturnType<typeof roleIndex>; items: ReviewItem[]; namespaces: string[]; now: number; route: TabProps['route']; set: (c: Record<string, string | undefined>) => void; onToast: (m: string) => void; cluster?: string }) {
  const scope = route.params.get('scope') ?? '*'
  const grants = useMemo(() => grantsOf(id, bindings), [id, bindings])
  const masters = isMasters(id)
  const findings = items.filter((f) => f.subject && subjectKey(f.subject) === id.key)
  const everything = masters || grants.some((g) => g.binding.kind === 'ClusterRoleBinding' && roles.get(`ClusterRole/${g.binding.roleName}`)?.powers.includes('everything'))
  const text = everything ? 'Can do everything, in every namespace.' : id.bindings.length ? `Has ${summary(id)}.` : id.kind === 'ServiceAccount' ? 'No binding names it, so it has only what every service account has: API discovery and asking what it may do.' : 'No binding names it.'
  const grantRows = useMemo<GrantView[]>(() => grants.map((g) => ({ ...g, key: g.binding.key + (g.via ?? ''), created: g.binding.created })), [grants])
  const gt = useTable<GrantView>({ id: 'access-grants', columns: GRANT_COLUMNS, rows: grantRows, terms: grantTerms, now, defaultSort: { col: 'how', dir: 'asc' } })
  const sections = [...new Set(AC_RES.map((r) => r.section))]
  const listCmd = `kubectl auth can-i --list ${asArgs(id)}${scope !== '*' ? ` -n ${scope}` : ''}`
  return (
    <>
      <a className="rk-link" href={withParams(route, { id: undefined, scope: undefined, open: undefined })} style={{ fontSize: 12.5, textDecoration: 'none' }} data-testid="access-back">
        ← Review and tables
      </a>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <span className="rk-tag">{id.kind}</span>
        <h1 style={{ margin: 0, fontSize: 19, fontWeight: 500 }} data-testid="identity-name">
          <IdentityName kind={id.kind} name={id.name} namespace={id.namespace} size={19} />
        </h1>
        {id.system && <span className="rk-tag" data-tone="info">Kubernetes system identity</span>}
        <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 8 }}>
          <AskSherlock question={`What can ${id.kind} ${id.namespace ? id.namespace + '/' : ''}${id.name} do?`} />
          <button className="rk-btn" onClick={() => copy(listCmd, onToast)} title={listCmd}>
            <CopyIcon />
            Copy kubectl auth can-i --list
          </button>
        </span>
      </div>
      <p style={{ margin: 0, fontSize: 13, color: 'var(--fg2)' }} data-testid="identity-summary">
        {text}
      </p>
      {id.kind === 'Group' && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)', maxWidth: 860 }}>Kubernetes doesn’t keep group members. The identity provider decides who is in {id.name}; anyone whose token carries the group gets these permissions.{masters ? ' Members of system:masters are allowed everything before RBAC is asked.' : ''}</p>}
      {!id.exists && (
        <div className="rk-callout" data-tone="warn" data-testid="identity-missing">
          <WarnIcon />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
            <strong style={{ fontSize: 12.5, fontWeight: 600 }}>This ServiceAccount doesn’t exist</strong>
            <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>Bindings still name it, so whoever creates a ServiceAccount with this name gets the permissions below.</span>
          </div>
        </div>
      )}
      {findings.map((f, i) => (
        <FindingBlock key={f.binding.key + i} f={f} onToast={onToast} card />
      ))}
      {id.sa?.tokens.map((t) => (
        <div key={t.name} className="rk-card" style={{ padding: '12px 16px', display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5 }} data-testid="identity-token">
          <span className="rk-tag" data-tone="warn">
            Long-lived token
          </span>
          <span>
            Secret{' '}
            <a className="rk-link rk-mono" {...objectLink('namespaces', [id.namespace!], { open: `Secret/${t.name}` })}>
              {t.name}
            </a>{' '}
            holds a token for it that never expires{t.created ? `; created ${fmtAge(t.created, now)} ago` : ''}.
          </span>
        </div>
      ))}
      {id.sa && id.sa.pods.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }} data-testid="identity-pods">
          <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Pods using it</span>
          {id.sa.pods.map((pod) => (
            <a key={pod.name} className="rk-chip" {...objectLink('pods', [], { open: `${id.namespace}/${pod.name}` })} style={{ textDecoration: 'none' }} title={pod.status}>
              <span className="rk-t" data-tone={pod.tone}>
                <span className="rk-dot" />
              </span>
              {pod.name}
            </a>
          ))}
        </div>
      )}
      <section className="rk-card" aria-label="Effective permissions" style={{ overflow: 'hidden', flex: 'none' }} data-testid="identity-matrix">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
          <h2 className="rk-h">Effective permissions</h2>
          <Select label="Where" value={scope} options={[['*', 'All namespaces'], ...namespaces.map((n): [string, string] => [n, n])]} onChange={(v) => set({ scope: v === '*' ? undefined : v })} testId="matrix-scope" />
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
            <span className="rk-pm" data-lvl="all" /> everywhere in scope
          </span>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
            <span className="rk-pm" data-lvl="some" /> in some namespaces
          </span>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
            <span className="rk-pm" data-lvl="named" /> only named objects
          </span>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <div className="rk-pmx" role="table" aria-label={`Permissions of ${id.name}`}>
            <div className="rk-pmr rk-pmh" role="row">
              <span role="columnheader">Resource</span>
              {AC_VERBS.map((v) => (
                <span key={v} role="columnheader">
                  {v}
                </span>
              ))}
            </div>
            {sections.map((sec) => (
              <div key={sec} role="rowgroup">
                <div className="rk-pmsec" role="row">
                  <span role="cell">{sec}</span>
                </div>
                {AC_RES.filter((r) => r.section === sec).map((r) => (
                  <div key={r.res} className="rk-pmr" role="row">
                    <span role="rowheader" style={{ gap: 8 }}>
                      <span style={{ fontWeight: 500 }}>{r.label}</span>
                      <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
                        {r.res}
                      </span>
                    </span>
                    {AC_VERBS.map((v) => {
                      const c = masters ? { level: 'all' as Level, namespaces: [], names: [], via: [] } : check(grants, v, r.res, scope, roles)
                      const t = cellText(c.level, c, scope, r.cluster)
                      const tip = `${v} ${r.res}: ${t}` + c.via.map((x) => `\nthrough ${x.binding.namespace ? x.binding.namespace + '/' : ''}${x.binding.name} → ${x.role}${x.group ? ` (as ${x.group})` : ''}${x.named ? ` (only ${x.named.join(', ')})` : ''}`).join('') + (c.level !== 'none' ? `\n${canICommand(id, v, r.res, scope)}` : '')
                      return (
                        <span key={v} role="cell" title={tip} aria-label={`${v} ${r.res}: ${t}`} data-verb={v} data-res={r.res} data-level={c.level}>
                          <span className="rk-pm" data-lvl={c.level} />
                        </span>
                      )
                    })}
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      </section>
      <section className="rk-card" aria-label="Granted by" style={{ overflow: 'hidden', flex: 'none' }} data-testid="identity-grants">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
          <h2 className="rk-h">Granted by</h2>
          <span style={{ marginLeft: 'auto' }}>
            <ExportMenu onExport={(f) => gt.exportRows(f, cluster, `bindings-of-${id.name.replace(/[^A-Za-z0-9.-]+/g, '-')}`, onToast)} disabled={!gt.shown.length} what="these bindings" />
          </span>
        </div>
        <DataTable<GrantView> label={`Bindings of ${id.name}`} rows={gt.shown} cols={gt.cols} rowKey={(r) => r.key} rowLabel={(r) => `${r.binding.kind} ${r.binding.name} — open details`} sort={gt.sort} onSort={gt.onSort} onOpen={(k) => set({ open: grantRows.find((g) => g.key === k)?.binding.key })} now={now} empty={masters ? 'No binding is needed: RBAC is not asked about system:masters.' : 'No binding applies to it.'} rowAttr="grant" />
      </section>
    </>
  )
}


