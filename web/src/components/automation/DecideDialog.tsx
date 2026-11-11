import { useState, type ReactNode } from 'react'
import { MAX_COMMENT } from '../../api/automation'
import { ConfirmDialog } from '../ConfirmDialog'

/** What a decision does: approve it, reject it, or, for whoever asked, withdraw it. */
export type Decision = 'approve' | 'reject' | 'withdraw'

const WORDS: Record<Decision, { verb: string; placeholder: string }> = {
  approve: { verb: 'Approve', placeholder: '' },
  reject: { verb: 'Reject', placeholder: 'What to change before it is asked for again' },
  withdraw: { verb: 'Withdraw', placeholder: 'Why it is no longer needed' },
}

/**
 * Asks before an approval, and for the reason with a rejection or a
 * withdrawal, which the person who asked reads. It is the one Reject… and
 * Withdraw… dialog of the console (I-4): the Inbox's console changes, a
 * wiki page's request, a proposed change and a script's version. A reason
 * is required unless the place says otherwise (a wiki page's request takes
 * one optionally); a required one left empty is not sent.
 */
export function DecideDialog({
  how,
  what,
  children,
  reasonRequired = true,
  busy,
  error,
  onCancel,
  onConfirm,
}: {
  how: Decision
  /** What is decided, as the title names it: "ensure-chrony v3". */
  what: string
  /** What the decision does. */
  children: ReactNode
  /** Whether a rejection or a withdrawal must say why; true unless the place keeps it optional. */
  reasonRequired?: boolean
  busy?: boolean
  error?: string
  onCancel: () => void
  onConfirm: (comment: string) => void
}) {
  const [comment, setComment] = useState('')
  const asks = how !== 'approve'
  const { verb, placeholder } = WORDS[how]
  return (
    <ConfirmDialog title={`${verb} ${what}?`} confirm={verb} danger={asks} busy={busy} disabled={asks && reasonRequired && comment.trim() === ''} error={error} onCancel={onCancel} onConfirm={() => onConfirm(comment.trim())}>
      <p style={{ margin: 0 }}>{children}</p>
      {asks && (
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12.5, fontWeight: 500 }}>{reasonRequired ? 'Why (required)' : 'Why (optional)'}</span>
          <textarea
            className="rk-area"
            rows={3}
            style={{ fontFamily: 'inherit', fontSize: 12.5, resize: 'vertical' }}
            maxLength={MAX_COMMENT}
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            placeholder={placeholder}
            data-testid="decide-comment"
          />
        </label>
      )}
    </ConfirmDialog>
  )
}
