import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/lessons'
import { NewExceptionDrawer } from './NewException'

vi.mock('../../api/lessons', async (original) => ({
  ...(await original<typeof import('../../api/lessons')>()),
  createException: vi.fn(),
  fetchCaseObjects: vi.fn(),
}))

const clusters: api.ExceptionCluster[] = [{ id: 'local', name: 'Lakehouse', rules: [{ id: 'rbac.wide' }] }]
const objects: api.CaseObject[] = [
  { object: 'sandbox-users/ServiceAccount/kevin', kind: 'ServiceAccount', namespace: 'sandbox-users', name: 'kevin', from: 'cases' },
  { object: 'ClusterRoleBinding/sandbox-user-kevin', kind: 'ClusterRoleBinding', name: 'sandbox-user-kevin', from: 'cluster' },
]
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

describe('New exception: the object is suggested as it is typed', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
    Element.prototype.scrollTo = () => {}
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('suggests nothing before two characters, then the objects with their kind, the cases’ marked', async () => {
    vi.mocked(api.fetchCaseObjects).mockResolvedValue({ objects })
    render(<NewExceptionDrawer clusters={clusters} onClose={() => {}} onCreated={() => {}} />)
    const field = screen.getByTestId('exception-new-object')
    expect(field.getAttribute('role')).toBe('combobox')
    fireEvent.focus(field)
    fireEvent.change(field, { target: { value: 'k' } })
    await wait(350)
    expect(api.fetchCaseObjects).not.toHaveBeenCalled()
    expect(screen.queryByTestId('exception-new-objects')).toBeNull()
    expect(field.getAttribute('aria-expanded')).toBe('false')
    fireEvent.change(field, { target: { value: 'ke' } })
    fireEvent.change(field, { target: { value: 'kev' } })
    const list = await screen.findByTestId('exception-new-objects')
    // Typing on within the pause asks once, for what was typed last.
    expect(api.fetchCaseObjects).toHaveBeenCalledTimes(1)
    expect(api.fetchCaseObjects).toHaveBeenCalledWith('local', 'kev')
    expect(list.getAttribute('role')).toBe('listbox')
    expect(field.getAttribute('aria-expanded')).toBe('true')
    expect(field.getAttribute('aria-controls')).toBe(list.id)
    expect(
      within(list)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['sandbox-users/ServiceAccount/kevinServiceAccount · in cases', 'ClusterRoleBinding/sandbox-user-kevinClusterRoleBinding'])
  })

  it('picks with the keyboard, closes with Escape without closing the drawer, and saves the pick', async () => {
    vi.mocked(api.fetchCaseObjects).mockResolvedValue({ objects })
    vi.mocked(api.createException).mockResolvedValue({ id: 'EX-9' } as api.AcceptedException)
    const onClose = vi.fn()
    const onCreated = vi.fn()
    render(<NewExceptionDrawer clusters={clusters} onClose={onClose} onCreated={onCreated} />)
    const field = screen.getByTestId('exception-new-object')
    fireEvent.focus(field)
    fireEvent.change(field, { target: { value: 'kevin' } })
    const list = await screen.findByTestId('exception-new-objects')
    const options = within(list).getAllByRole('option')
    fireEvent.keyDown(field, { key: 'ArrowDown' })
    expect(field.getAttribute('aria-activedescendant')).toBe(options[0]!.id)
    expect(options[0]!.getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(field, { key: 'ArrowDown' })
    expect(field.getAttribute('aria-activedescendant')).toBe(options[1]!.id)
    fireEvent.keyDown(field, { key: 'ArrowDown' })
    expect(field.getAttribute('aria-activedescendant')).toBe(options[0]!.id)
    fireEvent.keyDown(field, { key: 'ArrowUp' })
    expect(field.getAttribute('aria-activedescendant')).toBe(options[1]!.id)
    fireEvent.keyDown(field, { key: 'Enter' })
    expect((field as HTMLInputElement).value).toBe('ClusterRoleBinding/sandbox-user-kevin')
    expect(screen.queryByTestId('exception-new-objects')).toBeNull()
    // The pick is not asked about again.
    await wait(300)
    expect(api.fetchCaseObjects).toHaveBeenCalledTimes(1)

    // Typing again suggests again; Escape closes the list, not the drawer.
    fireEvent.change(field, { target: { value: 'sandbox' } })
    await screen.findByTestId('exception-new-objects')
    fireEvent.keyDown(field, { key: 'Escape' })
    expect(screen.queryByTestId('exception-new-objects')).toBeNull()
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByTestId('exception-new')).toBeTruthy()
    // A click picks too.
    fireEvent.keyDown(field, { key: 'ArrowDown' })
    fireEvent.click(within(await screen.findByTestId('exception-new-objects')).getAllByRole('option')[0]!)
    expect((field as HTMLInputElement).value).toBe('sandbox-users/ServiceAccount/kevin')

    fireEvent.change(screen.getByTestId('exception-new-rule'), { target: { value: 'rbac.wide' } })
    fireEvent.change(screen.getByTestId('exception-new-reason'), { target: { value: 'Kevin runs the sandbox.' } })
    fireEvent.click(screen.getByTestId('exception-new-save'))
    await waitFor(() => expect(api.createException).toHaveBeenCalledWith({ cluster: 'local', resource: 'sandbox-users/ServiceAccount/kevin', check: 'rbac.wide', reason: 'Kevin runs the sandbox.' }))
    await waitFor(() => expect(onCreated).toHaveBeenCalled())
  })

  it('still saves what was typed when nothing is suggested or the suggestions fail', async () => {
    vi.mocked(api.fetchCaseObjects).mockRejectedValueOnce(new Error('The server could not be reached.')).mockResolvedValue({ objects: [] })
    vi.mocked(api.createException).mockResolvedValue({ id: 'EX-10' } as api.AcceptedException)
    render(<NewExceptionDrawer clusters={clusters} onClose={() => {}} onCreated={() => {}} />)
    const field = screen.getByTestId('exception-new-object')
    fireEvent.focus(field)
    fireEvent.change(field, { target: { value: 'shop/Widget/w1' } })
    await waitFor(() => expect(api.fetchCaseObjects).toHaveBeenCalledTimes(1))
    fireEvent.change(field, { target: { value: 'shop/Widget/w2' } })
    await waitFor(() => expect(api.fetchCaseObjects).toHaveBeenCalledTimes(2))
    expect(screen.queryByTestId('exception-new-objects')).toBeNull()
    fireEvent.keyDown(field, { key: 'Enter' })
    expect((field as HTMLInputElement).value).toBe('shop/Widget/w2')
    fireEvent.change(screen.getByTestId('exception-new-rule'), { target: { value: 'rbac.wide' } })
    fireEvent.change(screen.getByTestId('exception-new-reason'), { target: { value: 'A custom resource.' } })
    fireEvent.click(screen.getByTestId('exception-new-save'))
    await waitFor(() => expect(api.createException).toHaveBeenCalledWith({ cluster: 'local', resource: 'shop/Widget/w2', check: 'rbac.wide', reason: 'A custom resource.' }))
  })

  it('asks nothing before a cluster is picked', async () => {
    render(<NewExceptionDrawer clusters={[...clusters, { id: 'prod', name: 'Production', rules: [] }]} onClose={() => {}} onCreated={() => {}} />)
    fireEvent.change(screen.getByTestId('exception-new-object'), { target: { value: 'kevin' } })
    await wait(300)
    expect(api.fetchCaseObjects).not.toHaveBeenCalled()
  })
})
