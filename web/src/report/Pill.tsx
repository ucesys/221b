// A status as a pill, in the console's tones.
export function Pill({ text, tone }: { text: string; tone: 'ok' | 'warn' | 'err' | 'info' | 'muted' }) {
  return (
    <span className="rk-pill" data-tone={tone}>
      <span className="rk-dot" aria-hidden="true" />
      {text}
    </span>
  )
}
