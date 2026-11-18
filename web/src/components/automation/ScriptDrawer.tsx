// A script's drawer: its text, its versions and what may be done with them.
import { useEffect, useRef, useState } from 'react'
import {
  approvedOf,
  approveVersion,
  rejectVersion,
  revokeVersion,
  canApprove,
  canReject,
  checkScript,
  createScript,
  deleteScript,
  fetchScript,
  latestOf,
  scriptKind,
  privilegeText,
  NEW_SCRIPT,
  mayRun,
  draftScript,
  reviewVersion,
  saveScript,
  SCRIPT_NAME,
  SCRIPT_TEMPLATES,
  submitVersion,
  versionTone,
  type AutomationIndex,
  type Finding,
  type Header,
  type Script,
  type Version,
} from '../../api/automation'
import { fmtTs } from '../../lib/format'
import { lineDiff } from '../../lib/linediff'
import { DiffLines } from '../apps/DiffView'
import { CodeArea, CodeView } from './CodeEditor'
import { ConfirmDialog } from '../ConfirmDialog'
import { DecideDialog } from './DecideDialog'
import { Drawer } from '../Drawer'
import { DiffIcon, NoDiffIcon, PlusIcon } from '../Icons'
import { errText, Pill, Field, Findings } from './parts'
import { paramTypeText } from './paramSuggest'
import { useUnsaved } from '../../lib/unsaved'

export function ScriptDrawer({
  name,
  linkedVersion,
  me,
  production,
  maxText,
  groups,
  onClose,
  onChanged,
  onDeleted,
  onRun,
  onNewGroup,
  onToast,
}: {
  name: string | null
  /** Opens on this version, as Pending approvals sends a reviewer here. */
  linkedVersion?: number
  me: AutomationIndex['me']
  production: boolean
  maxText: number
  groups: string[]
  onClose: () => void
  onChanged: (name?: string) => void
  onDeleted: () => void
  onRun: (script: string, dryRun: boolean) => void
  /** Opens a new group over the drawer; done gets its name once saved. */
  onNewGroup: (done: (name: string) => void) => void
  onToast: (msg: string) => void
}) {
  const [script, setScript] = useState<Script | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [tab, setTab] = useState(linkedVersion ? 'versions' : 'script')
  const [newName, setNewName] = useState('')
  const [group, setGroup] = useState('')
  const [text, setText] = useState(name === null ? NEW_SCRIPT : '')
  const [check, setCheck] = useState<{
    header: Header
    findings: Finding[] | null
  } | null>(null)
  const [busy, setBusy] = useState(false)
  const [actErr, setActErr] = useState('')
  const [viewing, setViewing] = useState<number | null>(linkedVersion ?? null)
  const [against, setAgainst] = useState<number | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  // What a new script starts from: the empty one, or the template picked.
  const [base, setBase] = useState({ text: NEW_SCRIPT, name: '', group: '' })
  const [confirmRevoke, setConfirmRevoke] = useState<number | null>(null)
  // The version an Approve or Reject asks about first.
  const [deciding, setDeciding] = useState<{
    n: number
    how: 'approve' | 'reject'
  } | null>(null)
  const [saveTried, setSaveTried] = useState(false)
  const [ask, setAsk] = useState('')
  const [asking, setAsking] = useState(false)
  const [askErr, setAskErr] = useState('')
  const [questions, setQuestions] = useState<string[]>([])
  const [said, setSaid] = useState<{
    notes: string[]
    rounds: number
    added: number
    removed: number
  } | null>(null)
  const [jump, setJump] = useState<{ line: number } | null>(null)
  const asked = useRef<AbortController | null>(null)
  useEffect(() => () => asked.current?.abort(), [])
  const write = () => {
    const ctl = new AbortController()
    asked.current = ctl
    setAsking(true)
    setAskErr('')
    setQuestions([])
    setSaid(null)
    const before = text === NEW_SCRIPT ? '' : text
    draftScript(ask, text === NEW_SCRIPT ? '' : text, ctl.signal)
      .then((d) => {
        setText(d.text)
        setAsk('')
        setQuestions(d.questions ?? [])
        const diff = lineDiff(before, d.text)
        setSaid({
          notes: d.notes ?? [],
          rounds: d.rounds,
          added: diff.added,
          removed: diff.removed,
        })
      })
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) setAskErr(errText(e))
      })
      .finally(() => setAsking(false))
  }

  useEffect(() => {
    if (name === null) return
    let gone = false
    fetchScript(name)
      .then((s) => {
        if (gone) return
        setScript(s)
        setGroup(s.group ?? '')
        setText(latestOf(s).text)
      })
      .catch((e: unknown) => !gone && setLoadErr(errText(e)))
    return () => {
      gone = true
    }
  }, [name])

  // The checks run as the text changes, as they will on save (AU-03).
  useEffect(() => {
    if (!text) return
    const t = setTimeout(() => {
      checkScript(text)
        .then(setCheck)
        .catch(() => setCheck(null))
    }, 400)
    return () => clearTimeout(t)
  }, [text])

  const latest = script ? latestOf(script) : undefined
  const approved = script ? approvedOf(script) : undefined
  const changed = name === null || (latest !== undefined && (text !== latest.text || group !== (script?.group ?? '')))
  // Closing with changes not saved asks first. Once saved, a new script
  // compares with what was saved, before the drawer takes its name.
  const unsaved = script ? text !== latestOf(script).text || group !== (script.group ?? '') : name === null && (text !== base.text || newName !== base.name || group !== base.group)
  useUnsaved(unsaved, 'the script drawer')
  // A save under way finishes on the server whether the drawer stays or not.
  const close = () => (unsaved && !busy ? setConfirmClose(true) : onClose())
  const nameBad = name === null && newName !== '' && !SCRIPT_NAME.test(newName)
  const tooLong = new TextEncoder().encode(text).length > maxText
  const errors = (check?.findings ?? []).filter((f) => f.level === 'error').length
  // Save stays there to press: pressing it says what it still needs.
  const saveWhy = tooLong
    ? `The script is over ${maxText / 1024} KiB: shorten it.`
    : name === null && !newName
      ? 'Give the script a name first.'
      : nameBad
        ? 'The name takes lowercase letters, digits and dashes.'
        : !changed
          ? 'Nothing to save yet: change the script first.'
          : ''

  const act = async (f: () => Promise<Script>, done: string) => {
    setBusy(true)
    setActErr('')
    try {
      const s = await f()
      setScript(s)
      setText(latestOf(s).text)
      setGroup(s.group ?? '')
      onToast(done)
      onChanged(s.name)
    } catch (e) {
      setActErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const save = () => (name === null ? act(() => createScript(newName, group, text), `${newName} v1 is saved as a draft.`) : act(() => saveScript(name, group, text), 'Saved.'))

  // What opens is what needs reading: the version in review, else the latest.
  const shownVersion = script?.versions.find((v) => v.n === viewing) ?? script?.versions.find((v) => v.state === 'review') ?? latest
  const pickTemplate = (id: string) => {
    const t = SCRIPT_TEMPLATES.find((x) => x.id === id)
    if (!t) return
    setText(t.text)
    setNewName(t.name)
    setGroup(t.group)
    setBase({ text: t.text, name: t.name, group: t.group })
  }
  // Production runs approved versions only; anywhere else Run takes the
  // newest version, approved or not, and never a rejected one.
  const runnableVersion = script ? [...script.versions].reverse().find((v) => mayRun(v, production)) : undefined

  const groupPick = (
    <>
      <label htmlFor="script-group" style={{ fontSize: 12.5, color: 'var(--fg3)' }}>
        Group
      </label>
      <span className="rk-field" style={{ width: 220, height: 30 }}>
        <select id="script-group" value={group} onChange={(e) => setGroup(e.target.value)} disabled={asking} data-testid="script-group">
          <option value="">Ungrouped</option>
          {[...new Set([...groups, ...(group ? [group] : [])])]
            .sort((a, b) => a.localeCompare(b))
            .map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
        </select>
      </span>
      <button className="rk-btn" onClick={() => onNewGroup(setGroup)} disabled={asking} style={{ height: 30 }} data-testid="script-new-group">
        <PlusIcon />
        New group
      </button>
    </>
  )

  const locked = busy || asking
  const footer = (
    <div
      style={{
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        gap: 8,
      }}
    >
      {actErr && (
        <p className="tb-err" style={{ flex: '1 1 100%', margin: 0 }} data-testid="script-error">
          {actErr}
        </p>
      )}
      {saveTried && saveWhy && (
        <p className="tb-err" style={{ flex: '1 1 100%', margin: 0 }} data-testid="script-save-why">
          {saveWhy}
        </p>
      )}
      <button
        className="rk-btn"
        data-on
        onClick={() => {
          setSaveTried(!!saveWhy)
          if (!saveWhy) void save()
        }}
        disabled={locked}
        data-testid="script-save"
      >
        {name === null || latest?.state === 'draft' ? 'Save draft' : `Save as v${(latest?.n ?? 0) + 1} draft`}
      </button>
      {latest && latest.state === 'draft' && !changed && (
        <button
          className="rk-btn"
          onClick={() => void act(() => submitVersion(script!.name, latest.n), `v${latest.n} is in review.`)}
          disabled={locked || errors > 0 || (latest.findings ?? []).some((f) => f.level === 'error')}
          data-testid="script-submit"
        >
          Submit v{latest.n} for review
        </button>
      )}
      {latest && !changed && canApprove(latest) && (
        <button className="rk-btn" onClick={() => setDeciding({ n: latest.n, how: 'approve' })} disabled={locked} data-testid="script-approve">
          Approve v{latest.n}…
        </button>
      )}
      {latest && !changed && canReject(latest) && (
        <button className="rk-btn" data-danger="true" onClick={() => setDeciding({ n: latest.n, how: 'reject' })} disabled={locked} data-testid="script-reject">
          Reject v{latest.n}…
        </button>
      )}
      {latest && !changed && latest.state === 'review' && latest.may?.involved && (
        <span className="tb-hint" style={{ margin: 0 }} data-testid="script-approve-wait">
          Another SuperAdmin must approve v{latest.n}: you wrote or submitted it.
        </span>
      )}
      {script && (
        <>
          <button
            className="rk-btn"
            style={{ marginLeft: 'auto' }}
            onClick={() => onRun(script.name, true)}
            disabled={locked || changed || errors > 0 || !latest?.header.dryRun}
            title={latest?.header.dryRun ? undefined : 'This script does not declare dry-run: yes, so it has no dry run'}
            data-testid="script-drawer-dry-run"
          >
            Dry run…
          </button>
          <button className="rk-btn" onClick={() => onRun(script.name, false)} disabled={locked || !runnableVersion || (runnableVersion.n === latest?.n && (changed || errors > 0))} data-testid="script-drawer-run">
            Run v{runnableVersion?.n ?? '—'}…
          </button>
          <button className="rk-btn" data-danger="true" onClick={() => setConfirmDelete(true)} disabled={locked} data-testid="script-delete">
            Delete
          </button>
        </>
      )}
    </div>
  )

  return (
    <>
      <Drawer
        label="Script"
        kicker={name === null ? 'New script' : `Script${script?.group ? ` in ${script.group}` : ''}`}
        title={name ?? (newName || 'new-script')}
        copy={name === null ? false : undefined}
        facts={
          script && latest ? (
            <span
              style={{
                display: 'flex',
                gap: 8,
                flexWrap: 'wrap',
                alignItems: 'center',
                fontSize: 12.5,
              }}
            >
              v{latest.n} <Pill tone={versionTone(latest.state)}>{latest.state}</Pill>
              {approved ? `runs v${approved.n} (${approved.approvals?.length ?? 0} ${approved.approvals?.length === 1 ? 'approval' : 'approvals'})` : 'nothing approved to run'}
              <span style={{ color: 'var(--fg3)' }}>updated {fmtTs(script.updated)}</span>
            </span>
          ) : undefined
        }
        tabs={
          name === null
            ? undefined
            : [
                { id: 'script', label: 'Script' },
                {
                  id: 'versions',
                  label: 'Versions',
                  count: script?.versions.length,
                },
              ]
        }
        tab={tab}
        onTab={setTab}
        onClose={close}
        footer={tab === 'script' ? footer : undefined}
        tabsEnd={tab === 'script' ? groupPick : undefined}
        width={1400}
        testId="script-drawer"
      >
        {loadErr && <p className="tb-err">{loadErr}</p>}
        {tab === 'script' && (name === null || script) && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {name === null && (
              <div
                style={{
                  display: 'flex',
                  gap: 12,
                  flexWrap: 'wrap',
                  alignItems: 'flex-start',
                }}
              >
                {name === null && (
                  <Field label="Start from">
                    <span className="rk-field" style={{ width: 320 }}>
                      <select defaultValue="blank" onChange={(e) => pickTemplate(e.target.value)} disabled={asking} data-testid="script-template">
                        {SCRIPT_TEMPLATES.map((t) => (
                          <option key={t.id} value={t.id}>
                            {t.label}
                          </option>
                        ))}
                      </select>
                    </span>
                  </Field>
                )}
                {name === null && (
                  <Field label="Name" hint={nameBad ? 'Lowercase letters, digits and dashes, starting with a letter or digit.' : 'It cannot change later.'}>
                    <span className="rk-field" style={{ width: 260 }}>
                      <input value={newName} onChange={(e) => setNewName(e.target.value.trim())} placeholder="ensure-chrony" aria-invalid={nameBad} disabled={asking} data-testid="script-name" />
                    </span>
                  </Field>
                )}
              </div>
            )}
            <div
              style={{
                display: 'flex',
                flexWrap: 'wrap',
                gap: 16,
                alignItems: 'stretch',
              }}
            >
              <div style={{ flex: '3 1 560px', minWidth: 0 }}>
                <Field label="Script" hint={tooLong ? `Over ${maxText / 1024} KiB: shorten it.` : 'Header lines: # 221b:describe, # 221b:param, # 221b:privilege, # 221b:dry-run, # 221b:facts.'}>
                  <CodeArea value={text} onChange={setText} testId="script-text" height="calc(100vh - 330px)" readOnly={asking} jump={jump} />
                </Field>
              </div>
              <aside
                style={{
                  flex: '1 1 340px',
                  minWidth: 0,
                  maxHeight: 'calc(100vh - 300px)',
                  overflow: 'auto',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 12,
                }}
                aria-label="Sherlock and checks"
              >
                {check && (
                  <div
                    style={{
                      fontSize: 12.5,
                      display: 'grid',
                      gridTemplateColumns: 'max-content 1fr',
                      gap: '4px 12px',
                    }}
                    data-testid="script-header"
                  >
                    <span style={{ color: 'var(--fg3)' }}>Kind</span>
                    <span data-testid="script-drawer-kind">{scriptKind(check.header.target)}</span>
                    <span style={{ color: 'var(--fg3)' }}>Privilege</span>
                    <span className="rk-mono">{privilegeText(check.header.privilege)}</span>
                    <span style={{ color: 'var(--fg3)' }}>Dry run</span>
                    <span>{check.header.dryRun ? 'honoured: DRY_RUN=1 changes nothing' : 'not supported'}</span>
                    <span style={{ color: 'var(--fg3)' }}>Parameters</span>
                    <span>
                      {(check.header.params ?? []).length === 0
                        ? 'none'
                        : (check.header.params ?? []).map((p) => (
                            <span key={p.name} style={{ display: 'block' }}>
                              <span className="rk-mono">{p.name}</span>
                              {/* FU-03: the kind of parameter, as the Run dialog says it. */}
                              {` · ${paramTypeText(p)}`}
                            </span>
                          ))}
                    </span>
                  </div>
                )}
                <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="script-sherlock">
                  <h3 className="rk-h" style={{ margin: 0 }}>
                    Sherlock
                  </h3>
                  <textarea
                    className="rk-area"
                    aria-label="What the script should do"
                    placeholder="Keep chrony running on every node"
                    rows={4}
                    value={ask}
                    onChange={(e) => setAsk(e.target.value)}
                    disabled={asking}
                    style={{ resize: 'vertical', minHeight: 88 }}
                    data-testid="script-sherlock-request"
                  />
                  <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <button className="rk-btn" onClick={write} disabled={asking || (!ask.trim() && !(check?.findings ?? []).length)} data-testid="script-sherlock-draft">
                      {asking ? 'Working…' : !ask.trim() && (check?.findings ?? []).length ? 'Fix with Sherlock' : 'Write with Sherlock'}
                    </button>
                    {asking && (
                      <button className="rk-btn" onClick={() => asked.current?.abort()} data-testid="script-sherlock-cancel">
                        Cancel
                      </button>
                    )}
                  </div>
                  {asking && (
                    <p className="tb-hint" style={{ margin: 0 }} data-testid="script-sherlock-working">
                      Sherlock is writing and fixing what the checks find. The script is locked until it ends.
                    </p>
                  )}
                  {(askErr || said || questions.length > 0) && (
                    <div
                      style={{
                        maxHeight: '15em',
                        overflow: 'auto',
                        lineHeight: 1.5,
                        display: 'flex',
                        flexDirection: 'column',
                        gap: 6,
                      }}
                      tabIndex={0}
                      role="region"
                      aria-label="What Sherlock said"
                      data-testid="script-sherlock-output"
                    >
                      {askErr && (
                        <p className="tb-err" style={{ margin: 0 }} data-testid="script-sherlock-error">
                          {askErr}
                        </p>
                      )}
                      {said && (
                        <div
                          style={{
                            padding: '8px 10px',
                            borderRadius: 8,
                            border: '1px solid var(--line)',
                            background: 'var(--bg)',
                            fontSize: 12.5,
                            display: 'flex',
                            flexDirection: 'column',
                            gap: 6,
                          }}
                          data-testid="script-sherlock-notes"
                        >
                          <div style={{ color: 'var(--fg3)' }}>
                            Sherlock changed the script: {said.added} lines added, {said.removed} removed
                            {said.rounds > 1 ? `, in ${said.rounds} rounds with the checks` : ''}. Nothing is saved until you save it.
                          </div>
                          {said.notes.map((n, i) => (
                            <p key={i} style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                              {n}
                            </p>
                          ))}
                        </div>
                      )}
                      {questions.length > 0 && (
                        <div data-testid="script-sherlock-questions">
                          <p className="tb-hint" style={{ margin: '0 0 4px' }}>
                            Sherlock needs your answer: say it above and ask again.
                          </p>
                          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }}>
                            {questions.map((q) => (
                              <li key={q}>{q}</li>
                            ))}
                          </ul>
                        </div>
                      )}
                    </div>
                  )}
                </section>
                <section>
                  <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                    Checks
                  </h3>
                  {check ? <Findings list={check.findings} onLine={(line) => setJump({ line })} /> : <p className="tb-hint">Checking…</p>}
                </section>
              </aside>
            </div>
          </div>
        )}
        {tab === 'versions' && script && shownVersion && (
          <div
            style={{
              display: 'flex',
              flexWrap: 'wrap',
              gap: 16,
              alignItems: 'flex-start',
            }}
          >
            <div
              style={{
                flex: '1 1 340px',
                maxWidth: 480,
                minWidth: 0,
                display: 'flex',
                flexDirection: 'column',
                gap: 8,
              }}
            >
              <table className="tb-table" data-testid="script-versions">
                <thead>
                  <tr>
                    <th>Version</th>
                    <th>Author</th>
                    <th>Approvals</th>
                    <th aria-label="Diff" />
                  </tr>
                </thead>
                <tbody>
                  {[...script.versions].reverse().map((v) => (
                    <VersionRow
                      key={v.n}
                      v={v}
                      me={me}
                      busy={busy}
                      shown={shownVersion.n}
                      latest={latest?.n ?? v.n}
                      against={against}
                      onView={() => {
                        setViewing(v.n)
                        setAgainst(null)
                      }}
                      onDiff={() => {
                        setViewing(latest?.n ?? v.n)
                        setAgainst(v.n)
                      }}
                      onRevoke={() => setConfirmRevoke(v.n)}
                      onApprove={() => setDeciding({ n: v.n, how: 'approve' })}
                      onReject={() => setDeciding({ n: v.n, how: 'reject' })}
                    />
                  ))}
                </tbody>
              </table>
              {actErr && <p className="tb-err">{actErr}</p>}
            </div>
            <VersionPane key={shownVersion.n} script={script} v={shownVersion} against={against} onAgainst={setAgainst} onReviewed={(s) => setScript(s)} />
          </div>
        )}
      </Drawer>
      {confirmRevoke !== null && script && (
        <ConfirmDialog
          title={`Revoke the approval of v${confirmRevoke}?`}
          confirm="Revoke approval"
          danger
          busy={busy}
          error={actErr}
          onCancel={() => setConfirmRevoke(null)}
          onConfirm={() => void act(() => revokeVersion(script.name, confirmRevoke), `You revoked the approval of v${confirmRevoke}.`).then(() => setConfirmRevoke(null))}
        >
          <p style={{ margin: 0 }}>It goes back to review. Production stops running it until someone approves it again; runs already going on carry on. The audit log records who revoked it.</p>
        </ConfirmDialog>
      )}
      {deciding && script && (
        <DecideDialog
          how={deciding.how}
          what={`${script.name} v${deciding.n}`}
          busy={busy}
          error={actErr}
          onCancel={() => {
            setDeciding(null)
            setActErr('')
          }}
          onConfirm={(comment) =>
            void act(
              () => (deciding.how === 'approve' ? approveVersion(script.name, deciding.n) : rejectVersion(script.name, deciding.n, comment)),
              deciding.how === 'approve' ? `You approved v${deciding.n}.` : `You rejected v${deciding.n}.`,
            ).then(() => setDeciding(null))
          }
        >
          {deciding.how === 'approve'
            ? 'Once approved, it can run on production, and your approval is recorded in the audit log.'
            : 'It stays rejected and never runs; the author saves the script again for a new version, and reads your reason.'}
        </DecideDialog>
      )}
      {confirmClose && (
        <ConfirmDialog
          title="Close without saving?"
          confirm="Close"
          danger
          onCancel={() => setConfirmClose(false)}
          onConfirm={() => {
            setConfirmClose(false)
            onClose()
          }}
        >
          <p style={{ margin: 0 }} data-testid="script-close-unsaved">
            {name === null ? 'This script is not saved. Closing loses it.' : 'Your changes to the script are not saved. Closing loses them.'}
          </p>
        </ConfirmDialog>
      )}
      {confirmDelete && script && (
        <ConfirmDialog
          title={`Delete ${script.name}?`}
          confirm="Delete"
          danger
          busy={busy}
          error={actErr}
          onCancel={() => setConfirmDelete(false)}
          onConfirm={() => {
            setBusy(true)
            deleteScript(script.name)
              .then(onDeleted)
              .catch((e: unknown) => setActErr(errText(e)))
              .finally(() => setBusy(false))
          }}
        >
          <p style={{ margin: 0 }}>Every version of it goes, approved ones too. The runs it made stay in Run history and in the audit log.</p>
        </ConfirmDialog>
      )}
    </>
  )
}

export function VersionRow({
  v,
  busy,
  shown,
  latest,
  against,
  onView,
  onDiff,
  onRevoke,
  onApprove,
  onReject,
}: {
  v: Version
  me: AutomationIndex['me']
  busy: boolean
  /** The version the page shows. */
  shown: number
  /** The latest version, which each row's diff compares with. */
  latest: number
  against: number | null
  onView: () => void
  onDiff: () => void
  onRevoke: () => void
  onApprove: () => void
  onReject: () => void
}) {
  const viewing = shown === v.n
  const diffed = shown === latest && against === v.n
  return (
    <tr data-testid="version-row" data-n={v.n} style={{ background: viewing ? 'var(--accent-weak)' : undefined }}>
      <td>
        <button className="rk-link" onClick={onView} aria-pressed={viewing} data-testid="version-show">
          v{v.n}
        </button>
        <div>
          <Pill tone={versionTone(v.state)}>{v.state}</Pill>
        </div>
      </td>
      <td>
        {v.author.name}
        <div style={{ color: 'var(--fg3)', fontSize: 12 }}>{fmtTs(v.created)}</div>
      </td>
      <td>
        {(v.approvals ?? []).length === 0 ? (
          <span style={{ color: 'var(--fg3)' }}>none</span>
        ) : (
          (v.approvals ?? []).map((a, i) => (
            <div key={i}>
              {a.by.name} <span style={{ color: 'var(--fg3)', fontSize: 12 }}>{fmtTs(a.at)}</span>
            </div>
          ))
        )}
        {(canApprove(v) || canReject(v) || v.state === 'approved') && (
          <div style={{ display: 'flex', gap: 4, marginTop: 2 }}>
            {canApprove(v) && (
              <button className="rk-btn" data-size="sm" onClick={onApprove} disabled={busy} data-testid="version-approve">
                Approve…
              </button>
            )}
            {canReject(v) && (
              <button className="rk-btn" data-size="sm" data-danger="true" onClick={onReject} disabled={busy} data-testid="version-reject">
                Reject…
              </button>
            )}
            {v.state === 'approved' && (
              <button className="rk-btn" data-size="sm" data-danger="true" onClick={onRevoke} disabled={busy} data-testid="version-revoke">
                Revoke
              </button>
            )}
          </div>
        )}
        {v.sameAs && (
          <div style={{ color: 'var(--fg3)', fontSize: 12 }} data-testid="version-same-as">
            the same text as v{v.sameAs}, which was rejected
          </div>
        )}
        {v.rejected && (
          <div style={{ color: 'var(--fg3)', fontSize: 12 }} data-testid="version-rejected">
            rejected by {v.rejected.by.name} {fmtTs(v.rejected.at)}
            {v.rejected.comment ? `: ${v.rejected.comment}` : ''}
          </div>
        )}
        {(v.revoked ?? []).map((r, i) => (
          <div key={i} style={{ color: 'var(--fg3)', fontSize: 12 }}>
            revoked by {r.by.name} {fmtTs(r.at)}
          </div>
        ))}
      </td>
      <td>
        <span style={{ display: 'inline-flex', gap: 4 }}>
          <button className="rk-icon-btn" aria-label={`No diff: show v${v.n} alone`} title={`No diff: show v${v.n} alone`} onClick={onView} data-testid="version-no-diff">
            <NoDiffIcon />
          </button>
          {v.n !== latest && (
            <button className="rk-icon-btn" aria-label={`Diff v${latest} against v${v.n}`} aria-pressed={diffed} title={`Diff v${latest} against v${v.n}`} onClick={onDiff} data-on={diffed} data-testid="version-diff">
              <DiffIcon />
            </button>
          )}
        </span>
      </td>
    </tr>
  )
}

/** A version's text, or what changed since another version. */
export function VersionPane({ script, v, against, onAgainst, onReviewed }: { script: Script; v: Version; against: number | null; onAgainst: (n: number | null) => void; onReviewed: (s: Script) => void }) {
  const [reviewing, setReviewing] = useState(false)
  const [reviewErr, setReviewErr] = useState('')
  const review = () => {
    setReviewing(true)
    setReviewErr('')
    reviewVersion(script.name, v.n)
      .then(onReviewed)
      .catch((e: unknown) => setReviewErr(errText(e)))
      .finally(() => setReviewing(false))
  }
  const other = script.versions.find((x) => x.n === against)
  const others = script.versions.filter((x) => x.n !== v.n)
  const diff = other ? lineDiff(other.text, v.text) : null
  const paneStyle = {
    margin: 0,
    padding: 12,
    borderRadius: 8,
    fontSize: 12,
    lineHeight: 1.5,
    overflow: 'auto',
    maxHeight: 'calc(100vh - 250px)',
  } as const
  return (
    <div
      style={{
        flex: '3 1 520px',
        minWidth: 0,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      }}
    >
      <div
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'center',
          gap: 8,
        }}
      >
        <h3 className="rk-h" style={{ margin: 0 }}>
          v{v.n}
        </h3>
        <Pill tone={versionTone(v.state)}>{v.state}</Pill>
        <span className="rk-mono" style={{ fontSize: 12 }} title={v.hash}>
          sha256 {v.hash.slice(0, 12)}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        {others.length > 0 && (
          <>
            <button className="rk-icon-btn" aria-label="No diff" title="No diff" onClick={() => onAgainst(null)} disabled={against === null} data-testid="version-no-diff-pane">
              <NoDiffIcon />
            </button>
            <span className="rk-field" style={{ width: 170 }}>
              <select aria-label="Diff against" value={against ?? ''} onChange={(e) => onAgainst(e.target.value ? Number(e.target.value) : null)} data-testid="version-against">
                <option value="">No diff: the script</option>
                {[...others].reverse().map((x) => (
                  <option key={x.n} value={x.n}>
                    Diff against v{x.n}
                  </option>
                ))}
              </select>
            </span>
          </>
        )}
      </div>
      <section
        style={{
          padding: '10px 12px',
          borderRadius: 8,
          border: '1px solid var(--line)',
          background: 'var(--surface)',
          fontSize: 12.5,
          display: 'flex',
          flexDirection: 'column',
          gap: 6,
        }}
        data-testid="version-review"
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <h4 className="rk-h" style={{ margin: 0 }}>
            Sherlock review
          </h4>
          <span style={{ color: 'var(--fg3)' }}>advice: it never approves</span>
          <span style={{ flex: '1 1 auto' }} />
          <button className="rk-btn" onClick={review} disabled={reviewing} data-testid="version-review-ask">
            {reviewing ? 'Reading…' : v.review ? 'Review again' : 'Ask Sherlock'}
          </button>
        </div>
        {reviewErr && (
          <p className="tb-err" style={{ margin: 0 }}>
            {reviewErr}
          </p>
        )}
        {v.review && <p style={{ margin: 0 }}>{v.review.summary}</p>}
        {(v.review?.findings ?? []).map((f, i) => (
          <p key={i} style={{ margin: 0 }}>
            <Pill tone={f.level === 'error' ? 'err' : 'warn'}>{f.level}</Pill> {f.line ? `line ${f.line}: ` : ''}
            {f.msg}
          </p>
        ))}
        {v.header.target === 'kubernetes' && (
          <p style={{ margin: 0 }} data-testid="version-watson">
            What Watson will run: in Kubernetes, only {(v.header.k8s ?? []).map((a) => `${a.verb} ${a.resource}`).join(', ') || 'nothing'}
            {v.header.runsAs ? `, as ${v.header.runsAs}` : ', as the person'}. The proxy refuses anything else, and each command is audited as “Watson via {script.name} v{v.n}, run #…”.
          </p>
        )}
      </section>
      {diff ? (
        <>
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }} data-testid="version-diff-summary">
            {diff.text ? `v${other?.n} → v${v.n}: ${diff.added} added, ${diff.removed} removed` : `v${other?.n} and v${v.n} are the same.`}
          </p>
          {diff.text && (
            <pre className="rk-code rk-mono" style={paneStyle} data-testid="version-diff-text">
              <DiffLines text={diff.text} />
            </pre>
          )}
        </>
      ) : (
        <CodeView text={v.text} testId="version-text" maxHeight={paneStyle.maxHeight} />
      )}
    </div>
  )
}
