import { useCallback, useEffect, useState } from 'react'
import { deleteHelmRepo, fetchHelmRepos, retestHelmRepo, saveHelmRepo, testHelmRepo, type HelmRepository, type HelmRepositoryInput, type RepoTest } from '../../api/apps'
import { fetchCredentials } from '../../api/credentials'
import { fmtTs } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'
import { CredentialPicker } from '../CredentialPicker'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KIND: Record<HelmRepository['kind'], string> = { helm: 'Helm repository', oci: 'OCI registry' }

/** How a repository signs in, in words: its credential by name (CR-11), or its own of before 0.12.1. */
function authText(r: HelmRepository, names: Record<string, string>): string {
  if (r.credential) return names[r.credential] ?? 'A credential'
  if (!r.credMigrated && r.auth === 'basic') return `User ${r.username ?? ''} and a password, kept before 0.12.1`
  if (!r.credMigrated && r.auth === 'token') return 'A token kept before 0.12.1'
  return 'None (public)'
}

/** A repository as the editor holds it: its credential by id, null until chosen. */
interface Draft {
  name: string
  kind: 'helm' | 'oci'
  url: string
  credential: string | null
}

const blank: Draft = { name: '', kind: 'helm', url: '', credential: null }

/**
 * Settings › Applications (Phase 12, wave 5, HR-02, HR-03): the Helm
 * repositories and OCI registries applications install charts from
 * besides git. A SuperAdmin adds, changes, tests and removes them; those
 * who manage applications see the list. Each names a credential of
 * Settings › Credentials (0.12.1, CR-11), chosen by name.
 */
export function HelmReposSettings({ onToast }: { onToast?: (msg: string) => void }) {
  const [list, setList] = useState<HelmRepository[] | null>(null)
  const [mayEdit, setMayEdit] = useState(false)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<{ draft: Draft; saved?: HelmRepository } | null>(null)
  const [removing, setRemoving] = useState<HelmRepository | null>(null)
  const [testing, setTesting] = useState('')
  // The credentials' names, for the list (anyone who reads it may read them).
  const [names, setNames] = useState<Record<string, string>>({})
  useEffect(() => {
    fetchCredentials().then(
      (v) => setNames(Object.fromEntries((v.credentials ?? []).map((c) => [c.id, c.name]))),
      () => undefined,
    )
  }, [list])
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
        Applications install charts from git and from the Helm repositories and OCI registries listed here.
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
                {['Name', 'Kind', 'Address', 'Credential', 'Last test', ''].map((h, i) => (
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
                  <td style={td} data-testid="helm-repo-credential">
                    {authText(r, names)}
                  </td>
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
                        <button
                          className="rk-btn"
                          data-size="sm"
                          onClick={() => setEditing({ saved: r, draft: { name: r.name, kind: r.kind, url: r.url, credential: r.credMigrated || r.credential ? (r.credential ?? '') : null } })}
                          data-testid="helm-repo-edit"
                        >
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

/** The input a draft sends: its kind, address and credential. */
function inputOf(d: Draft): HelmRepositoryInput {
  return { kind: d.kind, url: d.url.trim(), ...(d.credential ? { credential: d.credential } : {}) }
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
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      onSaved(await saveHelmRepo(d.name.trim(), inputOf(d)))
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  const runTest = async () => {
    setBusy(true)
    setError('')
    try {
      setTest(await testHelmRepo({ ...inputOf(d), ...(saved ? { name: saved.name } : {}) }))
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  // Several credentials covering the address: one must be chosen (CR-10).
  const ready = d.name.trim() !== '' && d.url.trim() !== '' && d.credential !== null
  return (
    <div className="rk-card" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="helm-repo-editor">
      <strong style={{ fontSize: 13.5 }}>{saved ? `Change ${saved.name}` : 'Add a repository'}</strong>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '10px 14px' }}>
        <label style={fieldLabel}>
          Name, as helm lines use it (name/chart)
          <span className="rk-field">
            <input className="rk-mono" value={d.name} disabled={!!saved} maxLength={63} onChange={(e) => set({ name: e.target.value.toLowerCase() })} placeholder="bitnami" data-testid="helm-repo-name" />
          </span>
          <span className="rk-fhint">
            In the Helm tab’s new application, and in Watson as <code className="rk-mono rk-icode">helm install NAME REPO/CHART --version V</code> (an OCI registry: <code className="rk-mono rk-icode">oci://REGISTRY/PATH/CHART</code>).
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
        <div style={{ gridColumn: '1 / -1' }}>
          <CredentialPicker
            use="helm"
            address={d.url.trim() && !d.url.includes('://') ? `${d.kind === 'oci' ? 'oci' : 'https'}://${d.url.trim()}` : d.url}
            value={d.credential}
            onChange={(v) => set({ credential: v })}
            label="Credential (user and password, or a token sent as Basic or Bearer; an OCI registry exchanges it for its own token)"
            testId="helm-repo-credential-picker"
          />
        </div>
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
      <p style={{ margin: 0 }}>Applications installed from it stay as they are; their next deploy says the repository is gone, until it is added again. {repo.credential ? 'Its credential stays in Settings › Credentials.' : ''}</p>
    </ConfirmDialog>
  )
}
