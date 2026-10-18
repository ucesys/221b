import { useState } from 'react'
import { createUser, setSuperRoles, type LocalGroup, type SuperRole } from '../api/admin'
import { groupChoice, passwordProblem, usernameProblem } from '../lib/admin'

interface Props {
  groups: LocalGroup[]
  onClose: () => void
  onDone: (message: string) => void
}

/** What a new user may do, chosen as they are created (6.3 review). */
const ACCESS: { id: SuperRole | ''; label: string; hint: string }[] = [
  { id: '', label: 'Only what their groups give', hint: 'Without a group they see the nodes and nothing else.' },
  { id: 'superViewer', label: 'SuperViewer', hint: 'Reads everything on every cluster; changes nothing.' },
  { id: 'superAdmin', label: 'SuperAdmin', hint: 'Everything on every cluster, and this Administration area.' },
]

/** Creates a local user (RB-46): name, password, what they may do. */
export function CreateUser({ groups, onClose, onDone }: Props) {
  const [username, setUsername] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [access, setAccess] = useState<SuperRole | ''>('')
  const [chosen, setChosen] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [touched, setTouched] = useState(false)

  const nameProblem = usernameProblem(username)
  const pwProblem = passwordProblem(password, username)
  const againProblem = again !== password ? 'The two passwords are not the same.' : ''
  const valid = !nameProblem && !pwProblem && !againProblem

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!valid) return
    setBusy(true)
    setError('')
    try {
      await createUser({ username, name: name.trim(), email: email.trim() || undefined, password, groups: chosen })
      if (access) {
        try {
          await setSuperRoles(username, [access])
        } catch (err) {
          onDone(`Created ${username}, but could not make them ${access === 'superAdmin' ? 'SuperAdmin' : 'SuperViewer'}: ${err instanceof Error ? err.message : String(err)}`)
          return
        }
      }
      onDone(`Created ${username}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }
  const field = { display: 'flex', flexDirection: 'column' as const, gap: 6, fontSize: 12, color: 'var(--fg3)' }
  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 50, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16 }}>
      <button aria-label="Close" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <form
        role="dialog"
        aria-modal="true"
        aria-label="Create local user"
        className="rk-card"
        data-testid="create-user"
        onSubmit={(e) => void submit(e)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: 'min(480px, 100%)', maxHeight: '100%', overflow: 'auto', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Create local user</h2>
        <label style={field}>
          Username
          <span className="rk-field">
            <input type="text" autoFocus autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} aria-invalid={touched && !!nameProblem} data-testid="new-username" />
          </span>
          <span className="rk-fhint" role={touched && nameProblem ? 'alert' : undefined} style={touched && nameProblem ? { color: 'var(--err)' } : undefined}>
            {touched && nameProblem ? nameProblem : 'Lowercase letters, digits, dot, dash and underscore.'}
          </span>
        </label>
        <label style={field}>
          Display name (optional)
          <span className="rk-field">
            <input type="text" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />
          </span>
        </label>
        <label style={field}>
          Email (optional)
          <span className="rk-field">
            <input type="email" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
          </span>
        </label>
        <label style={field}>
          Password
          <span className="rk-field">
            <input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} aria-invalid={touched && !!pwProblem} data-testid="new-password" />
          </span>
          <span className="rk-fhint" role={touched && pwProblem ? 'alert' : undefined} style={touched && pwProblem ? { color: 'var(--err)' } : undefined}>
            {touched && pwProblem ? pwProblem : 'At least 12 characters, and not the username.'}
          </span>
        </label>
        <label style={field}>
          Repeat the password
          <span className="rk-field">
            <input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} aria-invalid={(touched || again.length >= password.length) && !!again && !!againProblem} data-testid="new-again" />
          </span>
          {(touched || (again && again.length >= password.length)) && againProblem && (
            <span className="rk-fhint" role="alert" style={{ color: 'var(--err)' }}>
              {againProblem}
            </span>
          )}
        </label>
        <fieldset style={{ border: 0, margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="new-access">
          <legend style={{ fontSize: 12, color: 'var(--fg3)', padding: 0, marginBottom: 6 }}>What they can do</legend>
          {ACCESS.map((a) => (
            <label key={a.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 12.5 }}>
              <input type="radio" name="new-access" checked={access === a.id} onChange={() => setAccess(a.id)} value={a.id || 'groups'} style={{ marginTop: 3 }} />
              <span style={{ display: 'flex', flexDirection: 'column' }}>
                <span>{a.label}</span>
                <span className="rk-fhint">{a.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>
        {groups.length > 0 && (
          <fieldset style={{ border: 0, margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <legend style={{ fontSize: 12, color: 'var(--fg3)', padding: 0, marginBottom: 6 }}>Groups (optional): each gives a role in some namespaces</legend>
            {groups.map((g) => (
              <label key={g.name} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
                <input type="checkbox" checked={chosen.includes(g.name)} onChange={(e) => setChosen(e.target.checked ? [...chosen, g.name] : chosen.filter((x) => x !== g.name))} />
                {groupChoice(g)}
              </label>
            ))}
          </fieldset>
        )}
        {groups.length === 0 && !access && (
          <p className="rk-fhint" style={{ margin: 0 }}>
            No local groups are defined in values.yaml, so without a super role they see only the nodes.
          </p>
        )}
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button type="button" className="rk-btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="rk-btn" data-primary="true" aria-busy={busy} disabled={busy} data-testid="create-submit">
            Create user
          </button>
        </div>
      </form>
    </div>
  )
}
