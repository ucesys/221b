import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  allows,
  runProfilesFor,
  approvedOf,
  approveVersion,
  rejectVersion,
  revokeVersion,
  batches,
  cancelRun,
  canApprove,
  canReject,
  checkScript,
  countsText,
  createScript,
  deleteScript,
  fetchAutomation,
  fetchFleets,
  fetchRun,
  fetchRuns,
  fetchScript,
  latestOf,
  profileName,
  scriptKind,
  ceilingText,
  privilegeText,
  missingParams,
  paramClashes,
  NEW_SCRIPT,
  ALL_NODES,
  CUSTOM_NODES,
  fleetOf,
  formatGroups,
  inGroups,
  inState,
  involved,
  NODE_LABEL,
  pairedNames,
  parseGroups,
  rolloutHelp,
  SCRIPT_STATES,
  scriptGroups,
  nodeTone,
  resolveParams,
  runnable,
  runTone,
  draftScript,
  fetchProfiles,
  reviewVersion,
  retryRun,
  type RunProfile,
  type Group,
  saveScript,
  SCRIPT_NAME,
  UNGROUPED,
  SCRIPT_TEMPLATES,
  startRun,
  submitVersion,
  versionTone,
  type AutomationFleets,
  type AutomationIndex,
  type Finding,
  type Header,
  type NodeState,
  type Param,
  type Run,
  type RunDetail,
  type RolloutMode,
  type Script,
  type ScriptSummary,
  type Tone,
  type Version,
} from "../api/automation";
import type { Me } from "../api/types";
import { fmtTs } from "../lib/format";
import { href, navigate, type Route, type Tab } from "../lib/router";
import { lineDiff } from "../lib/linediff";
import { useSorted, type SortCol } from "../lib/useSorted";
import { DiffLines } from "./apps/DiffView";
import { GroupDrawer } from "./automation/GroupDrawer";
import { CodeArea, CodeView } from "./automation/CodeEditor";
import { RunProfilesSection } from "./automation/RunProfilesSection";
import { VariablesSection } from "./automation/VariablesSection";
import { Seg } from "./automation/Seg";
import { SortTh } from "./automation/SortTh";
import { ConfirmDialog } from "./ConfirmDialog";
import { DecideDialog } from "./automation/DecideDialog";
import { Drawer } from "./Drawer";
import {
  DiffIcon,
  NoDiffIcon,
  PencilIcon,
  PlusIcon,
  SearchIcon,
} from "./Icons";
import { ScrollBox } from "./Table";
import type { TabProps } from "./tabs";

const ROLLOUTS: [RolloutMode, string][] = [
  ["canary", "Canary first"],
  ["size", "Batches"],
  ["all", "All at once"],
];

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

function Pill({
  tone,
  children,
  testId,
}: {
  tone?: Tone;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <span className="rk-pill" data-tone={tone} data-testid={testId}>
      {children}
    </span>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <label className="rk-flabel">
      {label}
      {children}
      {hint && <span className="rk-fhint">{hint}</span>}
    </label>
  );
}

function Findings({
  list,
  onLine,
}: {
  list: Finding[] | null | undefined;
  onLine?: (line: number) => void;
}) {
  if (!list?.length)
    return (
      <p
        className="tb-hint"
        style={{ margin: 0 }}
        data-testid="auto-findings-none"
      >
        No findings: the header is complete and no check fired.
      </p>
    );
  return (
    <ul
      style={{
        margin: 0,
        paddingLeft: 0,
        listStyle: "none",
        display: "flex",
        flexDirection: "column",
        gap: 4,
        fontSize: 12.5,
      }}
      data-testid="auto-findings"
    >
      {list.map((f, i) => (
        <li key={i} style={{ display: "flex", gap: 8, alignItems: "baseline" }}>
          <Pill tone={f.level === "error" ? "err" : "warn"}>{f.level}</Pill>
          {onLine ? (
            <button
              className="rk-link rk-mono"
              style={{ minWidth: 48, textAlign: "left" }}
              onClick={() => onLine(f.line || 1)}
              data-testid="finding-line"
            >
              {f.line > 0 ? `line ${f.line}` : "header"}
            </button>
          ) : (
            <span
              className="rk-mono"
              style={{ color: "var(--fg3)", minWidth: 48 }}
            >
              {f.line > 0 ? `line ${f.line}` : "header"}
            </span>
          )}
          <span>{f.msg}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The Automation tab (Phase 11 part 2, AU-01..AU-14): scripts, their
 * runs and the fleets' variables, for a SuperAdmin.
 */
export function AutomationTab({
  route,
  onToast,
  me,
  now,
}: TabProps & { me: Me | null }) {
  const section: Tab = route.tab;
  const [index, setIndex] = useState<AutomationIndex | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<string | null | undefined>(undefined); // a name, null for a new script
  const [running, setRunning] = useState<{
    script: string;
    dryRun: boolean;
  } | null>(null);
  const [openRun, setOpenRun] = useState<number | null>(null);
  const [editingGroup, setEditingGroup] = useState<Group | null | undefined>(
    undefined,
  );
  // A new group asked for from the script drawer goes back into its box.
  const afterGroup = useRef<((name: string) => void) | null>(null);
  // Until the cluster is known, only approved versions are offered.
  const [production, setProduction] = useState(true);
  const allowed = me?.admin === true;
  // A link can open a script, on a version to review: ?script=x&version=2.
  const linked = section === "scripts" ? route.params.get("script") : null;
  const linkedVersion = Number(route.params.get("version")) || undefined;
  const scriptOpen = editing !== undefined ? editing : (linked ?? undefined);
  const closeScript = () => {
    setEditing(undefined);
    if (linked) navigate(href("scripts"), true);
  };
  useEffect(() => {
    if (!allowed) return;
    let gone = false;
    fetchFleets(route.cluster)
      .then((f) => !gone && setProduction(f.production))
      .catch(() => {});
    return () => {
      gone = true;
    };
  }, [route.cluster, allowed]);

  const fetchAll = useCallback(
    (background: boolean) =>
      Promise.all([fetchAutomation(background), fetchRuns(background)]).then(
        ([ix, rs]) => ({ ix, rs, err: "" }),
        (e: unknown) => ({ ix: null, rs: null, err: errText(e) }),
      ),
    [],
  );
  const apply = useCallback(
    (x: { ix: AutomationIndex | null; rs: Run[] | null; err: string }) => {
      if (x.ix) setIndex(x.ix);
      if (x.rs) setRuns(x.rs);
      setErr(x.err);
    },
    [],
  );
  const load = useCallback(
    (background = false) => fetchAll(background).then(apply),
    [fetchAll, apply],
  );
  useEffect(() => {
    if (!allowed) return;
    let gone = false;
    void fetchAll(false).then((x) => !gone && apply(x));
    return () => {
      gone = true;
    };
  }, [fetchAll, apply, allowed]);
  // While a run goes on, the lists follow it.
  const live = (index?.running ?? 0) > 0;
  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => void load(true), 4000);
    return () => clearInterval(t);
  }, [live, load]);

  // The page beside the side pane; Parameters puts its own pane beside it.
  const frame = (body: ReactNode) => (
        <section
          className="rk-pane"
          style={{
            flex: "1 1 600px",
            minWidth: 0,
            display: "flex",
            flexDirection: "column",
            gap: 14,
            padding: "16px 20px 32px",
            background: "var(--bg)",
          }}
          aria-label="Automation"
        >
          {index?.off && (
            <p
              className="rk-callout"
              data-tone="warn"
              style={{ margin: 0, fontSize: 12.5 }}
              data-testid="automation-off"
            >
              {index.off} Scripts can be written and approved; nothing runs until
              SSH is on.
            </p>
          )}
          {section === "run-history" && live && (
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
          {index && section === "scripts" && (
            <ScriptsSection
              index={index}
              route={route}
              production={production}
              now={now}
              onEdit={setEditing}
              onRun={(script, dryRun) => setRunning({ script, dryRun })}
              onOpenRun={setOpenRun}
            />
          )}
          {index && section === "run-history" && (
            <RunsSection runs={runs} now={now} onOpen={setOpenRun} />
          )}
          {body}
          {index && section === "run-profiles" && (
            <RunProfilesSection cluster={route.cluster} onToast={onToast} />
          )}
        </section>
  );

  if (me && !me.admin)
    return (
      <div
        style={{ flex: "1 1 auto", padding: 24 }}
        data-testid="automation-denied"
      >
        <p className="tb-hint">Only a SuperAdmin may use Automation.</p>
      </div>
    );

  return (
    <>
      {index && section === "scripts" && (
        <ScriptGroups
          scripts={index.scripts}
          kept={index.groups ?? []}
          route={route}
          onEdit={setEditingGroup}
        />
      )}
      {index && section === "variables" ? (
        <VariablesSection
          cluster={route.cluster}
          scripts={index.scripts}
          linkedFleet={route.params.get("fleet")}
          now={now}
          frame={frame}
          onToast={onToast}
        />
      ) : (
        frame(null)
      )}
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
            if (name !== undefined) setEditing(name);
            void load(true);
          }}
          onDeleted={() => {
            closeScript();
            onToast("The script is deleted.");
            void load(true);
          }}
          onRun={(script, dryRun) => setRunning({ script, dryRun })}
          onNewGroup={(done) => {
            afterGroup.current = done;
            setEditingGroup(null);
          }}
          onToast={onToast}
        />
      )}
      {editingGroup !== undefined && index && (
        <GroupDrawer
          group={editingGroup}
          scripts={index.scripts}
          onClose={() => {
            afterGroup.current = null;
            setEditingGroup(undefined);
          }}
          onSaved={(name) => {
            afterGroup.current?.(name);
            afterGroup.current = null;
            setEditingGroup(undefined);
            onToast(`${name} is saved.`);
            void load(true);
          }}
          onDeleted={(name) => {
            setEditingGroup(undefined);
            onToast(`The group ${name} is deleted.`);
            void load(true);
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
            setRunning(null);
            closeScript();
            setOpenRun(r.id);
            onToast(
              `${r.dryRun ? "Dry run" : "Run"} ${r.id} started on ${r.nodes.length} ${r.nodes.length === 1 ? "node" : "nodes"}.`,
            );
            void load(true);
          }}
        />
      )}
      {openRun !== null && (
        <RunDrawer
          id={openRun}
          now={now}
          onRetried={(r) => {
            setOpenRun(r.id);
            onToast(
              `Run ${r.id} tries ${r.nodes.length} ${r.nodes.length === 1 ? "node" : "nodes"} again.`,
            );
            void load(true);
          }}
          onClose={() => setOpenRun(null)}
          onToast={onToast}
          onEnded={() => void load(true)}
        />
      )}
    </>
  );
}

// ---------- scripts ----------

/** The Scripts page's filters, in the URL: the state and the groups picked. */
function goScripts(state: string, groups: string[]) {
  navigate(href("scripts", [], { state, group: formatGroups(groups) }), true);
}

/**
 * The groups beside the Scripts table, as the fleets are beside the Nodes
 * table: a group's name shows it alone, and its box adds it to the others.
 */
function ScriptGroups({
  scripts,
  kept,
  route,
  onEdit,
}: {
  scripts: ScriptSummary[];
  kept: Group[];
  route: Route;
  onEdit: (g: Group | null) => void;
}) {
  const state = route.params.get("state") ?? "";
  const sel = parseGroups(route.params.get("group"));
  const groups = scriptGroups(scripts, kept);
  return (
    <aside
      className="rk-pane rk-nodefleets"
      aria-label="Script groups"
      data-testid="script-groups"
      style={{
        flex: "0 0 250px",
        display: "flex",
        flexDirection: "column",
        background: "var(--surface)",
        borderRight: "1px solid var(--line)",
        overflow: "auto",
      }}
    >
      <div
        style={{
          padding: "10px 14px",
          borderBottom: "1px solid var(--line)",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 8,
        }}
      >
        <h2 className="rk-h">
          Groups{" "}
          <span
            style={{
              fontWeight: 500,
              letterSpacing: 0,
              textTransform: "none",
            }}
          >
            {groups.length}
          </span>
        </h2>
        <button
          className="rk-btn"
          onClick={() => onEdit(null)}
          data-testid="script-group-new"
        >
          <PlusIcon />
          New group
        </button>
      </div>
      <div style={{ padding: "0 0 6px" }}>
        <button
          className="rk-fl rk-fl-main"
          data-sel={sel.length === 0}
          aria-pressed={sel.length === 0}
          onClick={() => goScripts(state, [])}
          style={{ borderTop: 0, padding: "9px 14px" }}
          data-testid="script-group-all"
        >
          <span className="rk-fl-top">
            <span className="rk-fl-name">All scripts</span>
            <span className="rk-cnt">{scripts.length}</span>
          </span>
        </button>
        {groups.map((g) => {
          const checked = sel.includes(g.id);
          return (
            <div
              key={g.id}
              className="rk-fl"
              data-sel={checked}
              data-group={g.id}
              style={{ paddingRight: 6 }}
            >
              <label
                className="rk-fl-check"
                title={checked ? "Show every script again" : `Add ${g.name}`}
              >
                <input
                  type="checkbox"
                  checked={checked}
                  aria-label={
                    checked
                      ? `Stop filtering by ${g.name}`
                      : `Filter the scripts by ${g.name}`
                  }
                  onChange={() =>
                    goScripts(
                      state,
                      checked ? sel.filter((x) => x !== g.id) : [...sel, g.id],
                    )
                  }
                  data-testid="script-group-check"
                />
              </label>
              <button
                className="rk-fl-main"
                aria-pressed={checked}
                onClick={() =>
                  goScripts(state, checked && sel.length === 1 ? [] : [g.id])
                }
                title={
                  checked && sel.length === 1
                    ? "Show all scripts again"
                    : `Show only ${g.name}`
                }
                style={{ padding: "8px 6px 8px 0" }}
                data-testid="script-group-pick"
              >
                <span className="rk-fl-top">
                  <span className="rk-fl-name">{g.name}</span>
                  <span className="rk-cnt">{g.count}</span>
                </span>
              </button>
              {g.id !== UNGROUPED && (
                <button
                  className="rk-icon-btn"
                  aria-label={`Edit group ${g.name}`}
                  title="Edit this group"
                  onClick={() =>
                    onEdit(
                      kept.find((x) => x.name === g.name) ?? { name: g.name },
                    )
                  }
                  data-testid="script-group-edit"
                >
                  <PencilIcon />
                </button>
              )}
            </div>
          );
        })}
      </div>
    </aside>
  );
}

const SCRIPT_SORT: SortCol<ScriptSummary>[] = [
  { id: "name", sortValue: (s) => s.name },
  { id: "kind", sortValue: (s) => scriptKind(s.target) },
  { id: "group", sortValue: (s) => s.group || null },
  { id: "latest", sortValue: (s) => s.version, defaultDir: "desc" },
  { id: "runs", sortValue: (s) => s.approved || null, defaultDir: "desc" },
  { id: "privilege", sortValue: (s) => s.privilege || null },
  {
    id: "checks",
    sortValue: (s) => s.errors * 10000 + s.warnings,
    defaultDir: "desc",
  },
  {
    id: "lastRun",
    sortValue: (s) => s.lastRun?.started ?? null,
    defaultDir: "desc",
  },
];

function ScriptsSection({
  index,
  route,
  production,
  now,
  onEdit,
  onRun,
  onOpenRun,
}: {
  index: AutomationIndex;
  route: Route;
  production: boolean;
  now: number;
  onEdit: (name: string | null) => void;
  onRun: (script: string, dryRun: boolean) => void;
  onOpenRun: (id: number) => void;
}) {
  const [q, setQ] = useState("");
  const state = route.params.get("state") ?? "";
  const groups = parseGroups(route.params.get("group"));
  const picked = index.scripts.filter(
    (s) =>
      inState(s, state) &&
      inGroups(s, groups) &&
      (!q ||
        s.name.includes(q.toLowerCase()) ||
        s.describe.toLowerCase().includes(q.toLowerCase()) ||
        s.group.toLowerCase().includes(q.toLowerCase())),
  );
  const { sort, onSort, shown } = useSorted(
    "scripts",
    SCRIPT_SORT,
    picked,
    { col: "name", dir: "asc" },
    now,
  );
  const th = (col: string, label: string) => (
    <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  );
  return (
    <>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 12,
          flexWrap: "wrap",
        }}
      >
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Scripts</h2>
        <label className="rk-field" style={{ width: 260 }}>
          <SearchIcon />
          <span className="rk-sr">Find a script</span>
          <input
            type="search"
            placeholder="Find a script"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            data-testid="scripts-search"
          />
        </label>
        <div
          className="rk-seg"
          role="group"
          aria-label="Show scripts by state"
          data-testid="scripts-state"
        >
          {[{ id: "", label: "All" }, ...SCRIPT_STATES].map((o) => (
            <button
              key={o.id}
              aria-pressed={state === o.id}
              onClick={() => goScripts(o.id, groups)}
              data-state={o.id || "all"}
            >
              {o.label}{" "}
              <span style={{ color: "var(--fg3)", fontWeight: 400 }}>
                {index.scripts.filter((x) => inState(x, o.id)).length}
              </span>
            </button>
          ))}
        </div>
        <button
          className="rk-btn"
          data-on
          style={{ marginLeft: "auto" }}
          onClick={() => onEdit(null)}
          data-testid="script-new"
        >
          <PlusIcon />
          New script
        </button>
      </div>
      {index.scripts.length === 0 && (
        <div
          className="rk-card"
          style={{ padding: "16px 18px", fontSize: 13 }}
          data-testid="scripts-empty"
        >
          <p style={{ margin: "0 0 6px" }}>No scripts yet.</p>
          <p className="tb-hint" style={{ margin: 0 }}>
            A script is a shell script with a{" "}
            <span className="rk-mono"># 221b:</span> header that says what it
            does, its parameters, its privilege and whether it honours a dry
            run. It is checked on every save, approved once per version, and run
            on a fleet’s nodes over SSH.
          </p>
        </div>
      )}
      {index.scripts.length > 0 && shown.length === 0 && (
        <p className="tb-hint" style={{ margin: 0 }} data-testid="scripts-none">
          No script matches.
        </p>
      )}
      {shown.length > 0 && (
        <section
          className="rk-card"
          style={{ padding: 0, overflow: "auto" }}
          aria-label="Scripts"
          data-testid="scripts-table"
        >
          <table className="tb-table">
            <thead>
              <tr>
                {th("name", "Script")}
                {th("kind", "Kind")}
                {th("group", "Group")}
                {th("latest", "Latest")}
                {th("runs", "Runs")}
                {th("privilege", "Privilege")}
                {th("checks", "Checks")}
                {th("lastRun", "Last run")}
                <th aria-label="Tools" />
              </tr>
            </thead>
            <tbody>
              {shown.map((s) => (
                <ScriptRow
                  key={s.name}
                  s={s}
                  production={production}
                  onEdit={onEdit}
                  onRun={onRun}
                  onOpenRun={onOpenRun}
                />
              ))}
            </tbody>
          </table>
        </section>
      )}
    </>
  );
}

function ScriptRow({
  s,
  production,
  onEdit,
  onRun,
  onOpenRun,
}: {
  s: ScriptSummary;
  production: boolean;
  onEdit: (name: string) => void;
  onRun: (script: string, dryRun: boolean) => void;
  onOpenRun: (id: number) => void;
}) {
  return (
    <tr data-testid="script-row" data-name={s.name}>
      <td>
        <button
          className="rk-link rk-mono"
          onClick={() => onEdit(s.name)}
          style={{ fontWeight: 600 }}
          data-testid="script-open"
        >
          {s.name}
        </button>
        <div style={{ color: "var(--fg3)", fontSize: 12 }}>
          {s.describe || "no description"}
        </div>
      </td>
      <td data-testid="script-kind">{scriptKind(s.target)}</td>
      <td>{s.group || <span style={{ color: "var(--fg3)" }}>—</span>}</td>
      <td>
        v{s.version} <Pill tone={versionTone(s.state)}>{s.state}</Pill>
      </td>
      <td>
        {s.approved ? (
          `v${s.approved}, ${s.approvals} ${s.approvals === 1 ? "approval" : "approvals"}`
        ) : (
          <span style={{ color: "var(--fg3)" }}>nothing approved</span>
        )}
      </td>
      <td className="rk-mono">{privilegeText(s.privilege)}</td>
      <td>
        {s.errors > 0 && <Pill tone="err">{s.errors} errors</Pill>}{" "}
        {s.warnings > 0 && <Pill tone="warn">{s.warnings} warnings</Pill>}
        {s.errors + s.warnings === 0 && (
          <span style={{ color: "var(--fg3)" }}>clean</span>
        )}
      </td>
      <td>
        {s.lastRun ? (
          <button
            className="rk-link"
            onClick={() => onOpenRun(s.lastRun!.id)}
            data-testid="script-last-run"
          >
            #{s.lastRun.id}{" "}
            <Pill tone={runTone(s.lastRun.state)}>
              {s.lastRun.dryRun
                ? `dry run ${s.lastRun.state}`
                : s.lastRun.state}
            </Pill>
          </button>
        ) : (
          <span style={{ color: "var(--fg3)" }}>never</span>
        )}
      </td>
      <td style={{ whiteSpace: "nowrap", textAlign: "right" }}>
        <>
          <button
            className="rk-btn"
            onClick={() => onRun(s.name, true)}
            disabled={s.errors > 0 || !s.dryRun}
            data-testid="script-dry-run"
            title={
              s.dryRun
                ? "Run the latest version with DRY_RUN=1: nothing changes"
                : "This script does not declare dry-run: yes, so it has no dry run"
            }
          >
            Dry run…
          </button>{" "}
          <button
            className="rk-btn"
            onClick={() => onRun(s.name, false)}
            disabled={!s.approved && (production || s.errors > 0)}
            data-testid="script-run"
            title={
              production
                ? s.approved
                  ? `Run approved v${s.approved}`
                  : "No version is approved yet"
                : `Run v${s.version}, the latest version, or pick another`
            }
          >
            Run…
          </button>
        </>
      </td>
    </tr>
  );
}

function ScriptDrawer({
  name,
  linkedVersion,
  me,
  production,
  maxText,
  groups,
  onClose,
  onChanged,
  onDeleted,
  onRun,
  onNewGroup,
  onToast,
}: {
  name: string | null;
  /** Opens on this version, as Pending approvals sends a reviewer here. */
  linkedVersion?: number;
  me: AutomationIndex["me"];
  production: boolean;
  maxText: number;
  groups: string[];
  onClose: () => void;
  onChanged: (name?: string) => void;
  onDeleted: () => void;
  onRun: (script: string, dryRun: boolean) => void;
  /** Opens a new group over the drawer; done gets its name once saved. */
  onNewGroup: (done: (name: string) => void) => void;
  onToast: (msg: string) => void;
}) {
  const [script, setScript] = useState<Script | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [tab, setTab] = useState(linkedVersion ? "versions" : "script");
  const [newName, setNewName] = useState("");
  const [group, setGroup] = useState("");
  const [text, setText] = useState(name === null ? NEW_SCRIPT : "");
  const [check, setCheck] = useState<{
    header: Header;
    findings: Finding[] | null;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [actErr, setActErr] = useState("");
  const [viewing, setViewing] = useState<number | null>(linkedVersion ?? null);
  const [against, setAgainst] = useState<number | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  // What a new script starts from: the empty one, or the template picked.
  const [base, setBase] = useState({ text: NEW_SCRIPT, name: "", group: "" });
  const [confirmRevoke, setConfirmRevoke] = useState<number | null>(null);
  // The version an Approve or Reject asks about first.
  const [deciding, setDeciding] = useState<{
    n: number;
    how: "approve" | "reject";
  } | null>(null);
  const [saveTried, setSaveTried] = useState(false);
  const [ask, setAsk] = useState("");
  const [asking, setAsking] = useState(false);
  const [askErr, setAskErr] = useState("");
  const [questions, setQuestions] = useState<string[]>([]);
  const [said, setSaid] = useState<{
    notes: string[];
    rounds: number;
    added: number;
    removed: number;
  } | null>(null);
  const [jump, setJump] = useState<{ line: number } | null>(null);
  const asked = useRef<AbortController | null>(null);
  useEffect(() => () => asked.current?.abort(), []);
  const write = () => {
    const ctl = new AbortController();
    asked.current = ctl;
    setAsking(true);
    setAskErr("");
    setQuestions([]);
    setSaid(null);
    const before = text === NEW_SCRIPT ? "" : text;
    draftScript(ask, text === NEW_SCRIPT ? "" : text, ctl.signal)
      .then((d) => {
        setText(d.text);
        setAsk("");
        setQuestions(d.questions ?? []);
        const diff = lineDiff(before, d.text);
        setSaid({
          notes: d.notes ?? [],
          rounds: d.rounds,
          added: diff.added,
          removed: diff.removed,
        });
      })
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) setAskErr(errText(e));
      })
      .finally(() => setAsking(false));
  };

  useEffect(() => {
    if (name === null) return;
    let gone = false;
    fetchScript(name)
      .then((s) => {
        if (gone) return;
        setScript(s);
        setGroup(s.group ?? "");
        setText(latestOf(s).text);
      })
      .catch((e: unknown) => !gone && setLoadErr(errText(e)));
    return () => {
      gone = true;
    };
  }, [name]);

  // The checks run as the text changes, as they will on save (AU-03).
  useEffect(() => {
    if (!text) return;
    const t = setTimeout(() => {
      checkScript(text)
        .then(setCheck)
        .catch(() => setCheck(null));
    }, 400);
    return () => clearTimeout(t);
  }, [text]);

  const latest = script ? latestOf(script) : undefined;
  const approved = script ? approvedOf(script) : undefined;
  const changed =
    name === null ||
    (latest !== undefined &&
      (text !== latest.text || group !== (script?.group ?? "")));
  // Closing with changes not saved asks first. Once saved, a new script
  // compares with what was saved, before the drawer takes its name.
  const unsaved = script
    ? text !== latestOf(script).text || group !== (script.group ?? "")
    : name === null &&
      (text !== base.text || newName !== base.name || group !== base.group);
  // A save under way finishes on the server whether the drawer stays or not.
  const close = () => (unsaved && !busy ? setConfirmClose(true) : onClose());
  const nameBad = name === null && newName !== "" && !SCRIPT_NAME.test(newName);
  const tooLong = new TextEncoder().encode(text).length > maxText;
  const errors = (check?.findings ?? []).filter(
    (f) => f.level === "error",
  ).length;
  // Save stays there to press: pressing it says what it still needs.
  const saveWhy = tooLong
    ? `The script is over ${maxText / 1024} KiB: shorten it.`
    : name === null && !newName
      ? "Give the script a name first."
      : nameBad
        ? "The name takes lowercase letters, digits and dashes."
        : !changed
          ? "Nothing to save yet: change the script first."
          : "";

  const act = async (f: () => Promise<Script>, done: string) => {
    setBusy(true);
    setActErr("");
    try {
      const s = await f();
      setScript(s);
      setText(latestOf(s).text);
      setGroup(s.group ?? "");
      onToast(done);
      onChanged(s.name);
    } catch (e) {
      setActErr(errText(e));
    } finally {
      setBusy(false);
    }
  };
  const save = () =>
    name === null
      ? act(
          () => createScript(newName, group, text),
          `${newName} v1 is saved as a draft.`,
        )
      : act(() => saveScript(name, group, text), "Saved.");

  // What opens is what needs reading: the version in review, else the latest.
  const shownVersion =
    script?.versions.find((v) => v.n === viewing) ??
    script?.versions.find((v) => v.state === "review") ??
    latest;
  const pickTemplate = (id: string) => {
    const t = SCRIPT_TEMPLATES.find((x) => x.id === id);
    if (!t) return;
    setText(t.text);
    setNewName(t.name);
    setGroup(t.group);
    setBase({ text: t.text, name: t.name, group: t.group });
  };
  // Production runs approved versions only; anywhere else Run takes the
  // newest version, approved or not, and never a rejected one.
  const runnableVersion = production
    ? approved
    : script
      ? [...script.versions].reverse().find((v) => v.state !== "rejected")
      : undefined;

  const groupPick = (
    <>
      <label
        htmlFor="script-group"
        style={{ fontSize: 12.5, color: "var(--fg3)" }}
      >
        Group
      </label>
      <span className="rk-field" style={{ width: 220, height: 30 }}>
        <select
          id="script-group"
          value={group}
          onChange={(e) => setGroup(e.target.value)}
          disabled={asking}
          data-testid="script-group"
        >
          <option value="">Ungrouped</option>
          {[...new Set([...groups, ...(group ? [group] : [])])]
            .sort((a, b) => a.localeCompare(b))
            .map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
        </select>
      </span>
      <button
        className="rk-btn"
        onClick={() => onNewGroup(setGroup)}
        disabled={asking}
        style={{ height: 30 }}
        data-testid="script-new-group"
      >
        <PlusIcon />
        New group
      </button>
    </>
  );

  const locked = busy || asking;
  const footer = (
    <div
      style={{
        display: "flex",
        flexWrap: "wrap",
        alignItems: "center",
        gap: 8,
      }}
    >
      {actErr && (
        <p
          className="tb-err"
          style={{ flex: "1 1 100%", margin: 0 }}
          data-testid="script-error"
        >
          {actErr}
        </p>
      )}
      {saveTried && saveWhy && (
        <p
          className="tb-err"
          style={{ flex: "1 1 100%", margin: 0 }}
          data-testid="script-save-why"
        >
          {saveWhy}
        </p>
      )}
      <button
        className="rk-btn"
        data-on
        onClick={() => {
          setSaveTried(!!saveWhy);
          if (!saveWhy) void save();
        }}
        disabled={locked}
        data-testid="script-save"
      >
        {name === null || latest?.state === "draft"
          ? "Save draft"
          : `Save as v${(latest?.n ?? 0) + 1} draft`}
      </button>
      {latest && latest.state === "draft" && !changed && (
        <button
          className="rk-btn"
          onClick={() =>
            void act(
              () => submitVersion(script!.name, latest.n),
              `v${latest.n} is in review.`,
            )
          }
          disabled={
            locked ||
            errors > 0 ||
            (latest.findings ?? []).some((f) => f.level === "error")
          }
          data-testid="script-submit"
        >
          Submit v{latest.n} for review
        </button>
      )}
      {latest && !changed && canApprove(latest, me) && (
        <button
          className="rk-btn"
          onClick={() => setDeciding({ n: latest.n, how: "approve" })}
          disabled={locked}
          data-testid="script-approve"
        >
          Approve v{latest.n}…
        </button>
      )}
      {latest && !changed && canReject(latest, me) && (
        <button
          className="rk-btn"
          data-danger="true"
          onClick={() => setDeciding({ n: latest.n, how: "reject" })}
          disabled={locked}
          data-testid="script-reject"
        >
          Reject v{latest.n}…
        </button>
      )}
      {latest &&
        !changed &&
        latest.state === "review" &&
        involved(latest, me) && (
          <span
            className="tb-hint"
            style={{ margin: 0 }}
            data-testid="script-approve-wait"
          >
            Another SuperAdmin must approve v{latest.n}: you wrote or submitted
            it.
          </span>
        )}
      {script && (
        <>
          <button
            className="rk-btn"
            style={{ marginLeft: "auto" }}
            onClick={() => onRun(script.name, true)}
            disabled={locked || changed || errors > 0 || !latest?.header.dryRun}
            title={
              latest?.header.dryRun
                ? undefined
                : "This script does not declare dry-run: yes, so it has no dry run"
            }
            data-testid="script-drawer-dry-run"
          >
            Dry run…
          </button>
          <button
            className="rk-btn"
            onClick={() => onRun(script.name, false)}
            disabled={
              locked ||
              !runnableVersion ||
              (runnableVersion.n === latest?.n && (changed || errors > 0))
            }
            data-testid="script-drawer-run"
          >
            Run v{runnableVersion?.n ?? "—"}…
          </button>
          <button
            className="rk-btn"
            data-danger="true"
            onClick={() => setConfirmDelete(true)}
            disabled={locked}
            data-testid="script-delete"
          >
            Delete
          </button>
        </>
      )}
    </div>
  );

  return (
    <>
      <Drawer
        label="Script"
        kicker={
          name === null
            ? "New script"
            : `Script${script?.group ? ` in ${script.group}` : ""}`
        }
        title={name ?? (newName || "new-script")}
        copy={name === null ? false : undefined}
        facts={
          script && latest ? (
            <span
              style={{
                display: "flex",
                gap: 8,
                flexWrap: "wrap",
                alignItems: "center",
                fontSize: 12.5,
              }}
            >
              v{latest.n}{" "}
              <Pill tone={versionTone(latest.state)}>{latest.state}</Pill>
              {approved
                ? `runs v${approved.n} (${approved.approvals?.length ?? 0} ${approved.approvals?.length === 1 ? "approval" : "approvals"})`
                : "nothing approved to run"}
              <span style={{ color: "var(--fg3)" }}>
                updated {fmtTs(script.updated)}
              </span>
            </span>
          ) : undefined
        }
        tabs={
          name === null
            ? undefined
            : [
                { id: "script", label: "Script" },
                {
                  id: "versions",
                  label: "Versions",
                  count: script?.versions.length,
                },
              ]
        }
        tab={tab}
        onTab={setTab}
        onClose={close}
        footer={tab === "script" ? footer : undefined}
        tabsEnd={tab === "script" ? groupPick : undefined}
        width={1400}
        testId="script-drawer"
      >
        {loadErr && <p className="tb-err">{loadErr}</p>}
        {tab === "script" && (name === null || script) && (
          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            {name === null && (
            <div
              style={{
                display: "flex",
                gap: 12,
                flexWrap: "wrap",
                alignItems: "flex-start",
              }}
            >
              {name === null && (
                <Field label="Start from">
                  <span className="rk-field" style={{ width: 320 }}>
                    <select
                      defaultValue="blank"
                      onChange={(e) => pickTemplate(e.target.value)}
                      disabled={asking}
                      data-testid="script-template"
                    >
                      {SCRIPT_TEMPLATES.map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.label}
                        </option>
                      ))}
                    </select>
                  </span>
                </Field>
              )}
              {name === null && (
                <Field
                  label="Name"
                  hint={
                    nameBad
                      ? "Lowercase letters, digits and dashes, starting with a letter or digit."
                      : "It cannot change later."
                  }
                >
                  <span className="rk-field" style={{ width: 260 }}>
                    <input
                      value={newName}
                      onChange={(e) => setNewName(e.target.value.trim())}
                      placeholder="ensure-chrony"
                      aria-invalid={nameBad}
                      disabled={asking}
                      data-testid="script-name"
                    />
                  </span>
                </Field>
              )}
            </div>
            )}
            <div
              style={{
                display: "flex",
                flexWrap: "wrap",
                gap: 16,
                alignItems: "stretch",
              }}
            >
              <div style={{ flex: "3 1 560px", minWidth: 0 }}>
                <Field
                  label="Script"
                  hint={
                    tooLong
                      ? `Over ${maxText / 1024} KiB: shorten it.`
                      : "Header lines start with # 221b: describe, param, privilege, dry-run, facts."
                  }
                >
                  <CodeArea
                    value={text}
                    onChange={setText}
                    testId="script-text"
                    height="calc(100vh - 330px)"
                    readOnly={asking}
                    jump={jump}
                  />
                </Field>
              </div>
              <aside
                style={{
                  flex: "1 1 340px",
                  minWidth: 0,
                  maxHeight: "calc(100vh - 300px)",
                  overflow: "auto",
                  display: "flex",
                  flexDirection: "column",
                  gap: 12,
                }}
                aria-label="Sherlock and checks"
              >
                {check && (
                  <div
                    style={{
                      fontSize: 12.5,
                      display: "grid",
                      gridTemplateColumns: "max-content 1fr",
                      gap: "4px 12px",
                    }}
                    data-testid="script-header"
                  >
                    <span style={{ color: "var(--fg3)" }}>Kind</span>
                    <span data-testid="script-drawer-kind">
                      {scriptKind(check.header.target)}
                    </span>
                    <span style={{ color: "var(--fg3)" }}>Privilege</span>
                    <span className="rk-mono">
                      {privilegeText(check.header.privilege)}
                    </span>
                    <span style={{ color: "var(--fg3)" }}>Dry run</span>
                    <span>
                      {check.header.dryRun
                        ? "honoured: DRY_RUN=1 changes nothing"
                        : "not supported"}
                    </span>
                    <span style={{ color: "var(--fg3)" }}>Parameters</span>
                    <span>
                      {(check.header.params ?? []).length === 0
                        ? "none"
                        : (check.header.params ?? []).map((p) => (
                            <span key={p.name} style={{ display: "block" }}>
                              <span className="rk-mono">{p.name}</span>
                              {p.required ? " (required)" : ""}
                            </span>
                          ))}
                    </span>
                  </div>
                )}
                <section
                  style={{ display: "flex", flexDirection: "column", gap: 6 }}
                  data-testid="script-sherlock"
                >
                  <h3 className="rk-h" style={{ margin: 0 }}>
                    Sherlock
                  </h3>
                  <textarea
                    className="rk-area"
                    aria-label="What the script should do"
                    placeholder="Keep chrony running on every node"
                    rows={4}
                    value={ask}
                    onChange={(e) => setAsk(e.target.value)}
                    disabled={asking}
                    style={{ resize: "vertical", minHeight: 88 }}
                    data-testid="script-sherlock-request"
                  />
                  <div
                    style={{ display: "flex", gap: 8, alignItems: "center" }}
                  >
                    <button
                      className="rk-btn"
                      onClick={write}
                      disabled={
                        asking ||
                        (!ask.trim() && !(check?.findings ?? []).length)
                      }
                      data-testid="script-sherlock-draft"
                    >
                      {asking
                        ? "Working…"
                        : !ask.trim() && (check?.findings ?? []).length
                          ? "Fix with Sherlock"
                          : "Write with Sherlock"}
                    </button>
                    {asking && (
                      <button
                        className="rk-btn"
                        onClick={() => asked.current?.abort()}
                        data-testid="script-sherlock-cancel"
                      >
                        Cancel
                      </button>
                    )}
                  </div>
                  {asking && (
                    <p
                      className="tb-hint"
                      style={{ margin: 0 }}
                      data-testid="script-sherlock-working"
                    >
                      Sherlock is writing and fixing what the checks find. The
                      script is locked until it ends.
                    </p>
                  )}
                  {(askErr || said || questions.length > 0) && (
                    <div
                      style={{
                        maxHeight: "15em",
                        overflow: "auto",
                        lineHeight: 1.5,
                        display: "flex",
                        flexDirection: "column",
                        gap: 6,
                      }}
                      tabIndex={0}
                      role="region"
                      aria-label="What Sherlock said"
                      data-testid="script-sherlock-output"
                    >
                      {askErr && (
                        <p
                          className="tb-err"
                          style={{ margin: 0 }}
                          data-testid="script-sherlock-error"
                        >
                          {askErr}
                        </p>
                      )}
                      {said && (
                        <div
                          style={{
                            padding: "8px 10px",
                            borderRadius: 8,
                            border: "1px solid var(--line)",
                            background: "var(--bg)",
                            fontSize: 12.5,
                            display: "flex",
                            flexDirection: "column",
                            gap: 6,
                          }}
                          data-testid="script-sherlock-notes"
                        >
                          <div style={{ color: "var(--fg3)" }}>
                            Sherlock changed the script: {said.added} lines added,{" "}
                            {said.removed} removed
                            {said.rounds > 1
                              ? `, in ${said.rounds} rounds with the checks`
                              : ""}
                            . Nothing is saved until you save it.
                          </div>
                          {said.notes.map((n, i) => (
                            <p
                              key={i}
                              style={{ margin: 0, whiteSpace: "pre-wrap" }}
                            >
                              {n}
                            </p>
                          ))}
                        </div>
                      )}
                      {questions.length > 0 && (
                        <div data-testid="script-sherlock-questions">
                          <p className="tb-hint" style={{ margin: "0 0 4px" }}>
                            Sherlock needs your answer: say it above and ask again.
                          </p>
                          <ul
                            style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }}
                          >
                            {questions.map((q) => (
                              <li key={q}>{q}</li>
                            ))}
                          </ul>
                        </div>
                      )}
                    </div>
                  )}
                </section>
                <section>
                  <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                    Checks
                  </h3>
                  {check ? (
                    <Findings
                      list={check.findings}
                      onLine={(line) => setJump({ line })}
                    />
                  ) : (
                    <p className="tb-hint">Checking…</p>
                  )}
                </section>
              </aside>
            </div>
          </div>
        )}
        {tab === "versions" && script && shownVersion && (
          <div
            style={{
              display: "flex",
              flexWrap: "wrap",
              gap: 16,
              alignItems: "flex-start",
            }}
          >
            <div
              style={{
                flex: "1 1 340px",
                maxWidth: 480,
                minWidth: 0,
                display: "flex",
                flexDirection: "column",
                gap: 8,
              }}
            >
              <table className="tb-table" data-testid="script-versions">
                <thead>
                  <tr>
                    <th>Version</th>
                    <th>Author</th>
                    <th>Approvals</th>
                    <th aria-label="Diff" />
                  </tr>
                </thead>
                <tbody>
                  {[...script.versions].reverse().map((v) => (
                    <VersionRow
                      key={v.n}
                      v={v}
                      me={me}
                      busy={busy}
                      shown={shownVersion.n}
                      latest={latest?.n ?? v.n}
                      against={against}
                      onView={() => {
                        setViewing(v.n);
                        setAgainst(null);
                      }}
                      onDiff={() => {
                        setViewing(latest?.n ?? v.n);
                        setAgainst(v.n);
                      }}
                      onRevoke={() => setConfirmRevoke(v.n)}
                      onApprove={() => setDeciding({ n: v.n, how: "approve" })}
                      onReject={() => setDeciding({ n: v.n, how: "reject" })}
                    />
                  ))}
                </tbody>
              </table>
              {actErr && <p className="tb-err">{actErr}</p>}
            </div>
            <VersionPane
              key={shownVersion.n}
              script={script}
              v={shownVersion}
              against={against}
              onAgainst={setAgainst}
              onReviewed={(s) => setScript(s)}
            />
          </div>
        )}
      </Drawer>
      {confirmRevoke !== null && script && (
        <ConfirmDialog
          title={`Revoke the approval of v${confirmRevoke}?`}
          confirm="Revoke approval"
          danger
          busy={busy}
          error={actErr}
          onCancel={() => setConfirmRevoke(null)}
          onConfirm={() =>
            void act(
              () => revokeVersion(script.name, confirmRevoke),
              `You revoked the approval of v${confirmRevoke}.`,
            ).then(() => setConfirmRevoke(null))
          }
        >
          <p style={{ margin: 0 }}>
            It goes back to review. Production stops running it until someone
            approves it again; runs already going on carry on. The audit log
            records who revoked it.
          </p>
        </ConfirmDialog>
      )}
      {deciding && script && (
        <DecideDialog
          how={deciding.how}
          what={`${script.name} v${deciding.n}`}
          busy={busy}
          error={actErr}
          onCancel={() => {
            setDeciding(null);
            setActErr("");
          }}
          onConfirm={(comment) =>
            void act(
              () =>
                deciding.how === "approve"
                  ? approveVersion(script.name, deciding.n)
                  : rejectVersion(script.name, deciding.n, comment),
              deciding.how === "approve"
                ? `You approved v${deciding.n}.`
                : `You rejected v${deciding.n}.`,
            ).then(() => setDeciding(null))
          }
        >
          {deciding.how === "approve"
            ? "Once approved, it can run on production, and your approval is recorded in the audit log."
            : "It stays rejected and never runs; the author saves the script again for a new version, and reads your reason."}
        </DecideDialog>
      )}
      {confirmClose && (
        <ConfirmDialog
          title="Close without saving?"
          confirm="Close"
          danger
          onCancel={() => setConfirmClose(false)}
          onConfirm={() => {
            setConfirmClose(false);
            onClose();
          }}
        >
          <p style={{ margin: 0 }} data-testid="script-close-unsaved">
            {name === null
              ? "This script is not saved. Closing loses it."
              : "Your changes to the script are not saved. Closing loses them."}
          </p>
        </ConfirmDialog>
      )}
      {confirmDelete && script && (
        <ConfirmDialog
          title={`Delete ${script.name}?`}
          confirm="Delete"
          danger
          busy={busy}
          error={actErr}
          onCancel={() => setConfirmDelete(false)}
          onConfirm={() => {
            setBusy(true);
            deleteScript(script.name)
              .then(onDeleted)
              .catch((e: unknown) => setActErr(errText(e)))
              .finally(() => setBusy(false));
          }}
        >
          <p style={{ margin: 0 }}>
            Every version of it goes, approved ones too. The runs it made stay
            in Run History and in the audit log.
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

function VersionRow({
  v,
  me,
  busy,
  shown,
  latest,
  against,
  onView,
  onDiff,
  onRevoke,
  onApprove,
  onReject,
}: {
  v: Version;
  me: AutomationIndex["me"];
  busy: boolean;
  /** The version the page shows. */
  shown: number;
  /** The latest version, which each row's diff compares with. */
  latest: number;
  against: number | null;
  onView: () => void;
  onDiff: () => void;
  onRevoke: () => void;
  onApprove: () => void;
  onReject: () => void;
}) {
  const viewing = shown === v.n;
  const diffed = shown === latest && against === v.n;
  return (
    <tr
      data-testid="version-row"
      data-n={v.n}
      style={{ background: viewing ? "var(--accent-weak)" : undefined }}
    >
      <td>
        <button
          className="rk-link"
          onClick={onView}
          aria-pressed={viewing}
          data-testid="version-show"
        >
          v{v.n}
        </button>
        <div>
          <Pill tone={versionTone(v.state)}>{v.state}</Pill>
        </div>
      </td>
      <td>
        {v.author.name}
        <div style={{ color: "var(--fg3)", fontSize: 12 }}>
          {fmtTs(v.created)}
        </div>
      </td>
      <td>
        {(v.approvals ?? []).length === 0 ? (
          <span style={{ color: "var(--fg3)" }}>none</span>
        ) : (
          (v.approvals ?? []).map((a, i) => (
            <div key={i}>
              {a.by.name}{" "}
              <span style={{ color: "var(--fg3)", fontSize: 12 }}>
                {fmtTs(a.at)}
              </span>
            </div>
          ))
        )}
        {(canApprove(v, me) ||
          canReject(v, me) ||
          v.state === "approved") && (
          <div style={{ display: "flex", gap: 4, marginTop: 2 }}>
            {canApprove(v, me) && (
              <button
                className="rk-btn"
                data-size="sm"
                onClick={onApprove}
                disabled={busy}
                data-testid="version-approve"
              >
                Approve…
              </button>
            )}
            {canReject(v, me) && (
              <button
                className="rk-btn"
                data-size="sm"
                data-danger="true"
                onClick={onReject}
                disabled={busy}
                data-testid="version-reject"
              >
                Reject…
              </button>
            )}
            {v.state === "approved" && (
              <button
                className="rk-btn"
                data-size="sm"
                data-danger="true"
                onClick={onRevoke}
                disabled={busy}
                data-testid="version-revoke"
              >
                Revoke
              </button>
            )}
          </div>
        )}
        {v.rejected && (
          <div
            style={{ color: "var(--fg3)", fontSize: 12 }}
            data-testid="version-rejected"
          >
            rejected by {v.rejected.by.name} {fmtTs(v.rejected.at)}
            {v.rejected.comment ? `: ${v.rejected.comment}` : ""}
          </div>
        )}
        {(v.revoked ?? []).map((r, i) => (
          <div key={i} style={{ color: "var(--fg3)", fontSize: 12 }}>
            revoked by {r.by.name} {fmtTs(r.at)}
          </div>
        ))}
      </td>
      <td>
        <span style={{ display: "inline-flex", gap: 4 }}>
          <button
            className="rk-icon-btn"
            aria-label={`No diff: show v${v.n} alone`}
            title={`No diff: show v${v.n} alone`}
            onClick={onView}
            data-testid="version-no-diff"
          >
            <NoDiffIcon />
          </button>
          {v.n !== latest && (
            <button
              className="rk-icon-btn"
              aria-label={`Diff v${latest} against v${v.n}`}
              aria-pressed={diffed}
              title={`Diff v${latest} against v${v.n}`}
              onClick={onDiff}
              data-on={diffed}
              data-testid="version-diff"
            >
              <DiffIcon />
            </button>
          )}
        </span>
      </td>
    </tr>
  );
}

/** A version's text, or what changed since another version. */
export function VersionPane({
  script,
  v,
  against,
  onAgainst,
  onReviewed,
}: {
  script: Script;
  v: Version;
  against: number | null;
  onAgainst: (n: number | null) => void;
  onReviewed: (s: Script) => void;
}) {
  const [reviewing, setReviewing] = useState(false);
  const [reviewErr, setReviewErr] = useState("");
  const review = () => {
    setReviewing(true);
    setReviewErr("");
    reviewVersion(script.name, v.n)
      .then(onReviewed)
      .catch((e: unknown) => setReviewErr(errText(e)))
      .finally(() => setReviewing(false));
  };
  const other = script.versions.find((x) => x.n === against);
  const others = script.versions.filter((x) => x.n !== v.n);
  const diff = other ? lineDiff(other.text, v.text) : null;
  const paneStyle = {
    margin: 0,
    padding: 12,
    borderRadius: 8,
    fontSize: 12,
    lineHeight: 1.5,
    overflow: "auto",
    maxHeight: "calc(100vh - 250px)",
  } as const;
  return (
    <div
      style={{
        flex: "3 1 520px",
        minWidth: 0,
        display: "flex",
        flexDirection: "column",
        gap: 8,
      }}
    >
      <div
        style={{
          display: "flex",
          flexWrap: "wrap",
          alignItems: "center",
          gap: 8,
        }}
      >
        <h3 className="rk-h" style={{ margin: 0 }}>
          v{v.n}
        </h3>
        <Pill tone={versionTone(v.state)}>{v.state}</Pill>
        <span className="rk-mono" style={{ fontSize: 12 }} title={v.hash}>
          sha256 {v.hash.slice(0, 12)}
        </span>
        <span style={{ flex: "1 1 auto" }} />
        {others.length > 0 && (
          <>
            <button
              className="rk-icon-btn"
              aria-label="No diff"
              title="No diff"
              onClick={() => onAgainst(null)}
              disabled={against === null}
              data-testid="version-no-diff-pane"
            >
              <NoDiffIcon />
            </button>
            <span className="rk-field" style={{ width: 170 }}>
              <select
                aria-label="Diff against"
                value={against ?? ""}
                onChange={(e) =>
                  onAgainst(e.target.value ? Number(e.target.value) : null)
                }
                data-testid="version-against"
              >
                <option value="">No diff: the script</option>
                {[...others].reverse().map((x) => (
                  <option key={x.n} value={x.n}>
                    Diff against v{x.n}
                  </option>
                ))}
              </select>
            </span>
          </>
        )}
      </div>
      <section
        style={{
          padding: "10px 12px",
          borderRadius: 8,
          border: "1px solid var(--line)",
          background: "var(--surface)",
          fontSize: 12.5,
          display: "flex",
          flexDirection: "column",
          gap: 6,
        }}
        data-testid="version-review"
      >
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <h4 className="rk-h" style={{ margin: 0 }}>
            Sherlock review
          </h4>
          <span style={{ color: "var(--fg3)" }}>advice: it never approves</span>
          <span style={{ flex: "1 1 auto" }} />
          <button
            className="rk-btn"
            onClick={review}
            disabled={reviewing}
            data-testid="version-review-ask"
          >
            {reviewing
              ? "Reading…"
              : v.review
                ? "Review again"
                : "Ask Sherlock"}
          </button>
        </div>
        {reviewErr && (
          <p className="tb-err" style={{ margin: 0 }}>
            {reviewErr}
          </p>
        )}
        {v.review && <p style={{ margin: 0 }}>{v.review.summary}</p>}
        {(v.review?.findings ?? []).map((f, i) => (
          <p key={i} style={{ margin: 0 }}>
            <Pill tone={f.level === "error" ? "err" : "warn"}>{f.level}</Pill>{" "}
            {f.line ? `line ${f.line}: ` : ""}
            {f.msg}
          </p>
        ))}
        {v.header.target === "kubernetes" && (
          <p style={{ margin: 0 }} data-testid="version-watson">
            What Watson will run: in Kubernetes, only{" "}
            {(v.header.k8s ?? [])
              .map((a) => `${a.verb} ${a.resource}`)
              .join(", ") || "nothing"}
            {v.header.runsAs ? `, as ${v.header.runsAs}` : ", as the person"}.
            The proxy refuses anything else, and each command is audited as
            “Watson via {script.name} v{v.n}, run #…”.
          </p>
        )}
      </section>
      {diff ? (
        <>
          <p
            style={{ margin: 0, fontSize: 12.5, color: "var(--fg2)" }}
            data-testid="version-diff-summary"
          >
            {diff.text
              ? `v${other?.n} → v${v.n}: ${diff.added} added, ${diff.removed} removed`
              : `v${other?.n} and v${v.n} are the same.`}
          </p>
          {diff.text && (
            <pre
              className="rk-code rk-mono"
              style={paneStyle}
              data-testid="version-diff-text"
            >
              <DiffLines text={diff.text} />
            </pre>
          )}
        </>
      ) : (
        <CodeView
          text={v.text}
          testId="version-text"
          maxHeight={paneStyle.maxHeight}
        />
      )}
    </div>
  );
}

// ---------- running ----------

function RunDialog({
  cluster,
  script,
  dryRun: dry0,
  scripts,
  off,
  maxTimeout,
  onClose,
  onStarted,
}: {
  cluster: string;
  script: string;
  dryRun: boolean;
  scripts: ScriptSummary[];
  off: string;
  maxTimeout: number;
  onClose: () => void;
  onStarted: (r: Run) => void;
}) {
  const [sc, setSc] = useState<Script | null>(null);
  const [fleets, setFleets] = useState<AutomationFleets | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [wantDry, setWantDry] = useState(dry0);
  const [versionN, setVersionN] = useState<number | null>(null);
  const [profiles, setProfiles] = useState<RunProfile[]>([]);
  const [profileId, setProfileId] = useState("");
  const [drain, setDrain] = useState(false);
  const [hookBefore, setHookBefore] = useState("");
  const [hookAfter, setHookAfter] = useState("");
  const [fleetId, setFleetId] = useState(""); // CUSTOM_NODES: picked by hand
  const [listed, setListed] = useState(""); // the fleet whose nodes the dialog lists
  const [picked, setPicked] = useState<string[]>([]);
  const [overrides, setOverrides] = useState<Record<string, string>>({});
  const [mode, setMode] = useState<RolloutMode>("canary");
  const [size, setSize] = useState(5);
  const [stopAfter, setStopAfter] = useState(1);
  const [timeout, setTimeout_] = useState(10);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    let gone = false;
    Promise.all([fetchScript(script), fetchFleets(cluster), fetchProfiles()])
      .then(([s, f, c]) => {
        if (gone) return;
        setProfiles(c.profiles);
        setSc(s);
        setFleets(f);
        setFleetId(f.fleets[0]?.id ?? "");
        setListed(f.fleets[0]?.id ?? "");
        setPicked(pairedNames(f.fleets[0]));
      })
      .catch((e: unknown) => !gone && setLoadErr(errText(e)));
    return () => {
      gone = true;
    };
  }, [script, cluster]);

  const approvedVersion = sc ? approvedOf(sc) : undefined;
  const newestFirst = [...(sc?.versions ?? [])].reverse();
  const canDry = newestFirst.some((v) => v.header.dryRun);
  const dryRun = wantDry && canDry;
  // Off production a run takes the version chosen, approved or not, and
  // starts from the newest; a rejected version never runs. On production
  // only approved versions run, starting from the newest approved.
  const anyVersion = fleets?.production === false;
  const offered = dryRun
    ? newestFirst.filter((v) => v.header.dryRun)
    : anyVersion
      ? newestFirst.filter((v) => v.state !== "rejected")
      : newestFirst.filter((v) => v.state === "approved");
  const version =
    offered.find((v) => v.n === versionN) ??
    (dryRun || anyVersion ? offered[0] : (approvedVersion ?? offered[0]));
  const onCluster = version?.header.target === "kubernetes";
  const hookScripts = scripts.filter(
    (x) => x.target === "kubernetes" && x.approved,
  );
  const everyNode = fleets?.fleets.find((f) => f.id === ALL_NODES);
  const custom = fleetId === CUSTOM_NODES;
  // Picked by hand, the run is no fleet's: it takes All nodes' values and ceiling.
  const fleet = custom
    ? everyNode
    : fleets?.fleets.find((f) => f.id === fleetId);
  const nodes = pairedNames(everyNode).filter((n) => picked.includes(n));
  const leftOut = custom
    ? 0
    : (fleet?.nodes ?? []).filter((n) => !n.paired).length;
  const listedFleet = fleets?.fleets.find((f) => f.id === listed);
  const pickFleet = (id: string) => {
    setFleetId(id);
    setListed(id);
    setPicked(pairedNames(fleets?.fleets.find((f) => f.id === id)));
  };
  const toggleNode = (name: string, on: boolean) => {
    const next = on ? [...picked, name] : picked.filter((x) => x !== name);
    setPicked(next);
    // The list stays as it is under the pointer, and its own fleet is
    // named first when another fleet reaches the same nodes.
    setFleetId(
      fleetOf(
        [...(listedFleet ? [listedFleet] : []), ...(fleets?.fleets ?? [])],
        next,
      ),
    );
  };
  const params = version?.header.params ?? [];
  const runValues = Object.fromEntries(
    Object.entries(overrides).filter(([, v]) => v !== ""),
  );
  const missing = missingParams(
    params,
    fleets?.fleets ?? [],
    onCluster ? [""] : nodes,
    runValues,
  );
  const clashes = onCluster
    ? []
    : paramClashes(params, fleets?.fleets ?? [], nodes, runValues);
  const plan = batches(nodes, { mode, size });
  const runAs = runProfilesFor(profiles, {
    onCluster,
    dryRun,
    privilege: version?.header.privilege ?? "",
    fleet: fleet?.id,
    fp: fleet?.params,
  });
  const profile =
    runAs.options.find((c) => c.id === profileId) ?? runAs.fit;
  const blocked: string[] = [];
  if (off && !onCluster) blocked.push(off);
  if (fleets?.readOnly) blocked.push(fleets.readOnly);
  if (!version)
    blocked.push(
      dryRun
        ? "The script has no version."
        : "No version is approved: submit one and have it approved, or run a dry run.",
    );
  else {
    if ((version.findings ?? []).some((f) => f.level === "error"))
      blocked.push(`v${version.n} has errors to fix first.`);
    if (!dryRun && !anyVersion && !runnable(version))
      blocked.push(
        `v${version.n} was approved only by the person who wrote it: someone else must approve it.`,
      );
    if (
      !dryRun &&
      !onCluster &&
      !allows(fleet?.params, version.header.privilege)
    )
      blocked.push(
        `The script needs privilege elevated; the ceiling of fleet ${fleet?.name} is ${ceilingText("none")}: read-only. Raise it under Variables.`,
      );
  }
  if (version && profiles.length && !profile)
    blocked.push("No run profile fits this script on this fleet.");
  if (nodes.length === 0 && !onCluster)
    blocked.push("No paired node is picked.");
  if (clashes.length)
    blocked.push(
      `Defined on more than one fleet: ${clashes.slice(0, 4).join("; ")}${clashes.length > 4 ? "…" : ""}. Keep it on one fleet, or set it on the node or below.`,
    );
  if (missing.length)
    blocked.push(
      `Required parameters have no value: ${missing.slice(0, 4).join("; ")}${missing.length > 4 ? "…" : ""}`,
    );
  if (mode === "size" && size < 1)
    blocked.push("A batch has at least one node.");

  // A real run asks first; a dry run changes nothing.
  const [confirming, setConfirming] = useState(false);
  const start = async () => {
    if (!fleet || !version) return;
    setBusy(true);
    setErr("");
    try {
      const r = await startRun(cluster, {
        script,
        version: version.n,
        dryRun,
        fleet: onCluster ? "_cluster" : fleet.id,
        nodes: onCluster ? undefined : nodes,
        config: profile?.id,
        hooks: onCluster
          ? undefined
          : {
              before: [
                ...(drain ? ["drain"] : []),
                ...(hookBefore ? [`script:${hookBefore}`] : []),
              ],
              after: [
                ...(hookAfter ? [`script:${hookAfter}`] : []),
                ...(drain ? ["uncordon"] : []),
              ],
            },
        params: Object.keys(runValues).length ? runValues : undefined,
        rollout: {
          mode,
          size: mode === "all" ? undefined : size,
          stopAfter,
          timeoutMinutes: timeout,
        },
      });
      onStarted(r);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  const first = nodes[0];
  const resolved = resolveParams(
    params,
    fleets?.fleets ?? [],
    first ?? "",
    runValues,
  );

  return (
    <>
    <Drawer
      label="Run a script"
      kicker={dryRun ? "Dry run" : "Run"}
      title={script}
      copy={false}
      width={1400}
      onClose={onClose}
      testId="run-dialog"
      facts={
        version ? (
          <span style={{ fontSize: 12.5 }}>
            v{version.n} · privilege{" "}
            <span className="rk-mono">
              {dryRun ? "nobody (dry run)" : privilegeText(version.header.privilege)}
            </span>{" "}
            · sha256{" "}
            <span className="rk-mono">{version.hash.slice(0, 12)}</span>
          </span>
        ) : undefined
      }
      footer={
        <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          {err && (
            <p className="tb-err" style={{ margin: 0 }} data-testid="run-error">
              {err}
            </p>
          )}
          {blocked.length > 0 && (
            <ul
              className="tb-hint"
              style={{ margin: 0, paddingLeft: 18 }}
              data-testid="run-blocked"
            >
              {blocked.map((b) => (
                <li key={b}>{b}</li>
              ))}
            </ul>
          )}
          <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
            <span style={{ fontSize: 12.5, color: "var(--fg2)" }}>
              {nodes.length} {nodes.length === 1 ? "node" : "nodes"} in{" "}
              {plan.length} {plan.length === 1 ? "batch" : "batches"}
            </span>
            <button
              className="rk-btn"
              style={{ marginLeft: "auto" }}
              onClick={onClose}
            >
              Cancel
            </button>
            <button
              className="rk-btn"
              data-on
              onClick={() => (dryRun ? void start() : setConfirming(true))}
              disabled={busy || blocked.length > 0}
              data-testid="run-start"
            >
              {busy
                ? "Starting…"
                : dryRun
                  ? "Start dry run"
                  : `Run v${version?.n ?? ""}`}
            </button>
          </div>
        </div>
      }
    >
      {loadErr && <p className="tb-err">{loadErr}</p>}
      {sc && fleets && (
        <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
          <div
            style={{
              display: "flex",
              gap: 16,
              flexWrap: "wrap",
              alignItems: "flex-end",
            }}
          >
            <Field
              label="Kind"
              hint={
                canDry
                  ? undefined
                  : "This script does not declare dry-run: yes, so it has no dry run."
              }
            >
              <Seg
                label="Kind of run"
                value={dryRun ? "dry" : "run"}
                onChange={(v) => setWantDry(v === "dry")}
                options={[
                  ...(canDry ? [{ id: "dry" as const, label: "Dry run" }] : []),
                  { id: "run" as const, label: "Run" },
                ]}
              />
            </Field>
            <Field label="Version">
              <span className="rk-field" style={{ width: 170 }}>
                <select
                  value={version?.n ?? ""}
                  disabled={offered.length < 2}
                  onChange={(e) => setVersionN(Number(e.target.value))}
                  data-testid="run-version"
                >
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
                <select
                  value={profile?.id ?? ""}
                  onChange={(e) => setProfileId(e.target.value)}
                  disabled={runAs.options.length < 2}
                  data-testid="run-config"
                >
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
                  <select
                    value={fleetId}
                    onChange={(e) => pickFleet(e.target.value)}
                    data-testid="run-fleet"
                  >
                    {custom && (
                      <option value={CUSTOM_NODES}>
                        Custom selection ({nodes.length})
                      </option>
                    )}
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
            <p
              className="rk-callout"
              data-tone="info"
              style={{ margin: 0, fontSize: 12.5 }}
              data-testid="run-on-cluster"
            >
              This script acts on the cluster, not on nodes. Watson runs its
              kubectl lines
              {version?.header.runsAs
                ? ` as ${version.header.runsAs}`
                : " as you"}
              , and only the actions it declares:{" "}
              {(version?.header.k8s ?? [])
                .map((a) => `${a.verb} ${a.resource}`)
                .join(", ")}
              .
            </p>
          )}
          {fleets.production && !dryRun && (
            <p
              className="rk-callout"
              data-tone="warn"
              style={{ margin: 0, fontSize: 12.5 }}
              data-testid="run-production"
            >
              This is a production cluster: only an approved version runs.
            </p>
          )}
          {anyVersion &&
            !dryRun &&
            version?.state !== "approved" &&
            version && (
              <p
                className="rk-callout"
                data-tone="info"
                style={{ margin: 0, fontSize: 12.5 }}
                data-testid="run-unapproved"
              >
                v{version.n} is {version.state}, not approved. This cluster is
                not production, so it can run anyway.
              </p>
            )}
          {!onCluster && (
            <div>
              <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                Nodes{" "}
                <span
                  style={{ fontWeight: 400, color: "var(--fg3)" }}
                  data-testid="run-node-count"
                >
                  {nodes.length} of {listedFleet?.nodes.length ?? 0} selected
                </span>
              </h3>
              <ScrollBox
                label="Nodes"
                style={{ maxHeight: 190, overflowY: "auto" }}
              >
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns:
                      "repeat(auto-fill, minmax(220px, 1fr))",
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
                        display: "flex",
                        gap: 6,
                        alignItems: "center",
                        color: n.paired ? "var(--fg)" : "var(--fg3)",
                      }}
                    >
                      <input
                        type="checkbox"
                        disabled={!n.paired}
                        checked={n.paired && nodes.includes(n.name)}
                        onChange={(e) => toggleNode(n.name, e.target.checked)}
                      />
                      <span className="rk-mono">{n.name}</span>
                      {!n.paired && (
                        <span style={{ fontSize: 11.5 }}>not paired</span>
                      )}
                    </label>
                  ))}
                </div>
              </ScrollBox>
              {custom && (
                <p
                  className="tb-hint"
                  style={{ margin: "6px 0 0" }}
                  data-testid="run-custom"
                >
                  Picked by hand, this run is not configured for a fleet: it
                  takes the values and the privilege ceiling of All nodes.
                  Choose a fleet to go back to its nodes.
                </p>
              )}
              {leftOut > 0 && (
                <p
                  className="tb-hint"
                  style={{ margin: "6px 0 0" }}
                  data-testid="run-left-out"
                >
                  {leftOut} of this fleet’s nodes {leftOut === 1 ? "is" : "are"}{" "}
                  not paired for SSH and will be left out.
                </p>
              )}
            </div>
          )}
          {!onCluster && (
            <div data-testid="run-hooks">
              <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                Around each node
              </h3>
              <label style={{ display: "flex", gap: 6, fontSize: 12.5 }}>
                <input
                  type="checkbox"
                  checked={drain}
                  onChange={(e) => setDrain(e.target.checked)}
                  data-testid="run-hook-drain"
                />
                Cordon and drain each node first, and uncordon it after a
                healthy result. A node that fails stays cordoned.
              </label>
              {hookScripts.length > 0 && (
                <div
                  style={{
                    display: "flex",
                    flexWrap: "wrap",
                    gap: 12,
                    marginTop: 6,
                  }}
                >
                  <Field label="Before each node">
                    <span className="rk-field" style={{ width: 230 }}>
                      <select
                        value={hookBefore}
                        onChange={(e) => setHookBefore(e.target.value)}
                        data-testid="run-hook-before"
                      >
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
                      <select
                        value={hookAfter}
                        onChange={(e) => setHookAfter(e.target.value)}
                        data-testid="run-hook-after"
                      >
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
              <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                Parameters
              </h3>
              <p className="tb-hint" style={{ margin: "0 0 6px" }}>
                A value here is this run’s and wins over the node’s and the
                fleet’s. Left empty, each node takes its own value, then the
                fleet’s{first ? `; shown for ${first}` : ""}.
              </p>
              <table className="tb-table" data-testid="run-params">
                <tbody>
                  {resolved.map((p) => (
                    <tr key={p.name}>
                      <td className="rk-mono" style={{ whiteSpace: "nowrap" }}>
                        {p.name}
                        {p.required && (
                          <span style={{ color: "var(--err)" }}> *</span>
                        )}
                        {p.desc && (
                          <div
                            style={{
                              color: "var(--fg3)",
                              fontSize: 12,
                              fontFamily: "inherit",
                            }}
                          >
                            {p.desc}
                          </div>
                        )}
                      </td>
                      <td style={{ width: "50%" }}>
                        <span className="rk-field">
                          <input
                            value={overrides[p.name] ?? ""}
                            onChange={(e) =>
                              setOverrides({
                                ...overrides,
                                [p.name]: e.target.value,
                              })
                            }
                            placeholder={
                              secretOf(params, p.name) && !p.value
                                ? "secret:namespace/name/key"
                                : p.source && p.source !== "run"
                                  ? p.value
                                  : p.required
                                    ? "required"
                                    : "empty"
                            }
                            aria-label={p.name}
                            data-testid={`run-param-${p.name}`}
                          />
                        </span>
                      </td>
                      <td style={{ color: "var(--fg3)", whiteSpace: "nowrap" }}>
                        {p.conflict ? (
                          <span
                            style={{ color: "var(--err)" }}
                            data-testid="run-param-conflict"
                          >
                            on {p.conflict.join(" and ")}
                          </span>
                        ) : p.from ? (
                          `from ${p.from}`
                        ) : p.source ? (
                          `from the ${p.source}`
                        ) : p.required ? (
                          "no value"
                        ) : (
                          ""
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div>
            <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
              Rollout
            </h3>
            <div
              style={{
                display: "flex",
                flexWrap: "wrap",
                gap: "12px 32px",
                alignItems: "flex-start",
              }}
            >
              <fieldset
                style={{
                  flex: "1 1 340px",
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
                    <input
                      type="radio"
                      name="run-rollout"
                      checked={mode === id}
                      onChange={() => setMode(id)}
                      data-testid={`run-rollout-${id}`}
                    />
                    <span>
                      <b style={{ fontWeight: 500 }}>{label}</b>
                      <small>{rolloutHelp(id, size)}</small>
                    </span>
                  </label>
                ))}
              </fieldset>
              <div
                style={{
                  flex: "0 0 auto",
                  display: "grid",
                  gridTemplateColumns: "190px 90px",
                  gap: "8px 12px",
                  alignItems: "center",
                  fontSize: 12.5,
                }}
                data-testid="run-rollout-inputs"
              >
                {mode !== "all" && (
                  <RolloutInput
                    label={mode === "canary" ? "Then batches of" : "Batch size"}
                    hint={
                      mode === "canary" ? "0 runs the rest together" : undefined
                    }
                    min={mode === "canary" ? 0 : 1}
                    value={size}
                    onChange={setSize}
                    testId="run-size"
                  />
                )}
                <RolloutInput
                  label="Stop after failures"
                  hint="Failed or unreachable nodes; 0 sets no limit"
                  min={0}
                  value={stopAfter}
                  onChange={setStopAfter}
                  testId="run-stop-after"
                />
                <RolloutInput
                  label="Timeout per node (min)"
                  min={1}
                  max={maxTimeout}
                  value={timeout}
                  onChange={setTimeout_}
                  testId="run-timeout"
                />
              </div>
            </div>
            {plan.length > 1 && (
              <p
                className="tb-hint"
                style={{ margin: "4px 0 0" }}
                data-testid="run-plan"
              >
                {plan
                  .map(
                    (b, i) =>
                      `${i + 1}: ${b.length === 1 ? b[0] : `${b.length} nodes`}`,
                  )
                  .join(" → ")}
                .
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
            setConfirming(false);
            void start();
          }}
          onCancel={() => setConfirming(false)}
        >
          <p style={{ margin: 0 }}>
            {onCluster
              ? "It runs on the cluster"
              : `It runs on ${nodes.length} ${nodes.length === 1 ? "node" : "nodes"} of ${fleet?.name ?? "the fleet"}, ${plan.length} ${plan.length === 1 ? "batch" : "batches"}`}
            {profile ? `, as ${profile.name}` : ""}
            . This is not a dry run: it changes what the script changes.
            {version.state === "approved"
              ? ""
              : " This version is not approved, so it runs only off production."}
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

const secretOf = (params: Param[], name: string) =>
  params.some((p) => p.name === name && p.secret);

/** One of the numbers beside the rollout, its label left of its box. */
function RolloutInput({
  label,
  hint,
  min,
  max,
  value,
  onChange,
  testId,
}: {
  label: string;
  hint?: string;
  min: number;
  max?: number;
  value: number;
  onChange: (v: number) => void;
  testId: string;
}) {
  return (
    <label style={{ display: "contents" }}>
      <span>
        {label}
        {hint && (
          <small style={{ display: "block", color: "var(--fg3)" }}>
            {hint}
          </small>
        )}
      </span>
      <span className="rk-field">
        <input
          type="number"
          min={min}
          max={max}
          value={value}
          onChange={(e) =>
            onChange(
              Math.min(
                max ?? Infinity,
                Math.max(min, Number(e.target.value) || min),
              ),
            )
          }
          data-testid={testId}
        />
      </span>
    </label>
  );
}

/** A run's nodes counted by state. */
export function tally(r: Run): Partial<Record<NodeState, number>> {
  const m: Partial<Record<NodeState, number>> = {};
  for (const n of r.nodes) m[n.state] = (m[n.state] ?? 0) + 1;
  return m;
}

function duration(from: string, to: string | undefined, now: number): string {
  const s = Math.max(
    0,
    Math.round(
      ((to ? new Date(to).getTime() : now) - new Date(from).getTime()) / 1000,
    ),
  );
  return s < 60
    ? `${s} s`
    : s < 3600
      ? `${Math.floor(s / 60)} min ${s % 60} s`
      : `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}

function RunDrawer({
  id,
  now,
  onRetried,
  onClose,
  onToast,
  onEnded,
}: {
  id: number;
  now: number;
  onRetried: (r: Run) => void;
  onClose: () => void;
  onToast: (msg: string) => void;
  onEnded: () => void;
}) {
  const [d, setD] = useState<RunDetail | null>(null);
  const [err, setErr] = useState("");
  const [node, setNode] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const wasLive = useRef(false);
  const ended = useRef(onEnded);
  useEffect(() => {
    ended.current = onEnded;
  });
  const live = d?.live ?? true;

  useEffect(() => {
    let gone = false;
    const get = (background: boolean) =>
      fetchRun(id, background)
        .then((x) => {
          if (gone) return;
          setD(x);
          setErr("");
          if (wasLive.current && !x.live) ended.current();
          wasLive.current = x.live;
        })
        .catch((e: unknown) => !gone && setErr(errText(e)));
    void get(false);
    const t = live ? setInterval(() => void get(true), 2000) : undefined;
    return () => {
      gone = true;
      clearInterval(t);
    };
  }, [id, live]);

  const run = d?.run;
  const sel =
    node ??
    run?.nodes.find((n) => n.state === "failed" || n.state === "running")
      ?.node ??
    run?.nodes[0]?.node;
  const out = sel
    ? (d?.output?.[sel] ?? run?.nodes.find((n) => n.node === sel)?.tail ?? "")
    : "";
  const params = Object.entries(
    run?.nodes.find((n) => n.node === sel)?.values ?? {},
  );
  const failedNodes = (run?.nodes ?? []).filter(
    (n) => n.state === "failed" || n.state === "unreachable",
  ).length;
  return (
    <>
      <Drawer
        label="Run"
        kicker={
          run
            ? `${run.dryRun ? "Dry run" : "Run"} on ${run.clusterName || run.cluster} › ${run.fleetName || run.fleet}`
            : "Run"
        }
        title={run ? `#${run.id} ${run.script} v${run.version}` : `#${id}`}
        copy={false}
        width={1400}
        onClose={onClose}
        testId="run-drawer"
        tools={
          d?.live ? (
            <button
              className="rk-btn"
              data-danger="true"
              onClick={() => setConfirm(true)}
              data-testid="run-cancel"
            >
              Cancel run
            </button>
          ) : run && failedNodes > 0 ? (
            <button
              className="rk-btn"
              onClick={() =>
                void retryRun(run.cluster, run.id).then(
                  onRetried,
                  (e: unknown) => setErr(errText(e)),
                )
              }
              data-testid="run-retry"
            >
              Retry {failedNodes} failed
            </button>
          ) : undefined
        }
        facts={
          run ? (
            <span
              style={{
                display: "flex",
                gap: 8,
                flexWrap: "wrap",
                alignItems: "center",
                fontSize: 12.5,
              }}
            >
              <Pill tone={runTone(run.state)} testId="run-state">
                {run.state}
              </Pill>
              <span>{countsText(tally(run))}</span>
              <span style={{ color: "var(--fg3)" }}>
                by {run.by.name} · {fmtTs(run.started)} ·{" "}
                {duration(run.started, run.ended, now)} · privilege{" "}
                {run.privilege} · rollout {run.rollout.mode}
                {run.rollout.size ? ` ${run.rollout.size}` : ""}
              </span>
            </span>
          ) : undefined
        }
        notice={
          run?.note ? (
            <p
              className="rk-callout"
              data-tone={run.state === "succeeded" ? "info" : "warn"}
              style={{ margin: 0, fontSize: 12.5 }}
            >
              {run.note}
            </p>
          ) : undefined
        }
      >
        {err && <p className="tb-err">{err}</p>}
        {run && params.length > 0 && (
          <section
            style={{
              padding: "12px 16px",
              borderRadius: 8,
              border: "1px solid var(--line)",
              borderLeft: "3px solid var(--accent)",
              background: "var(--surface)",
            }}
            data-testid="run-parameters"
          >
            <h3 className="rk-h" style={{ margin: "0 0 2px" }}>
              Parameters
            </h3>
            <p className="tb-hint" style={{ margin: "0 0 8px" }}>
              What this run gave the script{sel ? ` on ${sel}` : ""}.
            </p>
            <dl className="rk-kv">
              {params.map(([k, v]) => (
                <div key={k} style={{ display: "contents" }}>
                  <dt className="rk-mono">{k}</dt>
                  <dd className="rk-mono">{v}</dd>
                </div>
              ))}
            </dl>
          </section>
        )}
        {run && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="run-watson">
            Watson via {run.script} v{run.version}, run #{run.id}
            {run.basis ? `: ${run.basis}` : ""}
            {run.config ? ` · run as ${profileName(run.config)}` : ""}
            {run.identity ? ` · as ${run.identity}` : ""}
            {run.hooks?.length ? ` · ${run.hooks.join(", ")}` : ""}
            {run.retryOf ? ` · retries run ${run.retryOf}` : ""}
          </p>
        )}
        {run && (
          <h3
            className="rk-h"
            style={{
              margin: 0,
              paddingTop: params.length > 0 ? 12 : 0,
              borderTop:
                params.length > 0 ? "1px solid var(--line)" : undefined,
            }}
            data-testid="run-results"
          >
            Results
          </h3>
        )}
        {run && (
          <div
            style={{
              display: "flex",
              gap: 16,
              flexWrap: "wrap",
              alignItems: "flex-start",
            }}
          >
            <table
              className="tb-table"
              style={{ flex: "1 1 380px", width: "auto" }}
              data-testid="run-nodes-table"
            >
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Batch</th>
                  <th>Result</th>
                  <th>Exit</th>
                </tr>
              </thead>
              <tbody>
                {run.nodes.map((n) => (
                  <tr
                    key={n.node}
                    data-sel={sel === n.node}
                    onClick={() => setNode(n.node)}
                    style={{
                      cursor: "pointer",
                      background:
                        sel === n.node ? "var(--accent-weak)" : undefined,
                    }}
                    data-testid="run-node"
                  >
                    <td className="rk-mono">{n.node}</td>
                    <td>{n.batch + 1}</td>
                    <td>
                      <Pill tone={nodeTone(n.state)}>
                        {NODE_LABEL[n.state]}
                      </Pill>
                      {(n.msg || n.error) && (
                        <div
                          style={{
                            fontSize: 12,
                            color: n.error ? "var(--err)" : "var(--fg2)",
                          }}
                        >
                          {n.error || n.msg}
                        </div>
                      )}
                    </td>
                    <td className="rk-mono">{n.exit ?? ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div style={{ flex: "2 1 420px", minWidth: 0 }}>
              <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                Output{sel ? ` of ${sel}` : ""}
              </h3>
              <pre
                className="rk-mono"
                style={{
                  margin: 0,
                  padding: 12,
                  background: "var(--sunken)",
                  borderRadius: 8,
                  fontSize: 12,
                  overflow: "auto",
                  maxHeight: 480,
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-word",
                }}
                data-testid="run-output"
              >
                {out || "No output."}
              </pre>
            </div>
          </div>
        )}
      </Drawer>
      {confirm && (
        <ConfirmDialog
          title={`Cancel run ${id}?`}
          confirm="Cancel run"
          danger
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setBusy(true);
            cancelRun(id)
              .then(() => {
                onToast(
                  `Run ${id} is cancelled: nodes running stop, waiting nodes do not start.`,
                );
                setConfirm(false);
              })
              .catch((e: unknown) => setErr(errText(e)))
              .finally(() => setBusy(false));
          }}
        >
          <p style={{ margin: 0 }}>
            Scripts running now get SIGTERM; no node that has not started will
            start. What a script already changed stays changed.
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

const RUN_SORT: SortCol<Run>[] = [
  { id: "id", sortValue: (r) => r.id, defaultDir: "desc" },
  { id: "script", sortValue: (r) => r.script },
  {
    id: "where",
    sortValue: (r) =>
      `${r.clusterName || r.cluster} ${r.fleetName || r.fleet}`,
  },
  { id: "by", sortValue: (r) => r.by.name },
  { id: "state", sortValue: (r) => r.state },
  { id: "nodes", sortValue: (r) => r.nodes.length, defaultDir: "desc" },
  { id: "started", sortValue: (r) => r.started, defaultDir: "desc" },
  {
    id: "took",
    sortValue: (r, now) =>
      (r.ended ? Date.parse(r.ended) : now) - Date.parse(r.started),
    defaultDir: "desc",
  },
];

function RunsSection({
  runs,
  now,
  onOpen,
}: {
  runs: Run[];
  now: number;
  onOpen: (id: number) => void;
}) {
  const [q, setQ] = useState("");
  const picked = runs.filter(
    (r) =>
      !q ||
      r.script.includes(q.toLowerCase()) ||
      (r.fleetName ?? r.fleet).toLowerCase().includes(q.toLowerCase()) ||
      r.by.name.toLowerCase().includes(q.toLowerCase()) ||
      String(r.id) === q,
  );
  const { sort, onSort, shown } = useSorted(
    "runs",
    RUN_SORT,
    picked,
    { col: "id", dir: "desc" },
    now,
  );
  const th = (col: string, label: string) => (
    <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  );
  return (
    <>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 12,
          flexWrap: "wrap",
        }}
      >
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Run History</h2>
        <label className="rk-field" style={{ width: 260 }}>
          <SearchIcon />
          <span className="rk-sr">Find a run</span>
          <input
            type="search"
            placeholder="Find a script, fleet, person or #"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            data-testid="runs-search"
          />
        </label>
      </div>
      {runs.length === 0 ? (
        <p className="tb-hint" data-testid="runs-empty">
          Nothing has run yet. Dry run a script from Scripts to see what it
          would change.
        </p>
      ) : (
        <div className="rk-card" style={{ padding: 0, overflow: "auto" }}>
          <table className="tb-table" data-testid="runs-table">
            <thead>
              <tr>
                {th("id", "#")}
                {th("script", "Script")}
                {th("where", "Where")}
                {th("by", "By")}
                {th("state", "State")}
                {th("nodes", "Nodes")}
                {th("started", "Started")}
                {th("took", "Took")}
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <tr
                  key={r.id}
                  onClick={() => onOpen(r.id)}
                  style={{ cursor: "pointer" }}
                  data-testid="run-row"
                >
                  <td>
                    <button className="rk-link" onClick={() => onOpen(r.id)}>
                      {r.id}
                    </button>
                  </td>
                  <td>
                    <span className="rk-mono">{r.script}</span> v{r.version}
                    {r.dryRun && (
                      <>
                        {" "}
                        <Pill>dry run</Pill>
                      </>
                    )}
                  </td>
                  <td>
                    {r.clusterName || r.cluster} › {r.fleetName || r.fleet}
                  </td>
                  <td>{r.by.name}</td>
                  <td>
                    <Pill tone={runTone(r.state)}>{r.state}</Pill>
                  </td>
                  <td>{countsText(tally(r))}</td>
                  <td style={{ whiteSpace: "nowrap" }}>{fmtTs(r.started)}</td>
                  <td style={{ whiteSpace: "nowrap" }}>
                    {duration(r.started, r.ended, now)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
