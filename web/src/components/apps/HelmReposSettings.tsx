import { useCallback, useEffect, useState } from 'react'
import { deleteHelmRepo, fetchHelmRepos, retestHelmRepo, saveHelmRepo, testHelmRepo, type HelmRepository, type HelmRepositoryInput, type RepoAuth, type RepoTest } from '../../api/apps'
import { fmtTs } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KIND: Record<HelmRepository['kind'], string> = { helm: 'Helm repository', oci: 'OCI registry' }

/** How a repository is signed in to, in words. */
function authText(r: HelmRepository): string {
  if (r.auth === 'basic') return `User ${r.username ?? ''} and a password${r.hasSecret ? '' : ' (not given)'}`
  if (r.auth === 'token') return `A token${r.username ? ` for ${r.username}` : ''}${r.hasSecret ? '' : ' (not given)'}`
  return 'None'
}

/** A repository as the editor holds it. */
interface Draft {
  name: string
  kind: 'helm' | 'oci'
  url: string
  auth: RepoAuth
  username: string
  secret: string
}

const blank: Draft = { name: '', kind: 'helm', url: '', auth: 'none', username: '', secret: '' }

/**
 * Settings › Applications (Phase 12, wave 5, HR-02, HR-03): the Helm
 * repositories and OCI registries applications install charts from
 * besides git. A SuperAdmin adds, changes, tests and removes them; those
 * who manage applications see the list. A password or token is written
 * and never shown again: the list says whether one is kept.
 */
export function HelmReposSettings({ onToast }: { onToast?: (msg: string) => void }) {
  const [list, setList] = useState<HelmRepository[] | null>(null)
  const [mayEdit, setMayEdit] = useState(false)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<{ draft: Draft; saved?: HelmRepository } | null>(null)
  const [removing, setRemoving] = useState<HelmRepository | null>(null)
  const [testing, setTesting] = useState('')
  const load = useCallback(() => {
    fetchHelmRepos().then(
      (v) => {
        setList(v.repositories ?? [])
        setMayEdit(v.mayEdit)
        setError('')
      },
      (e: unknown) => setError(errorText(e)),
    )
  }, [])
  useEffect(() => load(), [load])
  const retest = async (r: HelmRepository) => {
    setTesting(r.name)
    try {
      const t = await retestHelmRepo(r.name)
      onToast?.(t.ok ? t.text : `${r.name}: ${t.text}`)
      load()
    } catch (e) {
      onToast?.(errorText(e))
    } finally {
      setTesting('')
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 14 }} aria-label="Helm repositories" data-testid="helm-repos-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        Applications install charts from git, and from the Helm repositories and OCI registries listed here: in the Helm tab’s new application, and in Watson as <code className="rk-mono rk-icode">helm install NAME REPO/CHART --version V</code> or <code className="rk-mono rk-icode">oci://REGISTRY/PATH/CHART</code>. An OCI registry’s entry signs in to every chart under its address. Passwords and tokens are kept in a Secret, never shown again, never in a backup.
      </p>
      {error && (
        <p className="rk-t" data-tone="err" style={{ margin: 0 }} data-testid="helm-repos-error">
          {error}
        </p>
      )}
      {!list && !error && <p className="rk-fhint">Loading…</p>}
      {list && list.length === 0 && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="helm-repos-empty">
          No repositories yet{mayEdit ? ': add one.' : '; a SuperAdmin adds them.'}
        </p>
      )}
      {list && list.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 640, fontSize: 13 }} data-testid="helm-repos-table">
            <thead>
              <tr>
                {['Name', 'Kind', 'Address', 'Sign-in', 'Last test', ''].map((h, i) => (
                  <th key={i} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {list.map((r) => (
                <tr key={r.name} data-testid="helm-repo-row" data-repo={r.name}>
                  <td style={td} className="rk-mono">
                    {r.name}
                  </td>
                  <td style={td}>{KIND[r.kind]}</td>
                  <td style={{ ...td, overflowWrap: 'anywhere' }} className="rk-mono">
                    {r.url}
                  </td>
                  <td style={td}>{authText(r)}</td>
                  <td style={td} data-testid="helm-repo-tested">
                    {r.testedAt ? (
                      <span title={r.tested} style={{ color: r.testOk ? undefined : 'var(--err)' }}>
                        {r.testOk ? 'Answered' : 'Failed'}, {fmtTs(r.testedAt)}
                      </span>
                    ) : (
                      <span style={{ color: 'var(--fg3)' }}>Not tested</span>
                    )}
                  </td>
                  <td style={{ ...td, whiteSpace: 'nowrap', textAlign: 'right' }}>
                    {mayEdit && (
                      <span style={{ display: 'inline-flex', gap: 6 }}>
                        <button className="rk-btn" data-size="sm" disabled={testing === r.name} onClick={() => void retest(r)} data-testid="helm-repo-test">
                          {testing === r.name ? 'Testing…' : 'Test'}
                        </button>
                        <button className="rk-btn" data-size="sm" onClick={() => setEditing({ saved: r, draft: { name: r.name, kind: r.kind, url: r.url, auth: r.auth, username: r.username ?? '', secret: '' } })} data-testid="helm-repo-edit">
                          Edit
                        </button>
                        <button className="rk-btn" data-size="sm" data-danger="true" onClick={() => setRemoving(r)} data-testid="helm-repo-remove">
                          Remove
                        </button>
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {mayEdit && !editing && (
        <button className="rk-btn" data-primary="true" style={{ alignSelf: 'flex-start' }} onClick={() => setEditing({ draft: blank })} data-testid="helm-repo-add">
          Add repository
        </button>
      )}
      {editing && (
        <RepoEditor
          key={editing.saved?.name ?? 'new'}
          initial={editing.draft}
          saved={editing.saved}
          onCancel={() => setEditing(null)}
          onSaved={(r) => {
            setEditing(null)
            onToast?.(`${r.name} saved.`)
            load()
          }}
        />
      )}
      {removing && (
        <RemoveRepo
          repo={removing}
          onCancel={() => setRemoving(null)}
          onRemoved={() => {
            setRemoving(null)
            onToast?.(`${removing.name} removed.`)
            load()
          }}
        />
      )}
    </section>
  )
}

const th: React.CSSProperties = { fontSize: 12, color: 'var(--fg2)', fontWeight: 500, textAlign: 'left', padding: '8px 10px', borderBottom: '1px solid var(--line)' }
const td: React.CSSProperties = { padding: '9px 10px', borderBottom: '1px solid var(--line)', verticalAlign: 'top' }
const fieldLabel: React.CSSProperties = { display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }

/** The input a draft sends: the secret only when one was typed, or to remove it with sign-in none. */
function inputOf(d: Draft, saved?: HelmRepository): HelmRepositoryInput {
  const input: HelmRepositoryInput = { kind: d.kind, url: d.url.trim(), auth: d.auth }
  if (d.auth !== 'none') input.username = d.username.trim()
  if (d.secret) input.secret = d.secret
  else if (d.auth === 'none' && saved?.hasSecret) input.secret = ''
  return input
}

/** Adds or changes a repository, with Test before or after saving (HR-03). */
function RepoEditor({ initial, saved, onCancel, onSaved }: { initial: Draft; saved?: HelmRepository; onCancel: () => void; onSaved: (r: HelmRepository) => void }) {
  const [d, setD] = useState<Draft>(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [test, setTest] = useState<RepoTest | null>(null)
  const set = (p: Partial<Draft>) => {
    setD((x) => ({ ...x, ...p }))
    setTest(null)
  }
  const moved = !!saved && saved.hasSecret && d.url.trim() !== saved.url
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      onSaved(await saveHelmRepo(d.name.trim(), inputOf(d, saved)))
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  const runTest = async () => {
    setBusy(true)
    setError('')
    try {
      setTest(await testHelmRepo({ ...inputOf(d, saved), ...(saved ? { name: saved.name } : {}) }))
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const ready = d.name.trim() !== '' && d.url.trim() !== '' && (d.auth === 'none' || ((d.auth !== 'basic' || d.username.trim() !== '') && (d.secret !== '' || (!!saved?.hasSecret && !moved))))
  return (
    <div className="rk-card" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="helm-repo-editor">
      <strong style={{ fontSize: 13.5 }}>{saved ? `Change ${saved.name}` : 'Add a repository'}</strong>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '10px 14px' }}>
        <label style={fieldLabel}>
          Name, as helm lines use it (name/chart)
          <span className="rk-field">
            <input className="rk-mono" value={d.name} disabled={!!saved} maxLength={63} onChange={(e) => set({ name: e.target.value.toLowerCase() })} placeholder="bitnami" data-testid="helm-repo-name" />
          </span>
        </label>
        <div style={fieldLabel}>
          Kind
          <div className="rk-seg" role="group" aria-label="Kind of repository" style={{ alignSelf: 'flex-start' }}>
            {(['helm', 'oci'] as const).map((k) => (
              <button key={k} type="button" aria-pressed={d.kind === k} onClick={() => set({ kind: k })} data-testid={`helm-repo-kind-${k}`}>
                {KIND[k]}
              </button>
            ))}
          </div>
        </div>
        <label style={{ ...fieldLabel, gridColumn: '1 / -1' }}>
          Address
          <span className="rk-field">
            <input className="rk-mono" value={d.url} onChange={(e) => set({ url: e.target.value })} placeholder={d.kind === 'oci' ? 'oci://registry.example.com/charts' : 'https://charts.example.com/stable'} data-testid="helm-repo-url" />
          </span>
          <span className="rk-fhint">{d.kind === 'oci' ? 'oci:// with the registry and the path the charts are under; it signs in to every chart under it.' : 'The https:// address of the repository, where its index.yaml is.'}</span>
        </label>
        <div style={fieldLabel}>
          Sign-in
          <div className="rk-seg" role="group" aria-label="Sign-in" style={{ alignSelf: 'flex-start' }}>
            {(
              [
                ['none', 'None'],
                ['basic', 'User and password'],
                ['token', 'Token'],
              ] as const
            ).map(([k, l]) => (
              <button key={k} type="button" aria-pressed={d.auth === k} onClick={() => set({ auth: k })} data-testid={`helm-repo-auth-${k}`}>
                {l}
              </button>
            ))}
          </div>
        </div>
        {d.auth !== 'none' && (
          <label style={fieldLabel}>
            User name{d.auth === 'token' ? ' (optional; some registries want one with the token)' : ''}
            <span className="rk-field">
              <input value={d.username} autoComplete="off" onChange={(e) => set({ username: e.target.value })} data-testid="helm-repo-username" />
            </span>
          </label>
        )}
        {d.auth !== 'none' && (
          <label style={fieldLabel}>
            {d.auth === 'basic' ? 'Password' : 'Token'}
            <span className="rk-field">
              <input type="password" autoComplete="new-password" value={d.secret} onChange={(e) => set({ secret: e.target.value })} placeholder={saved?.hasSecret && !moved ? '•••••••• kept; type to replace' : ''} data-testid="helm-repo-secret" />
            </span>
            {moved && <span className="rk-fhint">The address changed: give it again, as the kept one belongs to the old address.</span>}
          </label>
        )}
      </div>
      {test && (
        <p className="rk-t" data-tone={test.ok ? 'ok' : 'err'} style={{ margin: 0, fontSize: 12.5 }} data-testid="helm-repo-test-result">
          {test.ok ? '✓ ' : '✗ '}
          {test.text}
        </p>
      )}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="helm-repo-editor-error">
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button className="rk-btn" disabled={busy || d.url.trim() === ''} onClick={() => void runTest()} data-testid="helm-repo-try">
          Test
        </button>
        <span style={{ flex: '1 1 auto' }} />
        <button className="rk-btn" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
        <button className="rk-btn" data-primary="true" disabled={busy || !ready} onClick={() => void save()} data-testid="helm-repo-save">
          {busy ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

function RemoveRepo({ repo, onCancel, onRemoved }: { repo: HelmRepository; onCancel: () => void; onRemoved: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const remove = async () => {
    setBusy(true)
    try {
      await deleteHelmRepo(repo.name)
      onRemoved()
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  return (
    <ConfirmDialog title={`Remove ${repo.name}?`} confirm="Remove" danger busy={busy} error={error} onCancel={onCancel} onConfirm={() => void remove()}>
      <p style={{ margin: 0 }}>
        Its {repo.hasSecret ? 'password or token is deleted with it. ' : ''}Applications installed from it stay as they are; their next deploy says the repository is gone, until it is added again.
      </p>
    </ConfirmDialog>
  )
}
