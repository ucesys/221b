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

/** A timestamp in UTC: "2026-10-12 14:03:07 UTC". */
export function fmtTs(iso: string | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}:${pad2(d.getUTCSeconds())} UTC`
}

/** A local time of day: "14:03:07". */
export function fmtClock(d: Date | null): string {
  if (!d) return '—'
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`
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
