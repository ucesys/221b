import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isHidden, MASK, shownValue, valueChanges, type VariableRow } from '../../lib/variables'
import { VariableDrawer } from './VariableDrawer'

const digest = 'hidden:0123456789abcdef01234567'
const secret: VariableRow = { name: 'DB_PASSWORD', value: digest, from: 'web', own: true, sensitive: true, over: [{ node: 'web-2', value: 'hidden:89abcdef0123456789abcdef', from: 'its own value', own: true }] }
const plain: VariableRow = { name: 'MODE', value: 'fast', from: 'web', own: true, sensitive: false, over: [] }

beforeEach(() => {
  // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
  Element.prototype.scrollTo = () => {}
})
afterEach(cleanup)

function open(row: VariableRow, onSave = vi.fn().mockResolvedValue(undefined)) {
  render(<VariableDrawer row={row} rows={[row]} place="web" onNode={false} scripts={[]} onSave={onSave} onDropOverride={vi.fn()} onShowNode={vi.fn()} onClose={vi.fn()} />)
  return onSave
}

describe('a sensitive variable is never shown, nor its digest (DB-71)', () => {
  it('shows a sensitive value as a mask and whether it is set', () => {
    expect(isHidden(digest)).toBe(true)
    expect(isHidden('fast')).toBe(false)
    expect(shownValue(digest, true)).toBe(`${MASK} set`)
    expect(shownValue(undefined, true)).toBe('not set')
    expect(shownValue('', true)).toBe('not set')
    expect(shownValue('fast', false)).toBe('fast')
    expect(shownValue(digest, false)).toBe(`${MASK} set`)
  })

  it('says a change of a sensitive value without its digest', () => {
    const list = valueChanges({ values: { DB_PASSWORD: digest, MODE: 'slow' } }, { values: { DB_PASSWORD: 'hidden:ffffffffffffffffffffffff', MODE: 'fast' } })
    expect(JSON.stringify(list)).not.toContain('hidden:')
    expect(list).toContainEqual({ what: 'DB_PASSWORD', before: MASK, after: `a new value (${MASK})` })
    expect(list).toContainEqual({ what: 'MODE', before: 'slow', after: 'fast' })
  })

  it('opens a sensitive value empty, says where it is kept, and keeps it when nothing is typed', async () => {
    const onSave = open(secret)
    const input = screen.getByTestId('param-value') as HTMLInputElement
    expect(input.value).toBe('')
    expect(input.type).toBe('password')
    expect(document.body.textContent).not.toContain('hidden:')
    expect(document.body.textContent).not.toContain('ConfigMap')
    expect(screen.getByTestId('param-value-hint').textContent).toContain('cluster’s Secret')
    expect(screen.getByTestId('param-override').textContent).toContain(`${MASK} set`)
    // Nothing changed: nothing to save. Marked not sensitive, the digest goes back as "unchanged".
    expect((screen.getByTestId('param-save') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByTestId('param-sensitive'))
    fireEvent.click(screen.getByTestId('param-save'))
    await waitFor(() => expect(onSave).toHaveBeenCalled())
    expect(onSave.mock.calls[0]![0]).toEqual({ old: 'DB_PASSWORD', name: 'DB_PASSWORD', value: digest, sensitive: false })
  })

  it('replaces a sensitive value with what is typed', async () => {
    const onSave = open(secret)
    fireEvent.change(screen.getByTestId('param-value'), { target: { value: 'n3w-s3cret' } })
    fireEvent.click(screen.getByTestId('param-save'))
    await waitFor(() => expect(onSave).toHaveBeenCalled())
    expect(onSave.mock.calls[0]![0]).toEqual({ old: 'DB_PASSWORD', name: 'DB_PASSWORD', value: 'n3w-s3cret', sensitive: true })
  })

  it('shows a plain value, and says it is kept in the database in plain text', () => {
    open(plain)
    expect((screen.getByTestId('param-value') as HTMLInputElement).value).toBe('fast')
    expect(screen.getByTestId('param-value-hint').textContent).toContain('plain text in the console’s database')
  })
})
