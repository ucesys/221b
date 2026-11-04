// Replacing a value (SE-02) and the stale-edit refusal (SE-04).
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { deleteSecretCommand, saveProblem, SecretData, SecretDelete } from './SecretData'
import { WatsonContext } from './Watson'
import { SecretsError, updateSecret } from '../api/secrets'

vi.mock('../api/secrets', async (original) => ({
  ...(await original<typeof import('../api/secrets')>()),
  fetchSecretsConfig: vi.fn(async () => ({ read: true, reveal: false, edit: true })),
  updateSecret: vi.fn(async () => undefined),
}))

const keys = [
  { key: 'password', bytes: 8 },
  { key: 'token', bytes: 4 },
]

async function editor(onToast = vi.fn()) {
  const view = render(<SecretData namespace="web" name="db" keys={keys} resourceVersion="7" onToast={onToast} />)
  fireEvent.click(await screen.findByTestId('secret-edit'))
  return view
}

describe('SecretData', () => {
  beforeEach(() => vi.mocked(updateSecret).mockClear())
  afterEach(cleanup)

  it('does not save a key left on Replace with nothing typed, and says so on the field', async () => {
    await editor()
    fireEvent.click(screen.getByTestId('secret-replace-password'))
    fireEvent.click(screen.getByTestId('secret-save'))
    expect(updateSecret).not.toHaveBeenCalled()
    const box = screen.getByTestId('secret-draft-password')
    const note = screen.getByTestId('secret-blank-password')
    expect(box.getAttribute('aria-invalid')).toBe('true')
    expect(box.getAttribute('aria-describedby')).toBe(note.id)
    expect(document.activeElement).toBe(box)
    // Typing settles it.
    fireEvent.change(box, { target: { value: 'new-value' } })
    expect(screen.queryByTestId('secret-blank-password')).toBeNull()
    fireEvent.click(screen.getByTestId('secret-save'))
    await waitFor(() => expect(updateSecret).toHaveBeenCalledWith('web', 'db', { set: { password: 'new-value' }, remove: [], resourceVersion: '7' }))
  })

  it('lets Keep as is or Remove settle a blank field', async () => {
    await editor()
    fireEvent.click(screen.getByTestId('secret-replace-password'))
    fireEvent.click(screen.getByTestId('secret-save'))
    fireEvent.click(screen.getByText('Keep as is'))
    expect(screen.queryByTestId('secret-blank-password')).toBeNull()
    fireEvent.click(screen.getByTestId('secret-replace-token'))
    fireEvent.click(screen.getByTestId('secret-remove-token'))
    fireEvent.click(screen.getByTestId('secret-save'))
    await waitFor(() => expect(updateSecret).toHaveBeenCalledWith('web', 'db', { set: {}, remove: ['token'], resourceVersion: '7' }))
  })

  it('sends the version the edit started from, not a newer one read since', async () => {
    const { rerender } = await editor()
    rerender(<SecretData namespace="web" name="db" keys={keys} resourceVersion="8" onToast={vi.fn()} />)
    fireEvent.click(screen.getByTestId('secret-replace-token'))
    fireEvent.change(screen.getByTestId('secret-draft-token'), { target: { value: 'x' } })
    fireEvent.click(screen.getByTestId('secret-save'))
    await waitFor(() => expect(updateSecret).toHaveBeenCalledWith('web', 'db', { set: { token: 'x' }, remove: [], resourceVersion: '7' }))
  })

  it('shows the stale-edit refusal in plain words and keeps what was typed', async () => {
    vi.mocked(updateSecret).mockRejectedValueOnce(new SecretsError('the Secret changed since you opened it', 409))
    await editor()
    fireEvent.click(screen.getByTestId('secret-replace-token'))
    fireEvent.change(screen.getByTestId('secret-draft-token'), { target: { value: 'x' } })
    fireEvent.click(screen.getByTestId('secret-save'))
    expect((await screen.findByRole('alert')).textContent).toContain('This Secret changed after you opened it, so nothing was saved.')
    expect((screen.getByTestId('secret-draft-token') as HTMLTextAreaElement).value).toBe('x')
  })

  it('words only that refusal', () => {
    expect(saveProblem(new SecretsError('the Secret is immutable: Kubernetes refuses to change it', 409))).toBe('the Secret is immutable: Kubernetes refuses to change it')
    expect(saveProblem(new SecretsError('forbidden', 403))).toBe('forbidden')
  })
})

describe('Delete', () => {
  afterEach(cleanup)

  it('asks first, then hands the deletion of the Secret to Watson', async () => {
    const send = vi.fn()
    const onToast = vi.fn()
    render(
      <WatsonContext.Provider value={{ available: true, readOnly: false, send }}>
        <SecretDelete namespace="web" name="db" keys={keys.length} onToast={onToast} />
      </WatsonContext.Provider>,
    )
    fireEvent.click(await screen.findByTestId('secret-delete'))
    expect(send).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog').textContent).toContain('2 keys go for good')
    fireEvent.click(screen.getByRole('button', { name: 'Send to Watson' }))
    expect(send).toHaveBeenCalledWith('kubectl delete secret db -n web', expect.objectContaining({ why: 'Delete Secret web/db.', over: true }))
    expect(onToast).toHaveBeenCalled()
  })

  it('is off where Watson changes nothing, and absent where Watson is off', async () => {
    const { unmount } = render(
      <WatsonContext.Provider value={{ available: true, readOnly: true, how: 'read-only connection', send: vi.fn() }}>
        <SecretDelete namespace="web" name="db" keys={keys.length} onToast={vi.fn()} />
      </WatsonContext.Provider>,
    )
    expect((await screen.findByTestId('secret-delete')).hasAttribute('disabled')).toBe(true)
    unmount()
    render(<SecretDelete namespace="web" name="db" keys={keys.length} onToast={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByTestId('secret-delete')).toBeNull()
  })

  it('is not in the keys', async () => {
    render(
      <WatsonContext.Provider value={{ available: true, readOnly: false, send: vi.fn() }}>
        <SecretData namespace="web" name="db" keys={keys} onToast={vi.fn()} />
      </WatsonContext.Provider>,
    )
    await screen.findByTestId('secret-edit')
    expect(screen.queryByTestId('secret-delete')).toBeNull()
  })

  it('names the Secret and its namespace in the command', () => {
    expect(deleteSecretCommand('db', 'creds')).toBe('kubectl delete secret creds -n db')
  })
})
