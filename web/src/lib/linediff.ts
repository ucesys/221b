// A line diff of two texts, in unified form, for what the browser already
// holds (a script's versions). Helm's diffs come from the server.

const CONTEXT = 3
/** Past this many cells of the table, the changed part shows as replaced whole. */
const MAX_CELLS = 4_000_000

type Op = [' ' | '-' | '+', string]

function operations(a: string[], b: string[]): Op[] {
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let end = 0
  while (end < a.length - start && end < b.length - start && a[a.length - 1 - end] === b[b.length - 1 - end]) end++
  const x = a.slice(start, a.length - end)
  const y = b.slice(start, b.length - end)
  const ops: Op[] = a.slice(0, start).map((l) => [' ', l])
  if (x.length * y.length > MAX_CELLS) {
    for (const l of x) ops.push(['-', l])
    for (const l of y) ops.push(['+', l])
  } else {
    // table[i][j] is the longest common subsequence of x from i and y from j.
    const w = y.length + 1
    const table = new Uint32Array((x.length + 1) * w)
    for (let i = x.length - 1; i >= 0; i--)
      for (let j = y.length - 1; j >= 0; j--)
        table[i * w + j] = x[i] === y[j] ? table[(i + 1) * w + j + 1]! + 1 : Math.max(table[(i + 1) * w + j]!, table[i * w + j + 1]!)
    let i = 0
    let j = 0
    while (i < x.length || j < y.length) {
      if (i < x.length && j < y.length && x[i] === y[j]) {
        ops.push([' ', x[i]!])
        i++
        j++
      } else if (j >= y.length || (i < x.length && table[(i + 1) * w + j]! >= table[i * w + j + 1]!)) {
        ops.push(['-', x[i++]!])
      } else {
        ops.push(['+', y[j++]!])
      }
    }
  }
  for (const l of a.slice(a.length - end)) ops.push([' ', l])
  return ops
}

export interface LineDiff {
  text: string
  added: number
  removed: number
}

/** The difference between two texts: hunks with three lines of context, and how many lines each side lost. */
export function lineDiff(from: string, to: string): LineDiff {
  const ops = operations(from.split('\n'), to.split('\n'))
  const changed = ops.flatMap(([kind], i) => (kind === ' ' ? [] : [i]))
  const added = ops.filter(([kind]) => kind === '+').length
  const removed = ops.filter(([kind]) => kind === '-').length
  const out: string[] = []
  let at = 0
  while (at < changed.length) {
    const first = Math.max(0, changed[at]! - CONTEXT)
    let last = changed[at]!
    while (at + 1 < changed.length && changed[at + 1]! - last <= CONTEXT * 2) last = changed[++at]!
    at++
    const end = Math.min(ops.length - 1, last + CONTEXT)
    const before = ops.slice(0, first)
    const hunk = ops.slice(first, end + 1)
    const oldStart = before.filter(([kind]) => kind !== '+').length + 1
    const newStart = before.filter(([kind]) => kind !== '-').length + 1
    out.push(`@@ -${oldStart},${hunk.filter(([kind]) => kind !== '+').length} +${newStart},${hunk.filter(([kind]) => kind !== '-').length} @@`)
    for (const [kind, line] of hunk) out.push(kind + line)
  }
  return { text: out.join('\n'), added, removed }
}
