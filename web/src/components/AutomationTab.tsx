import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  allows,
  approvedOf,
  approveVersion,
  batches,
  byGroup,
  cancelRun,
  canApprove,
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
  missingParams,
  NEW_SCRIPT,
  NODE_LABEL,
  nodeTone,
  PARAM_NAME,
  resolveParams,
  RESERVED,
  runnableOn,
  runTone,
  saveFleetParams,
  saveScript,
  SCRIPT_NAME,
  startRun,
  submitVersion,
  versionTone,
  type AutomationFleets,
  type AutomationIndex,
  type FleetParams,
  type Finding,
  type Header,
  type NodeState,
  type Privilege,
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
import { href } from "../lib/router";
import { ConfirmDialog } from "./ConfirmDialog";
import { Drawer } from "./Drawer";
import { PlusIcon, SearchIcon } from "./Icons";
import type { TabProps } from "./tabs";

type Section = "scripts" | "runs" | "params";

const SECTIONS: [Section, string, string][] = [
  [
    "scripts",
    "Scripts",
    "Shell scripts with a 221b header, their versions and approvals",
  ],
  [
    "runs",
    "Executions",
    "Every run and dry run, newest first, with each node’s result",
  ],
  [
    "params",
    "Fleet parameters",
    "Values the scripts’ parameters take on this cluster’s fleets",
  ],
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

function Seg<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { id: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="rk-seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          aria-pressed={value === o.id}
          onClick={() => onChange(o.id)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function Findings({ list }: { list: Finding[] | null | undefined }) {
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
          <span
            className="rk-mono"
            style={{ color: "var(--fg3)", minWidth: 48 }}
          >
            {f.line > 0 ? `line ${f.line}` : "header"}
          </span>
          <span>{f.msg}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The Automation tab (Phase 11 part 2, AU-01..AU-14): scripts, their
 * executions and the fleets' parameters, for a SuperAdmin.
 */
export function AutomationTab({
  route,
  onToast,
  me,
  now,
}: TabProps & { me: Me | null }) {
  const section: Section =
    (["scripts", "runs", "params"] as const).find((s) => s === route.path[0]) ??
    "scripts";
  const [index, setIndex] = useState<AutomationIndex | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<string | null | undefined>(undefined); // a name, null for a new script
  const [running, setRunning] = useState<{
    script: string;
    dryRun: boolean;
  } | null>(null);
  const [openRun, setOpenRun] = useState<number | null>(null);

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
    if (!me?.admin) return;
    let gone = false;
    void fetchAll(false).then((x) => !gone && apply(x));
    return () => {
      gone = true;
    };
  }, [fetchAll, apply, me?.admin]);
  // While a run goes on, the lists follow it.
  const live = (index?.running ?? 0) > 0;
  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => void load(true), 4000);
    return () => clearInterval(t);
  }, [live, load]);

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
      <aside
        className="rk-pane"
        aria-label="Automation"
        data-testid="automation-aside"
        style={{
          flex: "0 1 280px",
          maxWidth: "100%",
          display: "flex",
          flexDirection: "column",
          background: "var(--surface)",
          borderRight: "1px solid var(--line)",
        }}
      >
        <div
          style={{
            padding: "14px 16px 12px",
            borderBottom: "1px solid var(--line)",
          }}
        >
          <h2 className="rk-h">Automation</h2>
        </div>
        {SECTIONS.map(([s, name, tip]) => {
          const n =
            s === "scripts"
              ? index?.scripts.length
              : s === "runs"
                ? runs.length
                : undefined;
          return (
            <a
              key={s}
              className="rk-rrow"
              href={href("automation", s === "scripts" ? [] : [s])}
              data-sel={section === s}
              aria-current={section === s ? "page" : undefined}
              data-testid={`automation-section-${s}`}
              style={{
                display: "flex",
                flexDirection: "column",
                gap: 3,
                padding: "11px 16px",
                borderBottom: "1px solid var(--line)",
                textDecoration: "none",
                color: "inherit",
              }}
            >
              <span
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  fontWeight: 600,
                  fontSize: 13,
                }}
              >
                {name}
                {n !== undefined && (
                  <span style={{ color: "var(--fg3)", fontWeight: 500 }}>
                    {n}
                  </span>
                )}
                {s === "runs" && live && (
                  <Pill tone="info" testId="automation-live">
                    {index?.running} running
                  </Pill>
                )}
              </span>
              <span style={{ fontSize: 12, color: "var(--fg3)" }}>{tip}</span>
            </a>
          );
        })}
        {index?.off && (
          <p
            className="rk-callout"
            data-tone="warn"
            style={{ margin: 12, fontSize: 12.5 }}
            data-testid="automation-off"
          >
            {index.off} Scripts can be written and approved; nothing runs until
            SSH is on.
          </p>
        )}
      </aside>
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
        aria-label={SECTIONS.find((s) => s[0] === section)![1]}
      >
        {err && (
          <p className="tb-err" data-testid="automation-error">
            {err}
          </p>
        )}
        {!index && !err && <p className="tb-hint">Loading…</p>}
        {index && section === "scripts" && (
          <ScriptsSection
            index={index}
            onEdit={setEditing}
            onRun={(script, dryRun) => setRunning({ script, dryRun })}
            onOpenRun={setOpenRun}
          />
        )}
        {index && section === "runs" && (
          <RunsSection runs={runs} now={now} onOpen={setOpenRun} />
        )}
        {index && section === "params" && (
          <ParamsSection
            cluster={route.cluster}
            scripts={index.scripts}
            onToast={onToast}
          />
        )}
      </section>
      {editing !== undefined && index && (
        <ScriptDrawer
          name={editing}
          me={index.me}
          maxText={index.limits.text}
          groups={[
            ...new Set(index.scripts.map((s) => s.group).filter(Boolean)),
          ]}
          onClose={() => setEditing(undefined)}
          onChanged={(name) => {
            if (name !== undefined) setEditing(name);
            void load(true);
          }}
          onDeleted={() => {
            setEditing(undefined);
            onToast("The script is deleted.");
            void load(true);
          }}
          onRun={(script, dryRun) => setRunning({ script, dryRun })}
          onToast={onToast}
        />
      )}
      {running && index && (
        <RunDialog
          cluster={route.cluster}
          script={running.script}
          dryRun={running.dryRun}
          off={index.off}
          maxTimeout={index.limits.timeout}
          onClose={() => setRunning(null)}
          onStarted={(r) => {
            setRunning(null);
            setEditing(undefined);
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
          onClose={() => setOpenRun(null)}
          onToast={onToast}
          onEnded={() => void load(true)}
        />
      )}
    </>
  );
}

// ---------- scripts ----------

function ScriptsSection({
  index,
  onEdit,
  onRun,
  onOpenRun,
}: {
  index: AutomationIndex;
  onEdit: (name: string | null) => void;
  onRun: (script: string, dryRun: boolean) => void;
  onOpenRun: (id: number) => void;
}) {
  const [q, setQ] = useState("");
  const shown = index.scripts.filter(
    (s) =>
      !q ||
      s.name.includes(q.toLowerCase()) ||
      s.describe.toLowerCase().includes(q.toLowerCase()) ||
      s.group.toLowerCase().includes(q.toLowerCase()),
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
      {byGroup(shown).map(([group, list]) => (
        <section
          key={group}
          className="rk-card"
          style={{ padding: 0, overflow: "auto" }}
          aria-label={group}
          data-testid="scripts-group"
        >
          <h3 className="rk-h" style={{ margin: 0, padding: "10px 12px 4px" }}>
            {group}{" "}
            <span style={{ color: "var(--fg3)", fontWeight: 500 }}>
              {list.length}
            </span>
          </h3>
          <table className="tb-table">
            <thead>
              <tr>
                <th>Script</th>
                <th>Latest</th>
                <th>Runs</th>
                <th>Privilege</th>
                <th>Checks</th>
                <th>Last run</th>
                <th aria-label="Tools" />
              </tr>
            </thead>
            <tbody>
              {list.map((s) => (
                <ScriptRow
                  key={s.name}
                  s={s}
                  onEdit={onEdit}
                  onRun={onRun}
                  onOpenRun={onOpenRun}
                />
              ))}
            </tbody>
          </table>
        </section>
      ))}
    </>
  );
}

function ScriptRow({
  s,
  onEdit,
  onRun,
  onOpenRun,
}: {
  s: ScriptSummary;
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
      <td className="rk-mono">{s.privilege || "—"}</td>
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
        <button
          className="rk-btn"
          onClick={() => onRun(s.name, true)}
          disabled={s.errors > 0}
          data-testid="script-dry-run"
          title="Run the latest version with DRY_RUN=1: nothing changes"
        >
          Dry run…
        </button>{" "}
        <button
          className="rk-btn"
          onClick={() => onRun(s.name, false)}
          disabled={!s.approved}
          data-testid="script-run"
          title={
            s.approved
              ? `Run approved v${s.approved}`
              : "No version is approved yet"
          }
        >
          Run…
        </button>
      </td>
    </tr>
  );
}

function ScriptDrawer({
  name,
  me,
  maxText,
  groups,
  onClose,
  onChanged,
  onDeleted,
  onRun,
  onToast,
}: {
  name: string | null;
  me: AutomationIndex["me"];
  maxText: number;
  groups: string[];
  onClose: () => void;
  onChanged: (name?: string) => void;
  onDeleted: () => void;
  onRun: (script: string, dryRun: boolean) => void;
  onToast: (msg: string) => void;
}) {
  const [script, setScript] = useState<Script | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [tab, setTab] = useState("script");
  const [newName, setNewName] = useState("");
  const [group, setGroup] = useState("");
  const [text, setText] = useState(name === null ? NEW_SCRIPT : "");
  const [check, setCheck] = useState<{
    header: Header;
    findings: Finding[] | null;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [actErr, setActErr] = useState("");
  const [viewing, setViewing] = useState<number | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

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
  const nameBad = name === null && newName !== "" && !SCRIPT_NAME.test(newName);
  const tooLong = new TextEncoder().encode(text).length > maxText;
  const errors = (check?.findings ?? []).filter(
    (f) => f.level === "error",
  ).length;

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
      : act(
          () => saveScript(name, group, text),
          "Saved as a new draft version.",
        );

  const shownVersion =
    viewing !== null
      ? script?.versions.find((v) => v.n === viewing)
      : undefined;

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
      <button
        className="rk-btn"
        data-on
        onClick={() => void save()}
        disabled={
          busy ||
          !changed ||
          tooLong ||
          (name === null && (!newName || nameBad))
        }
        data-testid="script-save"
      >
        {name === null ? "Save draft" : "Save as new version"}
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
            busy ||
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
          onClick={() =>
            void act(
              () => approveVersion(script!.name, latest.n),
              `You approved v${latest.n}.`,
            )
          }
          disabled={busy}
          data-testid="script-approve"
        >
          Approve v{latest.n}
        </button>
      )}
      {script && (
        <>
          <button
            className="rk-btn"
            style={{ marginLeft: "auto" }}
            onClick={() => onRun(script.name, true)}
            disabled={busy || changed || errors > 0}
            data-testid="script-drawer-dry-run"
          >
            Dry run…
          </button>
          <button
            className="rk-btn"
            onClick={() => onRun(script.name, false)}
            disabled={busy || !approved}
            data-testid="script-drawer-run"
          >
            Run v{approved?.n ?? "—"}…
          </button>
          <button
            className="rk-btn"
            data-danger="true"
            onClick={() => setConfirmDelete(true)}
            disabled={busy}
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
        onClose={onClose}
        footer={tab === "script" ? footer : undefined}
        width={960}
        testId="script-drawer"
      >
        {loadErr && <p className="tb-err">{loadErr}</p>}
        {tab === "script" && (name === null || script) && (
          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            <div style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
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
                      data-testid="script-name"
                    />
                  </span>
                </Field>
              )}
              <Field
                label="Group"
                hint="Groups sort the list; a script is in one group or none."
              >
                <span className="rk-field" style={{ width: 220 }}>
                  <input
                    value={group}
                    onChange={(e) => setGroup(e.target.value)}
                    list="automation-groups"
                    placeholder="Ungrouped"
                    data-testid="script-group"
                  />
                </span>
                <datalist id="automation-groups">
                  {groups.map((g) => (
                    <option key={g} value={g} />
                  ))}
                </datalist>
              </Field>
            </div>
            <Field
              label="Script"
              hint={
                tooLong
                  ? `Over ${maxText / 1024} KiB: shorten it.`
                  : "The header lines start with # 221b: — describe, param, privilege, dry-run, facts. A saved change is a new draft version; approved versions never change."
              }
            >
              <textarea
                className="rk-area rk-mono"
                value={text}
                onChange={(e) => setText(e.target.value)}
                spellCheck={false}
                style={{
                  minHeight: 340,
                  fontSize: 12.5,
                  lineHeight: 1.5,
                  tabSize: 2,
                }}
                data-testid="script-text"
              />
            </Field>
            <div>
              <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                Checks
              </h3>
              {check ? (
                <Findings list={check.findings} />
              ) : (
                <p className="tb-hint">Checking…</p>
              )}
            </div>
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
                <span style={{ color: "var(--fg3)" }}>Privilege</span>
                <span className="rk-mono">{check.header.privilege || "—"}</span>
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
                        <span key={p.name} style={{ marginRight: 10 }}>
                          <span className="rk-mono">{p.name}</span>
                          {p.required ? " (required)" : ""}
                        </span>
                      ))}
                </span>
              </div>
            )}
          </div>
        )}
        {tab === "versions" && script && (
          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            <table className="tb-table" data-testid="script-versions">
              <thead>
                <tr>
                  <th>Version</th>
                  <th>State</th>
                  <th>Author</th>
                  <th>Approvals</th>
                  <th>SHA-256</th>
                  <th aria-label="Show" />
                </tr>
              </thead>
              <tbody>
                {[...script.versions].reverse().map((v) => (
                  <VersionRow
                    key={v.n}
                    v={v}
                    me={me}
                    busy={busy}
                    viewing={viewing === v.n}
                    onView={() => setViewing(viewing === v.n ? null : v.n)}
                    onApprove={() =>
                      void act(
                        () => approveVersion(script.name, v.n),
                        `You approved v${v.n}.`,
                      )
                    }
                  />
                ))}
              </tbody>
            </table>
            {actErr && <p className="tb-err">{actErr}</p>}
            {shownVersion && (
              <div>
                <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
                  v{shownVersion.n}
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
                    maxHeight: 360,
                  }}
                  data-testid="version-text"
                >
                  {shownVersion.text}
                </pre>
              </div>
            )}
          </div>
        )}
      </Drawer>
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
            in Executions and in the audit log.
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
  viewing,
  onView,
  onApprove,
}: {
  v: Version;
  me: AutomationIndex["me"];
  busy: boolean;
  viewing: boolean;
  onView: () => void;
  onApprove: () => void;
}) {
  return (
    <tr data-testid="version-row" data-n={v.n}>
      <td>v{v.n}</td>
      <td>
        <Pill tone={versionTone(v.state)}>{v.state}</Pill>
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
        {canApprove(v, me) && v.state !== "superseded" && (
          <button
            className="rk-btn"
            onClick={onApprove}
            disabled={busy}
            data-testid="version-approve"
            style={{ marginTop: 4 }}
          >
            Approve
          </button>
        )}
      </td>
      <td className="rk-mono" title={v.hash}>
        {v.hash.slice(0, 12)}
      </td>
      <td>
        <button className="rk-link" onClick={onView} aria-pressed={viewing}>
          {viewing ? "Hide" : "Show"}
        </button>
      </td>
    </tr>
  );
}

// ---------- running ----------

function RunDialog({
  cluster,
  script,
  dryRun: dry0,
  off,
  maxTimeout,
  onClose,
  onStarted,
}: {
  cluster: string;
  script: string;
  dryRun: boolean;
  off: string;
  maxTimeout: number;
  onClose: () => void;
  onStarted: (r: Run) => void;
}) {
  const [sc, setSc] = useState<Script | null>(null);
  const [fleets, setFleets] = useState<AutomationFleets | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [dryRun, setDryRun] = useState(dry0);
  const [fleetId, setFleetId] = useState("");
  const [picked, setPicked] = useState<string[] | null>(null); // null: every paired node
  const [overrides, setOverrides] = useState<Record<string, string>>({});
  const [mode, setMode] = useState<RolloutMode>("canary");
  const [size, setSize] = useState(5);
  const [stopAfter, setStopAfter] = useState(1);
  const [timeout, setTimeout_] = useState(10);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    let gone = false;
    Promise.all([fetchScript(script), fetchFleets(cluster)])
      .then(([s, f]) => {
        if (gone) return;
        setSc(s);
        setFleets(f);
        setFleetId(f.fleets[0]?.id ?? "");
      })
      .catch((e: unknown) => !gone && setLoadErr(errText(e)));
    return () => {
      gone = true;
    };
  }, [script, cluster]);

  const version = sc ? (dryRun ? latestOf(sc) : approvedOf(sc)) : undefined;
  const fleet = fleets?.fleets.find((f) => f.id === fleetId);
  const paired = (fleet?.nodes ?? [])
    .filter((n) => n.paired)
    .map((n) => n.name);
  const nodes =
    picked === null ? paired : picked.filter((n) => paired.includes(n));
  const params = version?.header.params ?? [];
  const runValues = Object.fromEntries(
    Object.entries(overrides).filter(([, v]) => v !== ""),
  );
  const missing = missingParams(params, fleet?.params, nodes, runValues);
  const plan = batches(nodes, { mode, size });
  const blocked: string[] = [];
  if (off) blocked.push(off);
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
    if (!dryRun && fleets && !runnableOn(version, fleets.twoPerson))
      blocked.push(
        `v${version.n} needs a second approval, by someone other than its author: this cluster has the two-person rule.`,
      );
    if (!dryRun && !allows(fleet?.params, version.header.privilege))
      blocked.push(
        `The script needs privilege full; fleet ${fleet?.name} allows only none. Raise its ceiling under Fleet parameters.`,
      );
    if (dryRun && !version.header.dryRun)
      blocked.push(
        `v${version.n} does not declare dry-run: yes, so a dry run could change things.`,
      );
  }
  if (nodes.length === 0) blocked.push("No paired node is picked.");
  if (missing.length)
    blocked.push(
      `Required parameters have no value: ${missing.slice(0, 4).join("; ")}${missing.length > 4 ? "…" : ""}`,
    );
  if (mode === "size" && size < 1)
    blocked.push("A batch has at least one node.");

  const start = async () => {
    if (!fleet || !version) return;
    setBusy(true);
    setErr("");
    try {
      const r = await startRun(cluster, {
        script,
        version: dryRun ? version.n : undefined,
        dryRun,
        fleet: fleet.id,
        nodes: picked === null ? undefined : nodes,
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
  const resolved = first
    ? resolveParams(params, fleet?.params, first, runValues)
    : [];

  return (
    <Drawer
      label="Run a script"
      kicker={dryRun ? "Dry run" : "Run"}
      title={script}
      copy={false}
      width={820}
      onClose={onClose}
      testId="run-dialog"
      facts={
        version ? (
          <span style={{ fontSize: 12.5 }}>
            v{version.n} · privilege{" "}
            <span className="rk-mono">
              {dryRun ? "none (dry run)" : version.header.privilege}
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
              onClick={() => void start()}
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
            <Field label="Kind">
              <Seg
                label="Kind of run"
                value={dryRun ? "dry" : "run"}
                onChange={(v) => setDryRun(v === "dry")}
                options={[
                  { id: "dry", label: "Dry run (latest version)" },
                  { id: "run", label: "Run (approved version)" },
                ]}
              />
            </Field>
            <Field label="Fleet">
              <span className="rk-field" style={{ width: 240 }}>
                <select
                  value={fleetId}
                  onChange={(e) => {
                    setFleetId(e.target.value);
                    setPicked(null);
                  }}
                  data-testid="run-fleet"
                >
                  {fleets.fleets.map((f) => (
                    <option key={f.id} value={f.id}>
                      {f.name} ({f.nodes.length})
                    </option>
                  ))}
                </select>
              </span>
            </Field>
          </div>
          {fleets.production && !dryRun && (
            <p
              className="rk-callout"
              data-tone="warn"
              style={{ margin: 0, fontSize: 12.5 }}
              data-testid="run-production"
            >
              This is a production cluster
              {fleets.twoPerson
                ? " with the two-person rule: the version runs only with two approvals, one not its author’s"
                : ""}
              .
            </p>
          )}
          <div>
            <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
              Nodes
            </h3>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))",
                gap: 4,
                fontSize: 12.5,
              }}
              data-testid="run-nodes"
            >
              {(fleet?.nodes ?? []).map((n) => (
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
                    onChange={(e) => {
                      const cur = picked ?? paired;
                      setPicked(
                        e.target.checked
                          ? [...cur, n.name]
                          : cur.filter((x) => x !== n.name),
                      );
                    }}
                  />
                  <span className="rk-mono">{n.name}</span>
                  {!n.paired && (
                    <span style={{ fontSize: 11.5 }}>not paired</span>
                  )}
                </label>
              ))}
            </div>
          </div>
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
                              p.source && p.source !== "run"
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
                        {p.source
                          ? `from the ${p.source}`
                          : p.required
                            ? "no value"
                            : ""}
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
                gap: 16,
                flexWrap: "wrap",
                alignItems: "flex-end",
              }}
            >
              <Field label="Mode">
                <Seg
                  label="Rollout"
                  value={mode}
                  onChange={setMode}
                  options={[
                    { id: "canary", label: "Canary first" },
                    { id: "size", label: "Batches" },
                    { id: "all", label: "All at once" },
                  ]}
                />
              </Field>
              {mode !== "all" && (
                <Field
                  label={mode === "canary" ? "Then batches of" : "Batch size"}
                >
                  <span className="rk-field" style={{ width: 90 }}>
                    <input
                      type="number"
                      min={mode === "canary" ? 0 : 1}
                      value={size}
                      onChange={(e) =>
                        setSize(Math.max(0, Number(e.target.value) || 0))
                      }
                      data-testid="run-size"
                    />
                  </span>
                </Field>
              )}
              <Field label="Stop after failures" hint="0 never stops">
                <span className="rk-field" style={{ width: 90 }}>
                  <input
                    type="number"
                    min={0}
                    value={stopAfter}
                    onChange={(e) =>
                      setStopAfter(Math.max(0, Number(e.target.value) || 0))
                    }
                    data-testid="run-stop-after"
                  />
                </span>
              </Field>
              <Field label="Timeout per node (min)">
                <span className="rk-field" style={{ width: 90 }}>
                  <input
                    type="number"
                    min={1}
                    max={maxTimeout}
                    value={timeout}
                    onChange={(e) =>
                      setTimeout_(
                        Math.min(
                          maxTimeout,
                          Math.max(1, Number(e.target.value) || 1),
                        ),
                      )
                    }
                    data-testid="run-timeout"
                  />
                </span>
              </Field>
            </div>
            {plan.length > 1 && (
              <p
                className="tb-hint"
                style={{ margin: "8px 0 0" }}
                data-testid="run-plan"
              >
                {plan
                  .map(
                    (b, i) =>
                      `${i + 1}: ${b.length === 1 ? b[0] : `${b.length} nodes`}`,
                  )
                  .join(" → ")}
                . The first batch stops the run on any failure.
              </p>
            )}
          </div>
        </div>
      )}
    </Drawer>
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
  onClose,
  onToast,
  onEnded,
}: {
  id: number;
  now: number;
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
        width={1000}
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
              {sel && run.nodes.find((n) => n.node === sel)?.values && (
                <p className="tb-hint" style={{ margin: "6px 0 0" }}>
                  Parameters:{" "}
                  {Object.entries(
                    run.nodes.find((n) => n.node === sel)!.values!,
                  )
                    .map(([k, v]) => `${k}=${v}`)
                    .join(" ")}
                </p>
              )}
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
  const shown = runs.filter(
    (r) =>
      !q ||
      r.script.includes(q.toLowerCase()) ||
      (r.fleetName ?? r.fleet).toLowerCase().includes(q.toLowerCase()) ||
      r.by.name.toLowerCase().includes(q.toLowerCase()) ||
      String(r.id) === q,
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
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Executions</h2>
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
                <th>#</th>
                <th>Script</th>
                <th>Where</th>
                <th>By</th>
                <th>State</th>
                <th>Nodes</th>
                <th>Started</th>
                <th>Took</th>
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

// ---------- fleet parameters ----------

type Row = { k: string; v: string };
const rowsOf = (m: Record<string, string> | undefined): Row[] =>
  Object.entries(m ?? {}).map(([k, v]) => ({ k, v }));
const mapOf = (rows: Row[]): Record<string, string> =>
  Object.fromEntries(rows.filter((r) => r.k).map((r) => [r.k, r.v]));

/** Problems with a set of name=value rows, as the server checks them. */
export function rowProblems(rows: Row[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const r of rows) {
    if (!r.k) continue;
    if (!PARAM_NAME.test(r.k))
      out.push(`${r.k}: a name is capitals, digits and underscores`);
    else if (RESERVED.includes(r.k)) out.push(`${r.k} is set by the run`);
    if (seen.has(r.k)) out.push(`${r.k} is there twice`);
    seen.add(r.k);
  }
  return out;
}

function ValueRows({
  rows,
  onChange,
  testId,
}: {
  rows: Row[];
  onChange: (rows: Row[]) => void;
  testId: string;
}) {
  return (
    <div
      style={{ display: "flex", flexDirection: "column", gap: 6 }}
      data-testid={testId}
    >
      {rows.map((r, i) => (
        <div key={i} style={{ display: "flex", gap: 6 }}>
          <span className="rk-field" style={{ width: 220 }}>
            <input
              className="rk-mono"
              value={r.k}
              onChange={(e) =>
                onChange(
                  rows.map((x, j) =>
                    j === i ? { ...x, k: e.target.value.toUpperCase() } : x,
                  ),
                )
              }
              placeholder="NAME"
              aria-label="Name"
            />
          </span>
          <span className="rk-field" style={{ flex: "1 1 auto" }}>
            <input
              value={r.v}
              onChange={(e) =>
                onChange(
                  rows.map((x, j) =>
                    j === i ? { ...x, v: e.target.value } : x,
                  ),
                )
              }
              placeholder="value"
              aria-label={`Value of ${r.k || "the parameter"}`}
            />
          </span>
          <button
            className="rk-btn"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
            aria-label={`Remove ${r.k || "the row"}`}
          >
            Remove
          </button>
        </div>
      ))}
      <div>
        <button
          className="rk-btn"
          onClick={() => onChange([...rows, { k: "", v: "" }])}
        >
          <PlusIcon />
          Add a value
        </button>
      </div>
    </div>
  );
}

function ParamsSection({
  cluster,
  scripts,
  onToast,
}: {
  cluster: string;
  scripts: ScriptSummary[];
  onToast: (msg: string) => void;
}) {
  const [fleets, setFleets] = useState<AutomationFleets | null>(null);
  const [err, setErr] = useState("");
  const [fleetId, setFleetId] = useState("");
  const [values, setValues] = useState<Row[]>([]);
  const [nodeRows, setNodeRows] = useState<Record<string, Row[]>>({});
  const [ceiling, setCeiling] = useState<Privilege>("none");
  const [nodeSel, setNodeSel] = useState("");
  const [busy, setBusy] = useState(false);
  const [saveErr, setSaveErr] = useState("");

  const reset = useCallback((f: AutomationFleets, id: string) => {
    const p: FleetParams = f.fleets.find((x) => x.id === id)?.params ?? {};
    setValues(rowsOf(p.values));
    setNodeRows(
      Object.fromEntries(
        Object.entries(p.nodes ?? {}).map(([n, m]) => [n, rowsOf(m)]),
      ),
    );
    setCeiling(p.ceiling === "full" ? "full" : "none");
    setSaveErr("");
  }, []);

  useEffect(() => {
    let gone = false;
    fetchFleets(cluster)
      .then((f) => {
        if (gone) return;
        setFleets(f);
        const id = f.fleets[0]?.id ?? "";
        setFleetId(id);
        reset(f, id);
      })
      .catch((e: unknown) => !gone && setErr(errText(e)));
    return () => {
      gone = true;
    };
  }, [cluster, reset]);

  const fleet = fleets?.fleets.find((f) => f.id === fleetId);
  const problems = useMemo(
    () => [
      ...rowProblems(values),
      ...Object.entries(nodeRows).flatMap(([n, rows]) =>
        rowProblems(rows).map((p) => `${n}: ${p}`),
      ),
    ],
    [values, nodeRows],
  );
  const fullScripts = scripts
    .filter((s) => s.privilege === "full")
    .map((s) => s.name);

  const save = async () => {
    if (!fleet) return;
    setBusy(true);
    setSaveErr("");
    const nodes = Object.fromEntries(
      Object.entries(nodeRows)
        .map(([n, rows]) => [n, mapOf(rows)])
        .filter(([, m]) => Object.keys(m as object).length > 0),
    );
    try {
      const p = await saveFleetParams(cluster, fleet.id, {
        values: mapOf(values),
        nodes,
        ceiling,
      });
      setFleets((cur) =>
        cur
          ? {
              ...cur,
              fleets: cur.fleets.map((f) =>
                f.id === fleet.id ? { ...f, params: p } : f,
              ),
            }
          : cur,
      );
      onToast(`Parameters of ${fleet.name} are saved.`);
    } catch (e) {
      setSaveErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

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
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>
          Fleet parameters
        </h2>
        {fleets && (
          <label className="rk-field" style={{ width: 260 }}>
            <span className="rk-sr">Fleet</span>
            <select
              value={fleetId}
              onChange={(e) => {
                setFleetId(e.target.value);
                reset(fleets, e.target.value);
              }}
              data-testid="params-fleet"
            >
              {fleets.fleets.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name} ({f.nodes.length})
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      <p className="tb-hint" style={{ margin: 0 }}>
        A run gives each parameter its own value first, then the node’s, then
        the fleet’s. Values are plain text kept in the console’s ConfigMap:
        never put a password or token here.
      </p>
      {err && <p className="tb-err">{err}</p>}
      {fleet && (
        <>
          <section
            className="rk-card"
            style={{
              padding: "14px 16px",
              display: "flex",
              flexDirection: "column",
              gap: 10,
            }}
            aria-label="Privilege ceiling"
          >
            <h3 className="rk-h" style={{ margin: 0 }}>
              Privilege ceiling
            </h3>
            <Seg
              label="Privilege ceiling"
              value={ceiling}
              onChange={setCeiling}
              options={[
                { id: "none", label: "none: scripts run as nobody" },
                {
                  id: "full",
                  label: "full: scripts may run as the login user",
                },
              ]}
            />
            <p className="tb-hint" style={{ margin: 0 }}>
              {ceiling === "none"
                ? fullScripts.length
                  ? `These scripts declare privilege full and will not run here: ${fullScripts.join(", ")}. Their dry runs still do.`
                  : "No script declares privilege full yet."
                : "Scripts that declare privilege full may run with the login user’s rights, root or sudo."}
            </p>
          </section>
          <section
            className="rk-card"
            style={{
              padding: "14px 16px",
              display: "flex",
              flexDirection: "column",
              gap: 10,
            }}
            aria-label="Fleet values"
          >
            <h3 className="rk-h" style={{ margin: 0 }}>
              Values for every node of {fleet.name}
            </h3>
            <ValueRows
              rows={values}
              onChange={setValues}
              testId="params-values"
            />
          </section>
          <section
            className="rk-card"
            style={{
              padding: "14px 16px",
              display: "flex",
              flexDirection: "column",
              gap: 10,
            }}
            aria-label="Node values"
          >
            <h3 className="rk-h" style={{ margin: 0 }}>
              A node’s own values
            </h3>
            {Object.entries(nodeRows).map(([n, rows]) => (
              <div
                key={n}
                style={{ display: "flex", flexDirection: "column", gap: 6 }}
              >
                <span className="rk-mono" style={{ fontWeight: 600 }}>
                  {n}
                </span>
                <ValueRows
                  rows={rows}
                  onChange={(r) => setNodeRows({ ...nodeRows, [n]: r })}
                  testId={`params-node-${n}`}
                />
              </div>
            ))}
            <div style={{ display: "flex", gap: 6 }}>
              <label className="rk-field" style={{ width: 260 }}>
                <span className="rk-sr">Node</span>
                <select
                  value={nodeSel}
                  onChange={(e) => setNodeSel(e.target.value)}
                  data-testid="params-node-pick"
                >
                  <option value="">Pick a node…</option>
                  {fleet.nodes
                    .filter((n) => !nodeRows[n.name])
                    .map((n) => (
                      <option key={n.name} value={n.name}>
                        {n.name}
                      </option>
                    ))}
                </select>
              </label>
              <button
                className="rk-btn"
                disabled={!nodeSel}
                onClick={() => {
                  setNodeRows({ ...nodeRows, [nodeSel]: [{ k: "", v: "" }] });
                  setNodeSel("");
                }}
                data-testid="params-node-add"
              >
                <PlusIcon />
                Add node values
              </button>
            </div>
          </section>
          {problems.length > 0 && (
            <ul
              className="tb-err"
              style={{ margin: 0, paddingLeft: 18 }}
              data-testid="params-problems"
            >
              {problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
          {saveErr && <p className="tb-err">{saveErr}</p>}
          <div style={{ display: "flex", gap: 8 }}>
            <button
              className="rk-btn"
              data-on
              onClick={() => void save()}
              disabled={busy || problems.length > 0}
              data-testid="params-save"
            >
              {busy ? "Saving…" : "Save parameters"}
            </button>
            <button
              className="rk-btn"
              onClick={() => fleets && reset(fleets, fleetId)}
              disabled={busy}
            >
              Revert
            </button>
          </div>
        </>
      )}
    </>
  );
}
