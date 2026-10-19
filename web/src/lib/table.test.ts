import { describe, expect, it } from 'vitest'
import { applySuggestion, csvText, exportFileName, matchesQuery, nextSort, queryWords, sortRows, suggestions, type ColumnDef } from './table'

interface R {
  name: string
  n: number | null
  labels: string[]
}

const cols: ColumnDef<R>[] = [
  { id: 'name', label: 'Name', width: '1fr', min: 100, sortValue: (r) => r.name, cell: (r) => r.name, export: (r) => [{ head: 'Name', cell: r.name }] },
  { id: 'n', label: 'N', width: '1fr', min: 100, defaultDir: 'desc', sortValue: (r) => r.n, cell: (r) => r.n, export: (r) => [{ head: 'N', cell: r.n }] },
]

const rows: R[] = [
  { name: 'b-2', n: 3, labels: ['app=web', 'tier=front'] },
  { name: 'b-10', n: null, labels: ['app=web'] },
  { name: 'a', n: 1, labels: ['app=api', 'tier=back'] },
]
const terms = (r: R) => [{ kind: 'name', text: r.name }, ...r.labels.map((l) => ({ kind: 'label', text: l }))]

describe('sorting', () => {
  it('sorts naturally with empty values last, ties by the first column', () => {
    expect(sortRows(rows, cols, { col: 'name', dir: 'asc' }, 0).map((r) => r.name)).toEqual(['a', 'b-2', 'b-10'])
    expect(sortRows(rows, cols, { col: 'n', dir: 'desc' }, 0).map((r) => r.name)).toEqual(['b-2', 'a', 'b-10'])
    expect(sortRows(rows, cols, { col: 'n', dir: 'asc' }, 0).map((r) => r.name)).toEqual(['a', 'b-2', 'b-10'])
  })

  it('reverses the same column and starts another in its own direction', () => {
    expect(nextSort(cols, { col: 'name', dir: 'asc' }, 'name')).toEqual({ col: 'name', dir: 'desc' })
    expect(nextSort(cols, { col: 'name', dir: 'asc' }, 'n')).toEqual({ col: 'n', dir: 'desc' })
  })
})

describe('searching', () => {
  it('matches every word in any term', () => {
    expect(rows.filter((r) => matchesQuery(terms(r), queryWords('web'))).map((r) => r.name)).toEqual(['b-2', 'b-10'])
    expect(rows.filter((r) => matchesQuery(terms(r), queryWords('web front'))).map((r) => r.name)).toEqual(['b-2'])
    expect(rows.filter((r) => matchesQuery(terms(r), queryWords('  '))).length).toBe(3)
  })

  it('suggests what the rows contain as the last word is typed, prefixes and common ones first', () => {
    expect(suggestions(rows, terms, 'ap')).toEqual([
      { kind: 'label', text: 'app=web', count: 2 },
      { kind: 'label', text: 'app=api', count: 1 },
    ])
    expect(suggestions(rows, terms, 'b-').map((s) => s.text)).toEqual(['b-2', 'b-10'])
    // Earlier words narrow the rows the suggestions come from.
    expect(suggestions(rows, terms, 'tier=back ap').map((s) => s.text)).toEqual(['app=api'])
    expect(suggestions(rows, terms, '')).toEqual([])
    expect(suggestions(rows, terms, 'app=web')).toEqual([])
    expect(applySuggestion('tier=back a', { kind: 'name', text: 'a', count: 1 })).toBe('tier=back a ')
    expect(applySuggestion('x ', { kind: 'label', text: 'app=web', count: 1 })).toBe('x app=web ')
  })
})

describe('exports', () => {
  it('writes CSV with a byte-order mark, CRLF and quoting', () => {
    const text = csvText(['Name', 'Note'], [['a', 'plain'], ['b,c', 'says "hi"\nthen'], [null, 3]])
    expect(text).toBe('﻿Name,Note\r\na,plain\r\n"b,c","says ""hi""\nthen"\r\n,3\r\n')
  })

  it('keeps text a spreadsheet would run as a formula as text', () => {
    const text = csvText(['Message'], [['=HYPERLINK("http://x")'], ['@SUM(A1)'], ['+1 more'], ['-'], ['-5'], ['12%'], [-3]])
    expect(text).toBe('﻿Message\r\n"\'=HYPERLINK(""http://x"")"\r\n\'@SUM(A1)\r\n\'+1 more\r\n-\r\n-5\r\n12%\r\n-3\r\n')
  })

  it('names files after the cluster and the table', () => {
    expect(exportFileName('Lakehouse Prod!', 'nodes', 'csv')).toMatch(/^lakehouse-prod-nodes-\d{8}-\d{4}\.csv$/)
    expect(exportFileName(undefined, 'pods', 'xlsx')).toMatch(/^cluster-pods-.*\.xlsx$/)
  })
})
