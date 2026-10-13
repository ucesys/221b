// A real .xlsx built in the browser with no library, ported from the
// prototype: an uncompressed ZIP of the five XML parts a workbook needs.

export type Cell = string | number | boolean | null | undefined

function xmlEsc(s: string): string {
  return s
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F]/g, '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/** Column letters: 0 → A, 25 → Z, 26 → AA. */
export function xlCol(i: number): string {
  let s = ''
  let n = i + 1
  while (n > 0) {
    const m = (n - 1) % 26
    s = String.fromCharCode(65 + m) + s
    n = Math.floor((n - 1) / 26)
  }
  return s
}

let crcTable: number[] | null = null

export function crc32(b: Uint8Array): number {
  if (!crcTable) {
    crcTable = []
    for (let n = 0; n < 256; n++) {
      let c = n
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
      crcTable[n] = c >>> 0
    }
  }
  let crc = 0xffffffff
  for (let i = 0; i < b.length; i++) crc = (crcTable[(crc ^ b[i]!) & 0xff]! ^ (crc >>> 8)) >>> 0
  return (crc ^ 0xffffffff) >>> 0
}

interface ZipFile {
  name: string
  data: string | Uint8Array
}

/** An uncompressed ZIP: local headers, data, central directory. */
export function zipStore(files: ZipFile[], when = new Date()): Uint8Array {
  const enc = new TextEncoder()
  const parts: Uint8Array[] = []
  const central: Uint8Array[] = []
  let offset = 0
  const time = (when.getHours() << 11) | (when.getMinutes() << 5) | (when.getSeconds() >> 1)
  const date = ((when.getFullYear() - 1980) << 9) | ((when.getMonth() + 1) << 5) | when.getDate()
  for (const f of files) {
    const name = enc.encode(f.name)
    const data = typeof f.data === 'string' ? enc.encode(f.data) : f.data
    const crc = crc32(data)
    const lh = new DataView(new ArrayBuffer(30))
    lh.setUint32(0, 0x04034b50, true)
    lh.setUint16(4, 20, true)
    lh.setUint16(6, 0x0800, true)
    lh.setUint16(8, 0, true)
    lh.setUint16(10, time, true)
    lh.setUint16(12, date, true)
    lh.setUint32(14, crc, true)
    lh.setUint32(18, data.length, true)
    lh.setUint32(22, data.length, true)
    lh.setUint16(26, name.length, true)
    lh.setUint16(28, 0, true)
    parts.push(new Uint8Array(lh.buffer), name, data)
    const ch = new DataView(new ArrayBuffer(46))
    ch.setUint32(0, 0x02014b50, true)
    ch.setUint16(4, 20, true)
    ch.setUint16(6, 20, true)
    ch.setUint16(8, 0x0800, true)
    ch.setUint16(10, 0, true)
    ch.setUint16(12, time, true)
    ch.setUint16(14, date, true)
    ch.setUint32(16, crc, true)
    ch.setUint32(20, data.length, true)
    ch.setUint32(24, data.length, true)
    ch.setUint16(28, name.length, true)
    ch.setUint16(30, 0, true)
    ch.setUint16(32, 0, true)
    ch.setUint16(34, 0, true)
    ch.setUint16(36, 0, true)
    ch.setUint32(38, 0, true)
    ch.setUint32(42, offset, true)
    central.push(new Uint8Array(ch.buffer), name)
    offset += 30 + name.length + data.length
  }
  const size = central.reduce((s, a) => s + a.length, 0)
  const end = new DataView(new ArrayBuffer(22))
  end.setUint32(0, 0x06054b50, true)
  end.setUint16(4, 0, true)
  end.setUint16(6, 0, true)
  end.setUint16(8, files.length, true)
  end.setUint16(10, files.length, true)
  end.setUint32(12, size, true)
  end.setUint32(16, offset, true)
  end.setUint16(20, 0, true)
  const all = [...parts, ...central, new Uint8Array(end.buffer)]
  const out = new Uint8Array(all.reduce((s, a) => s + a.length, 0))
  let at = 0
  for (const a of all) {
    out.set(a, at)
    at += a.length
  }
  return out
}

/** One sheet with a bold, frozen, filterable header row. */
export function xlsxBytes(sheet: string, head: string[], rows: Cell[][]): Uint8Array {
  const name = sheet.replace(/[[\]:*?/\\]/g, ' ').trim().slice(0, 31) || 'Sheet1'
  const nCols = Math.max(1, head.length)
  const last = xlCol(nCols - 1) + (rows.length + 1)
  const widths = head.map((h, i) => {
    let w = h.length
    for (const r of rows) {
      const v = r[i]
      if (v == null) continue
      for (const line of String(v).split('\n')) w = Math.max(w, line.length)
    }
    return Math.min(70, Math.max(7, w + 2))
  })
  const cell = (v: Cell, ref: string, hdr: boolean): string => {
    if (v == null || v === '') return ''
    if (!hdr && typeof v === 'number' && Number.isFinite(v)) return `<c r="${ref}"><v>${v}</v></c>`
    if (!hdr && typeof v === 'boolean') return `<c r="${ref}" t="b"><v>${v ? 1 : 0}</v></c>`
    const s = String(v)
    const style = hdr ? ' s="1"' : s.includes('\n') ? ' s="2"' : ''
    return `<c r="${ref}" t="inlineStr"${style}><is><t xml:space="preserve">${xmlEsc(s)}</t></is></c>`
  }
  const sheetData =
    `<row r="1">${head.map((h, i) => cell(h, xlCol(i) + '1', true)).join('')}</row>` +
    rows.map((r, ri) => `<row r="${ri + 2}">${r.map((v, i) => cell(v, xlCol(i) + (ri + 2), false)).join('')}</row>`).join('')
  const X = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
  const NS = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
  const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
  const P = 'http://schemas.openxmlformats.org/package/2006/relationships'
  const ws =
    X +
    `<worksheet xmlns="${NS}" xmlns:r="${R}"><dimension ref="A1:${last}"/><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultRowHeight="15"/><cols>` +
    widths.map((w, i) => `<col min="${i + 1}" max="${i + 1}" width="${w}" customWidth="1"/>`).join('') +
    `</cols><sheetData>${sheetData}</sheetData>` +
    (rows.length ? `<autoFilter ref="A1:${last}"/>` : '') +
    '</worksheet>'
  const wb =
    X +
    `<workbook xmlns="${NS}" xmlns:r="${R}"><sheets><sheet name="${xmlEsc(name)}" sheetId="1" r:id="rId1"/></sheets>` +
    (rows.length
      ? `<definedNames><definedName name="_xlnm._FilterDatabase" localSheetId="0" hidden="1">'${xmlEsc(name.replace(/'/g, "''"))}'!$A$1:$${xlCol(nCols - 1)}$${rows.length + 1}</definedName></definedNames>`
      : '') +
    '</workbook>'
  const styles =
    X +
    `<styleSheet xmlns="${NS}"><fonts count="2"><font><sz val="11"/><name val="Calibri"/><family val="2"/></font><font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font></fonts>` +
    '<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFEEF0F3"/><bgColor indexed="64"/></patternFill></fill></fills>' +
    '<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>' +
    '<cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment wrapText="1" vertical="top"/></xf></cellXfs>' +
    '<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>'
  return zipStore([
    {
      name: '[Content_Types].xml',
      data:
        X +
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>',
    },
    {
      name: '_rels/.rels',
      data: X + `<Relationships xmlns="${P}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
    },
    { name: 'xl/workbook.xml', data: wb },
    {
      name: 'xl/_rels/workbook.xml.rels',
      data:
        X +
        `<Relationships xmlns="${P}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`,
    },
    { name: 'xl/styles.xml', data: styles },
    { name: 'xl/worksheets/sheet1.xml', data: ws },
  ])
}

/** Starts a download of the bytes as a file. */
export function download(bytes: Uint8Array, file: string): void {
  const blob = new Blob([bytes as BlobPart], { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 30000)
}
