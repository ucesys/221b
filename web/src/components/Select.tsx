/** The prototype's select: a field with a chevron, its label for screen readers. */
export function Select({ label, value, options, onChange, disabled, testId, width }: { label: string; value: string; options: [string, string][]; onChange: (v: string) => void; disabled?: boolean; testId?: string; width?: number | string }) {
  return (
    <label className="rk-field rk-sel" style={{ width: width ?? 'auto', position: 'relative' }}>
      <span className="rk-sr">{label}</span>
      <select value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} data-testid={testId}>
        {options.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </select>
      <svg className="rk-selchev" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="m6 9 6 6 6-6" />
      </svg>
    </label>
  )
}
