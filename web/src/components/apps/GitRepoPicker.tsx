import { useCallback, useEffect, useState } from 'react'
import { fetchHelmRepos, saveHelmRepo, type HelmRepository } from '../../api/apps'
import { fetchCredentials } from '../../api/credentials'
import { CredentialPicker } from '../CredentialPicker'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** The select's value that opens the form for a new repository. */
const ADD = '__add'

interface Props {
  /** The repository chosen, by name; "" is none yet. */
  value: string
  /** A choice: the repository, or null when none. */
  onChange: (repo: HelmRepository | null) => void
  /** The address kept by a user that names no repository yet (before 0.12.1). */
  keptAddress?: string
  disabled?: boolean
  /** The label over the select; none when the field has its own. */
  label?: string
  testId: string
}

/**
 * A git repository of Administration › Settings › Repositories (0.12.1, RP-03), picked by
 * name for an application's Setup, a new application or a Mycroft git
 * source, which keep their own branch, tag and path. A SuperAdmin adds one
 * here with "Add a repository…" (its name, address, credential and
 * default branch); the others pick from the list. The address and the
 * credential are the repository's: they change once, there.
 */
export function GitRepoPicker({ value, onChange, keptAddress, disabled, label, testId }: Props) {
  const [list, setList] = useState<HelmRepository[] | null>(null)
  const [mayEdit, setMayEdit] = useState(false)
  const [error, setError] = useState('')
  const [adding, setAdding] = useState(false)
  const [names, setNames] = useState<Record<string, string>>({})
  const load = useCallback(
    () =>
      fetchHelmRepos().then(
        (v) => {
          const git = (v.repositories ?? []).filter((r) => r.kind === 'git')
          setList(git)
          setMayEdit(v.mayEdit)
          setError('')
          return git
        },
        (e: unknown) => {
          setList([])
          setError(errorText(e))
          return [] as HelmRepository[]
        },
      ),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])
  useEffect(() => {
    fetchCredentials().then(
      (v) => setNames(Object.fromEntries((v.credentials ?? []).map((c) => [c.id, c.name]))),
      () => undefined,
    )
  }, [])
  const chosen = list?.find((r) => r.name === value)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }} data-testid={testId}>
      {label && <span style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)' }}>{label}</span>}
      <label className="rk-field">
        <select
          aria-label={label || 'Git repository'}
          value={adding ? ADD : value}
          disabled={disabled || !list}
          onChange={(e) => {
            const v = e.target.value
            if (v === ADD) {
              setAdding(true)
              return
            }
            setAdding(false)
            onChange(list?.find((r) => r.name === v) ?? null)
          }}
          data-testid={`${testId}-select`}
        >
          <option value="">{list ? 'Choose a repository…' : 'Loading…'}</option>
          {value && list && !chosen && <option value={value}>{value} (not listed)</option>}
          {(list ?? []).map((r) => (
            <option key={r.name} value={r.name}>
              {r.name} · {r.url}
            </option>
          ))}
          {mayEdit && <option value={ADD}>Add a repository…</option>}
        </select>
      </label>
      <span className="rk-fhint" style={{ fontWeight: 400 }} data-testid={`${testId}-hint`}>
        {error
          ? error
          : chosen
            ? `${chosen.credential ? `Fetched with ${names[chosen.credential] ?? 'its credential'}` : 'Public: no credential'}${chosen.defaultBranch ? ` · default branch ${chosen.defaultBranch}` : ''} · its address and credential change in Administration › Settings › Repositories`
            : keptAddress && !value
              ? `Kept as ${keptAddress}: pick its repository`
              : list && list.length === 0 && !mayEdit
                ? 'None listed yet: a SuperAdmin adds them in Administration › Settings › Repositories'
                : 'As Administration › Settings › Repositories lists them'}
      </span>
      {adding && (
        <AddGitRepo
          testId={`${testId}-new`}
          initialURL={keptAddress ?? ''}
          onCancel={() => setAdding(false)}
          onAdded={(r) => {
            setAdding(false)
            void load().then((git) => onChange(git.find((x) => x.name === r.name) ?? r))
          }}
        />
      )}
    </div>
  )
}

/** A git repository's name from its address: its last segment, lower case. */
export function nameFromAddress(url: string): string {
  const last =
    url
      .trim()
      .replace(/\/+$/, '')
      .replace(/\.git$/, '')
      .split('/')
      .pop() ?? ''
  return last
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
}

/** The inline form a SuperAdmin adds a git repository with. */
function AddGitRepo({ initialURL, onCancel, onAdded, testId }: { initialURL: string; onCancel: () => void; onAdded: (r: HelmRepository) => void; testId: string }) {
  const [url, setURL] = useState(initialURL)
  const [name, setName] = useState(nameFromAddress(initialURL))
  const [named, setNamed] = useState(false)
  const [credential, setCredential] = useState<string | null>(null)
  const [branch, setBranch] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const add = async () => {
    setBusy(true)
    setError('')
    try {
      onAdded(await saveHelmRepo(name.trim(), { kind: 'git', url: url.trim(), ...(credential ? { credential } : {}), ...(branch.trim() ? { defaultBranch: branch.trim() } : {}) }))
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  const field: React.CSSProperties = { display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 0 }
  return (
    <div className="rk-card" style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid={testId}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: '8px 12px' }}>
        <label style={{ ...field, gridColumn: '1 / -1' }}>
          Address (https://)
          <span className="rk-field">
            <input
              className="rk-mono"
              value={url}
              onChange={(e) => {
                setURL(e.target.value)
                if (!named) setName(nameFromAddress(e.target.value))
              }}
              placeholder="https://github.com/org/app"
              data-testid={`${testId}-url`}
            />
          </span>
        </label>
        <label style={field}>
          Name
          <span className="rk-field">
            <input className="rk-mono" value={name} maxLength={63} onChange={(e) => (setName(e.target.value.toLowerCase()), setNamed(true))} placeholder="app" data-testid={`${testId}-name`} />
          </span>
        </label>
        <label style={field}>
          Default branch (optional)
          <span className="rk-field">
            <input className="rk-mono" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" data-testid={`${testId}-branch`} />
          </span>
        </label>
        <div style={{ ...field, gridColumn: '1 / -1' }}>
          <CredentialPicker use="git" address={url} value={credential} onChange={setCredential} label="Credential (for a private repository)" testId={`${testId}-credential`} />
        </div>
      </div>
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12 }} data-testid={`${testId}-error`}>
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: 8 }}>
        <button type="button" className="rk-btn" data-primary="true" disabled={busy || !url.trim() || !name.trim() || credential === null} onClick={() => void add()} data-testid={`${testId}-save`}>
          {busy ? 'Adding…' : 'Add repository'}
        </button>
        <button type="button" className="rk-btn" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  )
}
