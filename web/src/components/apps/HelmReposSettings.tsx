import { useCallback, useEffect, useState } from 'react'
import {
  deleteHelmRepo,
  dismissRepoMigration,
  fetchHelmRepos,
  forgetRepoCert,
  renameHelmRepo,
  retestHelmRepo,
  saveHelmRepo,
  testHelmRepo,
  trustRepoCert,
  type HelmRepository,
  type HelmRepositoryInput,
  type RepoMigration,
  type RepoTest,
} from '../../api/apps'
import type { PinnedWith } from '../../api/certificates'
import { CertificateResult, PinnedCertNote } from '../CertificateTrust'
import { fetchCredentials } from '../../api/credentials'
import { fmtTs } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'
import { CredentialPicker } from '../CredentialPicker'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KIND: Record<HelmRepository['kind'], string> = { git: 'Git repository', helm: 'Helm repository', oci: 'OCI registry' }

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
  kind: HelmRepository['kind']
  url: string
  credential: string | null
  defaultBranch: string
}

const blank: Draft = { name: '', kind: 'git', url: '', credential: null, defaultBranch: '' }

/**
 * Administration › Settings › Repositories (0.12.1, RP-01 to RP-08; Settings ›
 * Applications before, HR-02, HR-03): the git repositories applications'
 * Setup and Mycroft's git sources name, and the Helm repositories and OCI
 * registries applications install charts from. A SuperAdmin adds,
 * changes, renames, tests and removes them (not one in use: Used by says
 * by whom); those who manage applications see the list. Each names a
 * credential of Administration › Credentials (CR-11), chosen by name. What the
 * upgrade made of the git addresses typed before shows until dismissed.
 */
export function HelmReposSettings({ onToast }: { onToast?: (msg: string) => void }) {
  const [list, setList] = useState<HelmRepository[] | null>(null)
  const [mayEdit, setMayEdit] = useState(false)
  const [migration, setMigration] = useState<RepoMigration | null>(null)
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
        setMigration(v.migration ?? null)
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
    <section style={{ display: 'flex', flexDirection: 'column', gap: 14 }} aria-label="Repositories" data-testid="helm-repos-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        Git, Helm and OCI repositories applications and sources come from. An application’s Setup and a Mycroft git source pick a git repository here and keep their own branch, tag and path: an address or a credential changes once, here.
      </p>
      {migration && ((migration.created?.length ?? 0) > 0 || (migration.notes?.length ?? 0) > 0) && (
        <div className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="repos-migration">
          <strong style={{ fontSize: 13 }}>Created from existing settings</strong>
          {(migration.created ?? []).map((c) => (
            <span key={c.name} style={{ fontSize: 12.5 }}>
              <span className="rk-mono">{c.name}</span> <span className="rk-mono">{c.url}</span> <span style={{ color: 'var(--fg3)' }}>from {c.from.join(', ')}</span>
            </span>
          ))}
          {(migration.notes ?? []).map((n) => (
            <span key={n} style={{ fontSize: 12.5, color: 'var(--warn)' }}>
              {n}
            </span>
          ))}
          <span className="rk-fhint">
            One git repository for each address applications and Mycroft’s sources typed before 0.12.1, named after it: rename them as you like (Edit); what uses them follows. They keep their own address too, unchanged.
          </span>
          {mayEdit && (
            <button className="rk-btn" data-size="sm" style={{ alignSelf: 'flex-start' }} onClick={() => void dismissRepoMigration().then(load, (e: unknown) => onToast?.(errorText(e)))} data-testid="repos-migration-dismiss">
              Dismiss
            </button>
          )}
        </div>
      )}
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
                {['Name', 'Kind', 'Address', 'Credential', ...(mayEdit ? ['Used by'] : []), 'Last test', ''].map((h, i) => (
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
                    {r.defaultBranch && <div className="rk-fhint">default branch {r.defaultBranch}</div>}
                  </td>
                  <td style={td} data-testid="helm-repo-credential">
                    {authText(r, names)}
                  </td>
                  {mayEdit && (
                    <td style={{ ...td, fontSize: 12.5 }} data-testid="helm-repo-used-by">
                      {(r.usedBy ?? []).length ? r.usedBy!.join(', ') : <span style={{ color: 'var(--fg3)' }}>Nothing</span>}
                    </td>
                  )}
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
                          onClick={() => setEditing({ saved: r, draft: { name: r.name, kind: r.kind, url: r.url, credential: r.credMigrated || r.credential ? (r.credential ?? '') : null, defaultBranch: r.defaultBranch ?? '' } })}
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
          onPinChanged={load}
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

/** The input a draft sends: its kind, address, credential and a git repository's default branch. */
function inputOf(d: Draft): HelmRepositoryInput {
  return { kind: d.kind, url: d.url.trim(), ...(d.credential ? { credential: d.credential } : {}), ...(d.kind === 'git' && d.defaultBranch.trim() ? { defaultBranch: d.defaultBranch.trim() } : {}) }
}

/** Adds or changes a repository, with Test before or after saving (HR-03). */
function RepoEditor({ initial, saved, onCancel, onSaved, onPinChanged }: { initial: Draft; saved?: HelmRepository; onCancel: () => void; onSaved: (r: HelmRepository) => void; onPinChanged?: () => void }) {
  const [d, setD] = useState<Draft>(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [test, setTest] = useState<RepoTest | null>(null)
  const [pin, setPin] = useState<PinnedWith | undefined>(saved?.pinned)
  // Trust pins the saved repository's certificate: the address and the
  // credential as saved (CR-16).
  const asSaved = !!saved && d.url.trim() === saved.url && (d.credential ?? '') === (saved.credential ?? '')
  const set = (p: Partial<Draft>) => {
    setD((x) => ({ ...x, ...p }))
    setTest(null)
  }
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      // A new name first: what names the repository follows it (RP-05).
      let name = d.name.trim()
      if (saved && name !== saved.name) name = (await renameHelmRepo(saved.name, name)).name
      onSaved(await saveHelmRepo(name, inputOf(d)))
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
          {d.kind === 'git' ? 'Name, short and lowercase' : 'Name, as helm lines use it (name/chart)'}
          <span className="rk-field">
            <input
              className="rk-mono"
              value={d.name}
              disabled={!!saved && !saved.credMigrated && !!saved.hasSecret}
              maxLength={63}
              onChange={(e) => set({ name: e.target.value.toLowerCase() })}
              placeholder={d.kind === 'git' ? 'app' : 'bitnami'}
              data-testid="helm-repo-name"
            />
          </span>
          <span className="rk-fhint">
            {d.kind === 'git' ? (
              <>
                Picked in an application’s Setup and a Mycroft git source, and in Watson as <code className="rk-mono rk-icode">helm install NAME --git REPOSITORY --ref BRANCH</code>.
              </>
            ) : (
              <>
                In the Helm tab’s new application, and in Watson as <code className="rk-mono rk-icode">helm install NAME REPO/CHART --version V</code> (an OCI registry: <code className="rk-mono rk-icode">oci://REGISTRY/PATH/CHART</code>).
              </>
            )}
            {saved ? ' A new name is followed by what uses it.' : ''}
          </span>
        </label>
        <div style={fieldLabel}>
          Kind
          <div className="rk-seg" role="group" aria-label="Kind of repository" style={{ alignSelf: 'flex-start' }}>
            {(saved ? [saved.kind] : (['git', 'helm', 'oci'] as const)).map((k) => (
              <button key={k} type="button" aria-pressed={d.kind === k} onClick={() => set({ kind: k })} data-testid={`helm-repo-kind-${k}`}>
                {KIND[k]}
              </button>
            ))}
          </div>
        </div>
        <label style={{ ...fieldLabel, gridColumn: '1 / -1' }}>
          Address
          <span className="rk-field">
            <input
              className="rk-mono"
              value={d.url}
              onChange={(e) => set({ url: e.target.value })}
              placeholder={d.kind === 'oci' ? 'oci://registry.example.com/charts' : d.kind === 'git' ? 'https://github.com/org/app' : 'https://charts.example.com/stable'}
              data-testid="helm-repo-url"
            />
          </span>
          <span className="rk-fhint">
            {d.kind === 'oci'
              ? 'oci:// with the registry and the path the charts are under; it signs in to every chart under it.'
              : d.kind === 'git'
                ? 'The https:// address of the git repository; GitHub, GitLab and Gitea work.'
                : 'The https:// address of the repository, where its index.yaml is.'}
          </span>
        </label>
        {d.kind === 'git' && (
          <label style={fieldLabel}>
            Default branch (optional)
            <span className="rk-field">
              <input className="rk-mono" value={d.defaultBranch} onChange={(e) => set({ defaultBranch: e.target.value })} placeholder="main" data-testid="helm-repo-branch" />
            </span>
            <span className="rk-fhint">Offered first where the repository is picked; empty leaves it to the repository.</span>
          </label>
        )}
        <div style={{ gridColumn: '1 / -1' }}>
          <CredentialPicker
            use={d.kind === 'git' ? 'git' : 'helm'}
            address={d.url.trim() && !d.url.includes('://') ? `${d.kind === 'oci' ? 'oci' : 'https'}://${d.url.trim()}` : d.url}
            value={d.credential}
            onChange={(v) => set({ credential: v })}
            label={d.kind === 'git' ? 'Credential (a token sent as Basic or Bearer, for a private repository)' : 'Credential (user and password, or a token sent as Basic or Bearer; an OCI registry exchanges it for its own token)'}
            testId="helm-repo-credential-picker"
          />
        </div>
      </div>
      {pin && saved && (
        <PinnedCertNote
          pinned={pin}
          mayForget
          onForget={async () => {
            await forgetRepoCert(saved.name)
            setPin(undefined)
            setTest(null)
            onPinChanged?.()
          }}
        />
      )}
      {test?.certificate && (
        <CertificateResult
          cert={test.certificate}
          mayTrust
          onTrust={
            asSaved && saved
              ? async (sha256) => {
                  setPin(await trustRepoCert(saved.name, sha256))
                  onPinChanged?.()
                }
              : undefined
          }
          unavailable="Save the repository first: its certificate is trusted with it, or with its credential."
        />
      )}
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
  const users = repo.usedBy ?? []
  return (
    <ConfirmDialog title={`Remove ${repo.name}?`} confirm="Remove" danger busy={busy} error={error} onCancel={onCancel} onConfirm={() => void remove()}>
      <p style={{ margin: 0 }}>
        {users.length ? `It is used by ${users.join(', ')}: it stays while they name it; have them pick another repository first.` : 'Nothing names it now.'} {repo.credential ? 'Its credential stays in Administration › Credentials.' : ''}
      </p>
    </ConfirmDialog>
  )
}
