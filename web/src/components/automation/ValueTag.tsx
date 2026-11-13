// How a run parameter's value reads wherever it shows (FU-06): a variable
// it is bound to is a chip, "variable NAME · 3 values", and a value given
// for the run alone carries a small "constant" badge, so the one is never
// taken for the other. In the Run dialog the chip clears; elsewhere, as in
// a run's details or a run request waiting in the Inbox, it only reads.

/** A parameter bound to a variable: its name and, when known, its values on the run's nodes. */
export function VariableChip({
  variable,
  detail,
  title,
  onClear,
  clearLabel,
  textId,
  testId,
  detailTestId,
  clearTestId,
}: {
  variable: string
  /** "3 values", its one value, "value hidden". */
  detail?: string
  /** The values on each node, or where the variable is set. */
  title?: string
  onClear?: () => void
  clearLabel?: string
  /** Names the chip's text, for a field that it describes. */
  textId?: string
  testId?: string
  detailTestId?: string
  clearTestId?: string
}) {
  return (
    <span className="rk-pill" data-tone="info" title={title} style={{ maxWidth: '100%', minWidth: 0, overflow: 'hidden', paddingRight: onClear ? 2 : undefined, fontFamily: 'var(--font)' }} data-testid={testId} data-variable={variable}>
      <span id={textId} style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
        <span style={{ fontWeight: 400 }}>variable</span> <span className="rk-mono">{variable}</span>
        {detail !== undefined && (
          <>
            {' · '}
            <span className="rk-mono" style={{ fontWeight: 400 }} data-testid={detailTestId}>
              {detail}
            </span>
          </>
        )}
      </span>
      {onClear && (
        <button
          type="button"
          aria-label={clearLabel ?? `Clear variable ${variable}`}
          onClick={onClear}
          style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 18, height: 18, padding: 0, border: 0, borderRadius: 9, background: 'none', color: 'inherit', cursor: 'pointer', font: 'inherit' }}
          data-testid={clearTestId}
        >
          ✕
        </button>
      )}
    </span>
  )
}

/** A value given for the run, the same on every node; or, for a secret parameter, the reference given. */
export function ConstantBadge({ id, label = 'constant', testId }: { id?: string; label?: string; testId?: string }) {
  return (
    <span id={id} className="rk-tag" style={{ flex: 'none', fontWeight: 400 }} title={label === 'constant' ? 'The same value on every node' : undefined} data-testid={testId}>
      {label}
    </span>
  )
}

/**
 * What an empty parameter sends, said outright (FU-07): "empty string" is a
 * value, NAME set to "" on every node, over whatever nodes and fleets set;
 * "not set" is no value at all, the script gets no NAME. Environment
 * variables have no null: unset is the nearest. A run never gives a
 * secret parameter an empty value.
 */
export function EmptyChip({ kind, name, onClear, clearLabel, testId }: { kind: 'empty' | 'unset'; name: string; onClear?: () => void; clearLabel?: string; testId?: string }) {
  const empty = kind === 'empty'
  const title = empty ? `${name}="" on every node: set, but empty. It wins over what nodes and fleets set.` : `No value: the script gets no ${name} at all (unset).`
  return (
    <span className="rk-pill" data-tone={empty ? 'info' : undefined} title={title} style={{ flex: 'none', fontFamily: 'var(--font)', fontWeight: 400, paddingRight: onClear ? 2 : undefined }} data-testid={testId} data-empty={kind}>
      {empty ? (
        <>
          empty string <span className="rk-mono">&quot;&quot;</span>
        </>
      ) : (
        'not set'
      )}
      {onClear && (
        <button
          type="button"
          aria-label={clearLabel ?? `Clear the empty string from ${name}`}
          onClick={onClear}
          style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 18, height: 18, padding: 0, border: 0, borderRadius: 9, background: 'none', color: 'inherit', cursor: 'pointer', font: 'inherit' }}
          data-testid={testId ? `${testId}-clear` : undefined}
        >
          ✕
        </button>
      )}
    </span>
  )
}
