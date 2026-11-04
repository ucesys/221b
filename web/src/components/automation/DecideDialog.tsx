import { useState, type ReactNode } from "react";
import { MAX_COMMENT } from "../../api/automation";
import { ConfirmDialog } from "../ConfirmDialog";

/**
 * Asks before an approval, and for the reason with a rejection, which the
 * person who asked reads; a rejection without one is not sent.
 */
export function DecideDialog({
  how,
  what,
  children,
  busy,
  error,
  onCancel,
  onConfirm,
}: {
  how: "approve" | "reject";
  /** What is decided, as the title names it: "ensure-chrony v3". */
  what: string;
  /** What the decision does. */
  children: ReactNode;
  busy?: boolean;
  error?: string;
  onCancel: () => void;
  onConfirm: (comment: string) => void;
}) {
  const [comment, setComment] = useState("");
  const reject = how === "reject";
  return (
    <ConfirmDialog
      title={reject ? `Reject ${what}?` : `Approve ${what}?`}
      confirm={reject ? "Reject" : "Approve"}
      danger={reject}
      busy={busy}
      disabled={reject && comment.trim() === ""}
      error={error}
      onCancel={onCancel}
      onConfirm={() => onConfirm(comment.trim())}
    >
      <p style={{ margin: 0 }}>{children}</p>
      {reject && (
        <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          <span style={{ fontSize: 12.5, fontWeight: 500 }}>Why (required)</span>
          <textarea
            className="rk-area"
            rows={3}
            style={{ fontFamily: "inherit", fontSize: 12.5, resize: "vertical" }}
            maxLength={MAX_COMMENT}
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            placeholder="What to change before it is asked for again"
            data-testid="decide-comment"
          />
        </label>
      )}
    </ConfirmDialog>
  );
}
