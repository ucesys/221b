// The table is an ARIA grid: rows are focusable, Enter opens one, the arrow
// keys move between rows and into a row's links, and links inside a row are
// not nested in a button (WCAG 4.1.2, checked by axe in the browser tests).
import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ColumnDef } from '../lib/table'
import { DataTable } from './DataTable'

interface R {
  name: string
  ns: string
}

const cols: ColumnDef<R>[] = [
  { id: 'name', label: 'Name', width: 'minmax(100px, 1fr)', min: 100, cell: (r) => <span className="rk-pname">{r.name}</span>, sortValue: (r) => r.name, export: (r) => [{ head: 'Name', cell: r.name }] },
  { id: 'ns', label: 'Namespace', width: '100px', min: 100, cell: (r) => <a href={`#/namespaces/${r.ns}`}>{r.ns}</a>, sortValue: (r) => r.ns, export: (r) => [{ head: 'Namespace', cell: r.ns }] },
]

const rows: R[] = [
  { name: 'a', ns: 'web' },
  { name: 'b', ns: 'web' },
  { name: 'c', ns: 'batch' },
]

function table(onOpen = vi.fn()) {
  const r = render(<DataTable<R> label="Things" rows={rows} cols={cols} rowKey={(r) => r.name} rowLabel={(r, open) => `${r.name}${open ? ', open' : ''}`} sort={{ col: 'name', dir: 'asc' }} onSort={() => {}} selected="b" onOpen={onOpen} now={0} empty="None" rowAttr="thing" />)
  return { ...r, onOpen, row: (name: string) => r.container.querySelector<HTMLElement>(`[data-thing="${name}"]`)! }
}

afterEach(cleanup)

describe('the grid', () => {
  it('is a grid of rows and cells, with the selected row marked and the sort on the header', () => {
    const { container, getByRole } = table()
    const grid = getByRole('grid', { name: 'Things' })
    expect(grid.getAttribute('aria-rowcount')).toBe('4')
    expect(grid.getAttribute('data-total')).toBe('3')
    expect(container.querySelectorAll('[role="row"]')).toHaveLength(4)
    expect(container.querySelector('[role="columnheader"][aria-sort="ascending"]')?.textContent).toContain('Name')
    expect(container.querySelectorAll('[role="rowheader"]')).toHaveLength(3)
    expect(container.querySelectorAll('[role="gridcell"]')).toHaveLength(3)
    const b = container.querySelector('[data-thing="b"]')!
    expect(b.getAttribute('aria-selected')).toBe('true')
    expect(b.getAttribute('aria-label')).toBe('b, open')
    expect(b.getAttribute('aria-rowindex')).toBe('3')
    expect(container.querySelector('[data-thing="a"]')!.getAttribute('aria-selected')).toBe('false')
    // Rows are not buttons: a link inside one is not a nested control.
    expect(container.querySelectorAll('[role="button"]')).toHaveLength(0)
  })

  it('opens a row with Enter or a click, but not from a link inside it', () => {
    const { onOpen, row } = table()
    fireEvent.keyDown(row('a'), { key: 'Enter' })
    expect(onOpen).toHaveBeenLastCalledWith('a')
    fireEvent.click(row('c'))
    expect(onOpen).toHaveBeenLastCalledWith('c')
    fireEvent.click(row('b').querySelector('a')!)
    expect(onOpen).toHaveBeenCalledTimes(2)
  })

  it('moves between rows with the arrow keys and into a row with Right', () => {
    const { row } = table()
    row('a').focus()
    fireEvent.keyDown(row('a'), { key: 'ArrowDown' })
    expect(document.activeElement).toBe(row('b'))
    fireEvent.keyDown(row('b'), { key: 'ArrowDown' })
    expect(document.activeElement).toBe(row('c'))
    fireEvent.keyDown(row('c'), { key: 'ArrowDown' })
    expect(document.activeElement).toBe(row('c'))
    fireEvent.keyDown(row('c'), { key: 'ArrowUp' })
    expect(document.activeElement).toBe(row('b'))
    fireEvent.keyDown(row('b'), { key: 'ArrowRight' })
    const link = row('b').querySelector('a')!
    expect(document.activeElement).toBe(link)
    fireEvent.keyDown(link, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(row('b'))
  })
})
