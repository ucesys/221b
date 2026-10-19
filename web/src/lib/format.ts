// Number, size and time formatting, ported from the prototype so the app
// reads the same.

const MI = 1024 * 1024

/** Drops trailing zeros: trimNum(2.50, 2) → "2.5". */
export function trimNum(n: number, dp: number): string {
  let s = n.toFixed(dp)
  if (s.includes('.')) s = s.replace(/0+$/, '').replace(/\.$/, '')
  return s
}

/** Millicores as cores: 6900 → "6.9", 15800 → "15.8". */
export function fCores(milli: number): string {
  return trimNum(milli / 1000, milli < 10000 ? 2 : 1)
}

/** Bytes as Mi, Gi or Ti, the way kubectl users read memory. */
export function fMem(bytes: number | null | undefined): string {
  if (bytes == null) return '—'
  const mi = bytes / MI
  if (mi < 1024) return Math.round(mi) + 'Mi'
  if (mi < 1048576) return trimNum(mi / 1024, mi < 10240 ? 2 : 1) + 'Gi'
  return trimNum(mi / 1048576, 2) + 'Ti'
}

export function pct(a: number, b: number): number {
  return b > 0 ? (a / b) * 100 : 0
}

export function clampPct(p: number): number {
  return Math.max(0, Math.min(100, p))
}

/** A percentage for display: "<1%" for tiny shares. */
export function pctTxt(p: number): string {
  return (p > 0 && p < 1 ? '<1' : String(Math.round(p))) + '%'
}

/** A duration in minutes as the prototype shows ages: 45s, 12m, 3h 5m, 2d 4h, 30d. */
export function fmtMinutes(min: number): string {
  if (min < 1) return Math.max(1, Math.round(min * 60)) + 's'
  const tot = Math.round(min)
  if (tot < 60) return tot + 'm'
  const h = Math.floor(tot / 60)
  const m = tot % 60
  if (h < 24) return m ? `${h}h ${m}m` : `${h}h`
  const d = Math.floor(h / 24)
  const hh = h % 24
  if (d < 10) return hh ? `${d}d ${hh}h` : `${d}d`
  return `${d}d`
}

export function minutesSince(iso: string | undefined, now: number): number {
  if (!iso) return 0
  return Math.max(0, (now - Date.parse(iso)) / 60000)
}

/** How long ago, as an age: "3d 4h". */
export function fmtAge(iso: string | undefined, now: number): string {
  return iso ? fmtMinutes(minutesSince(iso, now)) : '—'
}

function pad2(n: number): string {
  return (n < 10 ? '0' : '') + n
}

// ---- dates and times ----
//
// Every date and time the console shows is in the browser's time zone, with
// its short name, in the one format the Settings page sets for everyone:
// "2026-05-03 1:34pm PDT" by default.

/** How dates and times read, as the Settings page sets it for everyone. */
export interface TimeFormat {
  /** iso 2026-05-03 · us 05/03/2026 · eu 03/05/2026 · long May 3, 2026 */
  date: 'iso' | 'us' | 'eu' | 'long'
  /** 12h 1:34pm · 24h 13:34 */
  clock: '12h' | '24h'
}

export const DEFAULT_TIME_FORMAT: TimeFormat = { date: 'iso', clock: '12h' }

let timeFormat: TimeFormat = DEFAULT_TIME_FORMAT

/** Uses the format the server's settings carry; the app sets it as they arrive. */
export function setTimeFormat(f: TimeFormat | undefined): void {
  timeFormat = f ?? DEFAULT_TIME_FORMAT
}

export function getTimeFormat(): TimeFormat {
  return timeFormat
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

function dateText(d: Date, f: TimeFormat): string {
  const y = d.getFullYear()
  const m = pad2(d.getMonth() + 1)
  const day = pad2(d.getDate())
  switch (f.date) {
    case 'us':
      return `${m}/${day}/${y}`
    case 'eu':
      return `${day}/${m}/${y}`
    case 'long':
      return `${MONTHS[d.getMonth()]} ${d.getDate()}, ${y}`
  }
  return `${y}-${m}-${day}`
}

function clockText(d: Date, f: TimeFormat, seconds: boolean): string {
  const ss = seconds ? `:${pad2(d.getSeconds())}` : ''
  if (f.clock === '24h') return `${pad2(d.getHours())}:${pad2(d.getMinutes())}${ss}`
  return `${d.getHours() % 12 || 12}:${pad2(d.getMinutes())}${ss}${d.getHours() < 12 ? 'am' : 'pm'}`
}

const zoneNames = new Map<string, Intl.DateTimeFormat>()

/**
 * The browser time zone's short name at that moment: "PDT", "EST", "CEST",
 * "UTC". A zone with no common short name reads as its offset, "GMT+9".
 */
export function zoneText(d: Date): string {
  for (const locale of ['en-US', 'en-GB']) {
    let f = zoneNames.get(locale)
    if (!f) {
      f = new Intl.DateTimeFormat(locale, { timeZoneName: 'short' })
      zoneNames.set(locale, f)
    }
    const z = f.formatToParts(d).find((p) => p.type === 'timeZoneName')?.value ?? ''
    if (z && !/^(GMT|UTC)[+-]/.test(z)) return z
    if (locale === 'en-GB') return z
  }
  return ''
}

function parse(at: string | Date | null | undefined): Date | null {
  if (!at) return null
  const d = at instanceof Date ? at : new Date(at)
  return Number.isNaN(d.getTime()) ? null : d
}

/** A date and time: "2026-05-03 1:34pm PDT"; with seconds where they matter, "1:34:07pm". */
export function fmtTs(at: string | Date | null | undefined, opts: { seconds?: boolean } = {}): string {
  const d = parse(at)
  if (!d) return '—'
  return `${dateText(d, timeFormat)} ${clockText(d, timeFormat, !!opts.seconds)} ${zoneText(d)}`.trim()
}

/** A time of day: "1:34pm PDT"; with seconds where they matter, "1:34:07pm PDT". */
export function fmtClock(at: string | Date | null | undefined, opts: { seconds?: boolean } = {}): string {
  const d = parse(at)
  if (!d) return '—'
  return `${clockText(d, timeFormat, !!opts.seconds)} ${zoneText(d)}`.trim()
}

/**
 * Times the server writes into its own sentences, "Ready since 2026-10-12
 * 14:03:07 UTC.", in the console's format and the browser's time zone.
 */
export function localTimes(text: string): string
export function localTimes(text: string | undefined): string | undefined
export function localTimes(text: string | undefined): string | undefined {
  return text?.replace(/\b(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}) UTC\b/g, (_, day: string, time: string) => fmtTs(`${day}T${time}Z`))
}

export function nWord(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** Natural order: worker-2 before worker-10. */
export function compareNatural(a: string, b: string): number {
  return collator.compare(a, b)
}

/** Numbers numerically, text naturally. */
export function compareValues(a: number | string, b: number | string): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return compareNatural(String(a), String(b))
}

/** A stamp for file names: 20261012-1403. */
export function fileStamp(d = new Date()): string {
  return `${d.getFullYear()}${pad2(d.getMonth() + 1)}${pad2(d.getDate())}-${pad2(d.getHours())}${pad2(d.getMinutes())}`
}

export function round(n: number, dp: number): number {
  const f = 10 ** dp
  return Math.round(n * f) / f
}
