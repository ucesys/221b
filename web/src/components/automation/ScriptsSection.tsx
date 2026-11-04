// The Scripts page: the groups on the left and the table of scripts.
import { useState } from "react";
import {
  scriptKind,
  privilegeText,
  formatGroups,
  inGroups,
  inState,
  parseGroups,
  SCRIPT_STATES,
  scriptGroups,
  runTone,
  type Group,
  UNGROUPED,
  versionTone,
  type AutomationIndex,
  type ScriptSummary,
} from "../../api/automation";
import { href, navigate, type Route } from "../../lib/router";
import { useSorted, type SortCol } from "../../lib/useSorted";
import { SortTh } from "./SortTh";
import {
  PencilIcon,
  PlusIcon,
  SearchIcon,
} from "../Icons";
import { Pill } from "./parts";

// ---------- scripts ----------

/** The Scripts page's filters, in the URL: the state and the groups picked. */
export function goScripts(state: string, groups: string[]) {
  navigate(href("scripts", [], { state, group: formatGroups(groups) }), true);
}

/**
 * The groups beside the Scripts table, as the fleets are beside the Nodes
 * table: a group's name shows it alone, and its box adds it to the others.
 */
export function ScriptGroups({
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

export const SCRIPT_SORT: SortCol<ScriptSummary>[] = [
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

export function ScriptsSection({
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

export function ScriptRow({
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
