import { describe, expect, it } from 'vitest'
import { crc32, xlCol, xlsxBytes, zipStore } from './xlsx'

/** Reads the entries of an uncompressed ZIP. */
function unzip(bytes: Uint8Array): Map<string, string> {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  const dec = new TextDecoder()
  const out = new Map<string, string>()
  let at = 0
  while (view.getUint32(at, true) === 0x04034b50) {
    expect(view.getUint16(at + 8, true)).toBe(0) // stored, not deflated
    const size = view.getUint32(at + 18, true)
    const nameLen = view.getUint16(at + 26, true)
    const extra = view.getUint16(at + 28, true)
    const name = dec.decode(bytes.subarray(at + 30, at + 30 + nameLen))
    const start = at + 30 + nameLen + extra
    const data = bytes.subarray(start, start + size)
    expect(crc32(data)).toBe(view.getUint32(at + 14, true))
    out.set(name, dec.decode(data))
    at = start + size
  }
  expect(view.getUint32(bytes.length - 22, true)).toBe(0x06054b50)
  return out
}

describe('xlsx', () => {
  it('computes CRC-32', () => {
    expect(crc32(new TextEncoder().encode('123456789'))).toBe(0xcbf43926)
  })

  it('names columns like a spreadsheet', () => {
    expect([0, 25, 26, 51, 701, 702].map(xlCol)).toEqual(['A', 'Z', 'AA', 'AZ', 'ZZ', 'AAA'])
  })

  it('writes a ZIP that reads back', () => {
    const files = unzip(zipStore([{ name: 'a.txt', data: 'hello' }, { name: 'b/c.xml', data: '<x/>' }]))
    expect([...files.keys()]).toEqual(['a.txt', 'b/c.xml'])
    expect(files.get('a.txt')).toBe('hello')
  })

  it('writes a workbook with typed, escaped cells', () => {
    const files = unzip(
      xlsxBytes('Nodes', ['Node', 'CPU', 'Taints'], [
        ['worker-1', 2.5, 'a=b:NoSchedule\nc:NoExecute'],
        ['worker-<2>&', null, ''],
      ]),
    )
    expect([...files.keys()]).toContain('xl/worksheets/sheet1.xml')
    const sheet = files.get('xl/worksheets/sheet1.xml') ?? ''
    expect(sheet).toContain('<dimension ref="A1:C3"/>')
    expect(sheet).toContain('<c r="B2"><v>2.5</v></c>')
    expect(sheet).toContain('worker-&lt;2&gt;&amp;')
    expect(sheet).toContain('<c r="C2" t="inlineStr" s="2">')
    expect(sheet).not.toContain('r="B3"')
    expect(sheet).toContain('<autoFilter ref="A1:C3"/>')
    expect(files.get('xl/workbook.xml')).toContain('<sheet name="Nodes"')
  })
})
