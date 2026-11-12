import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { fetchAutomation, fetchFleets, fetchRuns, type Group, type AutomationIndex, type Run } from '../api/automation'
import type { Me } from '../api/types'
import { href, navigate, type Tab } from '../lib/router'
import { GroupDrawer } from './automation/GroupDrawer'
import { RunProfilesSection } from './automation/RunProfilesSection'
import { VariablesSection } from './automation/VariablesSection'
import type { TabProps } from './tabs'
import { errText, Pill } from './automation/parts'
import { ScriptGroups, ScriptsSection } from './automation/ScriptsSection'
import { ScriptDrawer } from './automation/ScriptDrawer'
import { RunDialog } from './automation/RunDialog'
import { RunDrawer, RunsSection } from './automation/RunsSection'
export { VersionPane } from './automation/ScriptDrawer'
export { tally } from './automation/RunsSection'

/**
 * The Automation tab (Phase 11 part 2, AU-01..AU-14): scripts, their
 * runs and the fleets' variables, for a SuperAdmin.
 */
export function AutomationTab({ route, onToast, me, now }: TabProps & { me: Me | null }) {
  const section: Tab = route.tab
  const [index, setIndex] = useState<AutomationIndex | null>(null)
  const [runs, setRuns] = useState<Run[]>([])
  const [err, setErr] = useState('')
  const [editing, setEditing] = useState<string | null | undefined>(undefined) // a name, null for a new script
  const [running, setRunning] = useState<{
    script: string
    dryRun: boolean
  } | null>(null)
  const [openRun, setOpenRun] = useState<number | null>(null)
  const [editingGroup, setEditingGroup] = useState<Group | null | undefined>(undefined)
  // A new group asked for from the script drawer goes back into its box.
  const afterGroup = useRef<((name: string) => void) | null>(null)
  // Until the cluster is known, only approved versions are offered.
  const [production, setProduction] = useState(true)
  const allowed = me?.admin === true
  // A link can open a script, on a version to review: ?script=x&version=2.
  const linked = section === 'scripts' ? route.params.get('script') : null
  const linkedVersion = Number(route.params.get('version')) || undefined
  const scriptOpen = editing !== undefined ? editing : (linked ?? undefined)
  const closeScript = () => {
    setEditing(undefined)
    if (linked) navigate(href('scripts'), true)
  }
  useEffect(() => {
    if (!allowed) return
    let gone = false
    fetchFleets(route.cluster)
      .then((f) => !gone && setProduction(f.production))
      .catch(() => {})
    return () => {
      gone = true
    }
  }, [route.cluster, allowed])

  const fetchAll = useCallback(
    (background: boolean) =>
      Promise.all([fetchAutomation(background), fetchRuns(background)]).then(
        ([ix, rs]) => ({ ix, rs, err: '' }),
        (e: unknown) => ({ ix: null, rs: null, err: errText(e) }),
      ),
    [],
  )
  const apply = useCallback((x: { ix: AutomationIndex | null; rs: Run[] | null; err: string }) => {
    if (x.ix) setIndex(x.ix)
    if (x.rs) setRuns(x.rs)
    setErr(x.err)
  }, [])
  const load = useCallback((background = false) => fetchAll(background).then(apply), [fetchAll, apply])
  useEffect(() => {
    if (!allowed) return
    let gone = false
    void fetchAll(false).then((x) => !gone && apply(x))
    return () => {
      gone = true
    }
  }, [fetchAll, apply, allowed])
  // While a run goes on, the lists follow it.
  const live = (index?.running ?? 0) > 0
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => void load(true), 4000)
    return () => clearInterval(t)
  }, [live, load])

  // The page beside the side pane; Parameters puts its own pane beside it.
  const frame = (body: ReactNode) => (
    <section
      className="rk-pane"
      style={{
        flex: '1 1 600px',
        minWidth: 0,
        display: 'flex',
        flexDirection: 'column',
        gap: 14,
        padding: '16px 20px 32px',
        background: 'var(--bg)',
      }}
      aria-label="Automation"
    >
      {index?.off && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="automation-off">
          {index.off} Scripts can be written and approved; nothing runs until SSH is on.
        </p>
      )}
      {section === 'run-history' && live && (
        <span>
          <Pill tone="info" testId="automation-live">
            {index?.running} running
          </Pill>
        </span>
      )}
      {err && (
        <p className="tb-err" data-testid="automation-error">
          {err}
        </p>
      )}
      {!index && !err && <p className="tb-hint">Loading…</p>}
      {index && section === 'scripts' && <ScriptsSection index={index} route={route} production={production} now={now} onEdit={setEditing} onRun={(script, dryRun) => setRunning({ script, dryRun })} onOpenRun={setOpenRun} />}
      {index && section === 'run-history' && <RunsSection runs={runs} now={now} onOpen={setOpenRun} />}
      {body}
      {index && section === 'run-profiles' && <RunProfilesSection cluster={route.cluster} onToast={onToast} />}
    </section>
  )

  if (me && !me.admin)
    return (
      <div style={{ flex: '1 1 auto', padding: 24 }} data-testid="automation-denied">
        <p className="tb-hint">Only a SuperAdmin may use Automation.</p>
      </div>
    )

  return (
    <>
      {index && section === 'scripts' && <ScriptGroups scripts={index.scripts} kept={index.groups ?? []} route={route} onEdit={setEditingGroup} />}
      {index && section === 'variables' ? <VariablesSection cluster={route.cluster} scripts={index.scripts} linkedFleet={route.params.get('fleet')} now={now} frame={frame} onToast={onToast} /> : frame(null)}
      {scriptOpen !== undefined && index && (
        <ScriptDrawer
          name={scriptOpen}
          linkedVersion={linked ? linkedVersion : undefined}
          me={index.me}
          production={production}
          maxText={index.limits.text}
          groups={(index.groups ?? []).map((g) => g.name)}
          onClose={closeScript}
          onChanged={(name) => {
            if (name !== undefined) setEditing(name)
            void load(true)
          }}
          onDeleted={() => {
            closeScript()
            onToast('The script is deleted.')
            void load(true)
          }}
          onRun={(script, dryRun) => setRunning({ script, dryRun })}
          onNewGroup={(done) => {
            afterGroup.current = done
            setEditingGroup(null)
          }}
          onToast={onToast}
        />
      )}
      {editingGroup !== undefined && index && (
        <GroupDrawer
          group={editingGroup}
          scripts={index.scripts}
          onClose={() => {
            afterGroup.current = null
            setEditingGroup(undefined)
          }}
          onSaved={(name) => {
            afterGroup.current?.(name)
            afterGroup.current = null
            setEditingGroup(undefined)
            onToast(`${name} is saved.`)
            void load(true)
          }}
          onDeleted={(name) => {
            setEditingGroup(undefined)
            onToast(`The group ${name} is deleted.`)
            void load(true)
          }}
        />
      )}
      {running && index && (
        <RunDialog
          cluster={route.cluster}
          script={running.script}
          dryRun={running.dryRun}
          scripts={index.scripts}
          off={index.off}
          maxTimeout={index.limits.timeout}
          onClose={() => setRunning(null)}
          onStarted={(r) => {
            setRunning(null)
            closeScript()
            setOpenRun(r.id)
            onToast(`${r.dryRun ? 'Dry run' : 'Run'} ${r.id} started on ${r.nodes.length} ${r.nodes.length === 1 ? 'node' : 'nodes'}.`)
            void load(true)
          }}
        />
      )}
      {openRun !== null && (
        <RunDrawer
          id={openRun}
          now={now}
          onRetried={(r) => {
            setOpenRun(r.id)
            onToast(`Run ${r.id} tries ${r.nodes.length} ${r.nodes.length === 1 ? 'node' : 'nodes'} again.`)
            void load(true)
          }}
          onClose={() => setOpenRun(null)}
          onToast={onToast}
          onEnded={() => void load(true)}
        />
      )}
    </>
  )
}
