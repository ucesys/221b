import { useState } from "react";
import type { ScriptSummary } from "../../api/automation";
import { href } from "../../lib/router";
import {
  nameProblems,
  scriptsUsing,
  type Override,
  type VariableEdit,
  type VariableRow,
} from "../../lib/variables";
import { ConfirmDialog } from "../ConfirmDialog";
import { Drawer } from "../Drawer";
import { TrashIcon } from "../Icons";

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** One variable of a fleet or node: its name and value, who differs, and who reads it. */
export function VariableDrawer({
  row,
  rows,
  place,
  onNode,
  scripts,
  onSave,
  onDropOverride,
  onShowNode,
  onClose,
}: {
  /** null for a new variable. */
  row: VariableRow | null;
  /** Everything in this table, to catch a name already used. */
  rows: VariableRow[];
  /** The name of the fleet or node being edited. */
  place: string;
  /** A node's own value: it has no sensitive box of its own. */
  onNode: boolean;
  scripts: ScriptSummary[];
  onSave: (edit: VariableEdit, reason: string) => Promise<void>;
  onDropOverride: (o: Override) => Promise<void>;
  onShowNode: (node: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(row?.name ?? "");
  const [value, setValue] = useState(row?.value ?? "");
  const [sensitive, setSensitive] = useState(row?.sensitive ?? false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [dropping, setDropping] = useState<Override | null>(null);
  const old = row?.own ? row.name : undefined;
  const problems = name ? nameProblems(rows, name, row?.name) : [];
  const renamed = !!old && name !== old;
  const changed =
    !row ||
    !!row.conflict ||
    renamed ||
    value !== (row.value ?? "") ||
    sensitive !== row.sensitive;
  const readers = scriptsUsing(scripts, name);
  const leftBehind = renamed ? scriptsUsing(scripts, old) : [];
  const run = (f: () => Promise<void>) => {
    setBusy(true);
    setErr("");
    f()
      .catch((e: unknown) => setErr(errText(e)))
      .finally(() => setBusy(false));
  };
  const unmarking = !!row?.own && row.sensitive && !sensitive;
  return (
    <Drawer
      label="Variable"
      kicker={`${row ? "Variable" : "New variable"} of ${place}`}
      title={name || "New variable"}
      copy={false}
      width={720}
      onClose={onClose}
      testId="param-drawer"
      footer={
        <>
          {err && (
            <p
              className="tb-err"
              style={{ flex: "1 1 100%", margin: 0 }}
              data-testid="param-error"
            >
              {err}
            </p>
          )}
          <button
            className="rk-btn"
            data-on
            disabled={busy || !name || problems.length > 0 || !changed}
            onClick={() =>
              run(() =>
                onSave({ old, name, value, sensitive }, reason),
              )
            }
            data-testid="param-save"
          >
            {row ? "Save" : "Add variable"}
          </button>
          <button className="rk-btn" onClick={onClose}>
            Cancel
          </button>
          {row?.own && (
            <button
              className="rk-icon-btn"
              style={{ marginLeft: "auto" }}
              aria-label={`Delete ${row.name}`}
              title={`Delete ${row.name}`}
              disabled={busy}
              onClick={() => setConfirming(true)}
              data-testid="param-delete"
            >
              <TrashIcon />
            </button>
          )}
        </>
      }
    >
      <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        {row && !row.own && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="param-inherited">
            Inherited from {row.from}. Saving sets it on {place}, over that
            value.
          </p>
        )}
        <label className="rk-flabel">
          Name
          <span className="rk-field">
            <input
              className="rk-mono"
              value={name}
              readOnly={!!row && !row.own}
              onChange={(e) => setName(e.target.value.toUpperCase())}
              data-testid="param-name"
            />
          </span>
        </label>
        <label className="rk-flabel">
          Value
          <span className="rk-field">
            <input
              value={value}
              onChange={(e) => setValue(e.target.value)}
              data-testid="param-value"
            />
          </span>
          <span className="rk-fhint">
            Plain text kept in the console’s ConfigMap: never put a password or
            token here.
          </span>
        </label>
        {problems.length > 0 && (
          <ul
            className="tb-err"
            style={{ margin: 0, paddingLeft: 18 }}
            data-testid="param-problems"
          >
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        {!onNode && (
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              fontSize: 12.5,
            }}
            title="A change to it always needs a second person on production. Unmarking it needs a second admin on any cluster."
          >
            <input
              type="checkbox"
              checked={sensitive}
              onChange={(e) => setSensitive(e.target.checked)}
              data-testid="param-sensitive"
            />
            Sensitive
          </label>
        )}
        {unmarking && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="param-unmark-hint">
            It stops being sensitive only when a second admin approves. Until
            then this change waits.
          </p>
        )}
        <label className="rk-flabel">
          Reason for this change
          <span className="rk-field">
            <input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              data-testid="param-reason"
            />
          </span>
          <span className="rk-fhint">
            Optional. It travels with any approval request.
          </span>
        </label>
        {row?.overwrites && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="param-overwrites">
            Overwrites <span className="rk-mono">{row.overwrites.value}</span>{" "}
            from {row.overwrites.from}.
          </p>
        )}
        {row && row.over.length > 0 && (
          <section data-testid="param-overrides">
            <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
              Overwritten on {row.over.length}{" "}
              {row.over.length === 1 ? "node" : "nodes"}
            </h3>
            <table className="tb-table">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Value</th>
                  <th>From</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {row.over.map((o) => (
                  <tr key={o.node} data-testid="param-override">
                    <td className="rk-mono">
                      <button
                        className="rk-link"
                        onClick={() => onShowNode(o.node)}
                      >
                        {o.node}
                      </button>
                    </td>
                    <td className="rk-mono" style={{ overflowWrap: "anywhere" }}>
                      {o.conflict ? (
                        <span style={{ color: "var(--err)" }}>conflict</span>
                      ) : (
                        o.value
                      )}
                    </td>
                    <td>{o.conflict ? o.conflict.join(" and ") : o.from}</td>
                    <td style={{ textAlign: "right" }}>
                      {o.own && (
                        <button
                          className="rk-icon-btn"
                          style={{ margin: "-6px 0" }}
                          aria-label={`Remove the value of ${o.node}`}
                          title={`Remove the value of ${o.node}, so it inherits again`}
                          disabled={busy}
                          onClick={() => setDropping(o)}
                          data-testid="param-override-delete"
                        >
                          <TrashIcon />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
        )}
        <section data-testid="param-scripts">
          <h3 className="rk-h" style={{ margin: "0 0 6px" }}>
            Scripts that use it {readers.length}
          </h3>
          {readers.length === 0 ? (
            <p className="tb-hint" style={{ margin: 0 }}>
              No script reads {name || "this variable"}.
            </p>
          ) : (
            <table className="tb-table">
              <tbody>
                {readers.map((r) => (
                  <tr key={r.script} data-testid="param-script">
                    <td className="rk-mono">
                      <a
                        className="rk-link"
                        href={href("scripts", [], { script: r.script })}
                      >
                        {r.script}
                      </a>
                    </td>
                    <td>{r.param.required ? "Required" : "Optional"}</td>
                    <td>{r.param.desc}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {leftBehind.length > 0 && (
            <p className="tb-err" style={{ margin: "6px 0 0" }} data-testid="param-rename-warning">
              {leftBehind.map((r) => r.script).join(", ")} still{" "}
              {leftBehind.length === 1 ? "reads" : "read"} {old}: renaming it
              here does not change the scripts.
            </p>
          )}
          {renamed && row && row.over.length > 0 && (
            <p className="tb-hint" style={{ margin: "6px 0 0" }}>
              Nodes that overwrite {old} keep their values under that name.
            </p>
          )}
        </section>
      </div>
      {dropping && row && (
        <ConfirmDialog
          title={`Remove the value of ${dropping.node}?`}
          confirm="Remove value"
          danger
          busy={busy}
          error={err}
          onConfirm={() =>
            run(async () => {
              await onDropOverride(dropping);
              setDropping(null);
            })
          }
          onCancel={() => setDropping(null)}
        >
          {dropping.node} goes back to {row.value} from {row.from}. On
          production a change that needs approval waits for a second person.
        </ConfirmDialog>
      )}
      {confirming && row && (
        <ConfirmDialog
          title={`Delete ${row.name} from ${place}?`}
          confirm="Delete variable"
          danger
          busy={busy}
          error={err}
          onConfirm={() =>
            run(async () => {
              await onSave({ old: row.name }, reason);
              setConfirming(false);
            })
          }
          onCancel={() => setConfirming(false)}
        >
          {readers.length > 0
            ? `${readers.map((r) => r.script).join(", ")} ${readers.length === 1 ? "reads" : "read"} it.`
            : "No script reads it."}{" "}
          On production a change that needs approval waits for a second person.
        </ConfirmDialog>
      )}
    </Drawer>
  );
}
