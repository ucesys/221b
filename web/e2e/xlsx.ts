// Reads the first sheet of the workbooks the console exports: an
// uncompressed ZIP with inline strings, as src/lib/xlsx.ts writes them.

export type SheetCell = string | number | null

function entries(bytes: Uint8Array): Map<string, string> {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  const dec = new TextDecoder()
  const out = new Map<string, string>()
  let at = 0
  while (at + 30 <= bytes.length && view.getUint32(at, true) === 0x04034b50) {
    if (view.getUint16(at + 8, true) !== 0) throw new Error('compressed entry')
    const size = view.getUint32(at + 18, true)
    const nameLen = view.getUint16(at + 26, true)
    const extra = view.getUint16(at + 28, true)
    const name = dec.decode(bytes.subarray(at + 30, at + 30 + nameLen))
    const start = at + 30 + nameLen + extra
    out.set(name, dec.decode(bytes.subarray(start, start + size)))
    at = start + size
  }
  return out
}

const unescape = (s: string) => s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&amp;/g, '&')

function colIndex(ref: string): number {
  const letters = /^[A-Z]+/.exec(ref)?.[0] ?? 'A'
  let n = 0
  for (const ch of letters) n = n * 26 + (ch.charCodeAt(0) - 64)
  return n - 1
}

/** Rows of cells, header first. Missing cells read as null. */
export function readSheet(bytes: Uint8Array): SheetCell[][] {
  const xml = entries(bytes).get('xl/worksheets/sheet1.xml')
  if (!xml) throw new Error('no sheet1.xml')
  const rows: SheetCell[][] = []
  for (const m of xml.matchAll(/<row r="\d+">(.*?)<\/row>/gs)) {
    const row: SheetCell[] = []
    for (const c of (m[1] ?? '').matchAll(/<c r="([A-Z]+)\d+"([^>]*)>(.*?)<\/c>/gs)) {
      const i = colIndex(c[1] ?? 'A')
      while (row.length < i) row.push(null)
      const inline = /<t[^>]*>(.*?)<\/t>/s.exec(c[3] ?? '')
      const v = /<v>(.*?)<\/v>/s.exec(c[3] ?? '')
      row[i] = inline ? unescape(inline[1] ?? '') : v ? Number(v[1]) : null
    }
    rows.push(row)
  }
  return rows
}
