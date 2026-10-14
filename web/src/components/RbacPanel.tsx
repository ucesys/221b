import { useState } from 'react'
import type { BindingRow, RoleRow, SARow } from '../api/types'
import { fmtAge, fmtTs, nWord } from '../lib/format'
import { purpose, roleStatus, ruleText, SEV_TEXT, SEV_TONE, sourceText, subjectKey, subjectText } from '../lib/rbac'
import { href } from '../lib/router'
import { Card, Drawer, Facts } from './Drawer'
import { WarnIcon } from './Icons'
import { YamlView } from './YamlView'

interface Props {
  /** "binding:…" or "role:…", as the access view keys them. */
  keyOf: string
  roles: RoleRow[]
  bindings: BindingRow[]
  sas: SARow[]
  now: number
  onClose: () => void
  onOpen: (key: string) => void
  onToast: (msg: string) => void
}

function roleKeyOf(b: BindingRow): string {
  return b.roleKind === 'ClusterRole' ? `role:ClusterRole/${b.roleName}` : `role:Role/${b.namespace}/${b.roleName}`
}

function Item({ main, sub, action, testId }: { main: string; sub?: string; action?: React.ReactNode; testId?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '9px 0', borderTop: '1px solid var(--line)' }} data-testid={testId}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0, flex: '1 1 auto' }}>
        <span className="rk-mono" style={{ fontSize: 12.5, overflowWrap: 'anywhere' }}>
          {main}
        </span>
        {sub && (
          <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg3)', whiteSpace: 'pre-line', overflowWrap: 'anywhere' }}>
            {sub}
          </span>
        )}
      </div>
      {action}
    </div>
  )
}

/** The drawer of a role or a binding (P4-03): what it grants, to whom, and its YAML. */
export function RbacPanel({ keyOf, roles, bindings, now, onClose, onOpen, onToast }: Props) {
  const [tab, setTab] = useState<'overview' | 'yaml'>('overview')
  const binding = bindings.find((b) => b.key === keyOf)
  const role = roles.find((r) => r.key === keyOf)
  const kind = binding?.kind ?? role?.kind ?? (keyOf.startsWith('binding:') ? 'Binding' : 'Role')
  const ns = binding?.namespace ?? role?.namespace
  const name = binding?.name ?? role?.name ?? keyOf.split('/').pop() ?? keyOf
  const yamlKind = { ClusterRoleBinding: 'clusterrolebindings', RoleBinding: 'rolebindings', ClusterRole: 'clusterroles', Role: 'roles' }[kind as string] ?? ''
  const worst = binding?.findings.find((f) => f.severity !== 'info')
  const roleOfBinding = binding ? roles.find((r) => r.key === roleKeyOf(binding)) : undefined
  const status = role ? roleStatus(role.powers) : null
  const boundBy = role ? bindings.filter((b) => roleKeyOf(b) === role.key) : []

  const copyName = () =>
    void navigator.clipboard?.writeText(name).then(
      () => onToast('Copied the name'),
      () => onToast('Could not copy: the clipboard is not available.'),
    )

  return (
    <Drawer
      label={`${kind} details`}
      testId="rbac-panel"
      kicker={
        <>
          <span className="rk-tag" style={{ fontSize: 11, padding: '1px 7px' }}>
            {kind}
          </span>
          {ns ? (
            <a className="rk-link rk-mono" href={href('namespaces', [ns])} style={{ fontSize: 11.5 }}>
              {ns}
            </a>
          ) : (
            <span>cluster-scoped</span>
          )}
        </>
      }
      title={name}
      facts={
        <>
          {worst && (
            <span className="rk-pill" data-tone={SEV_TONE[worst.severity]} data-testid="rbac-status">
              <span className="rk-dot" />
              {SEV_TEXT[worst.severity]} risk
            </span>
          )}
          {status && (
            <span className="rk-pill" data-tone={status.tone} data-testid="rbac-status">
              <span className="rk-dot" />
              {status.text}
            </span>
          )}
          {binding && (
            <>
              <span>
                Role <span className="rk-mono" style={{ color: 'var(--fg)' }}>{binding.roleName}</span>
              </span>
              <span>
                Subjects <span className="rk-num" style={{ color: 'var(--fg)' }}>{binding.subjects.length}</span>
              </span>
            </>
          )}
          {role && (
            <>
              <span>
                Rules <span className="rk-num" style={{ color: 'var(--fg)' }}>{role.rules.length}</span>
              </span>
              <span>
                Bindings <span className="rk-num" style={{ color: 'var(--fg)' }}>{boundBy.length}</span>
              </span>
            </>
          )}
          {(binding?.created ?? role?.created) && (
            <span>
              Age <span style={{ color: 'var(--fg)' }}>{fmtAge(binding?.created ?? role?.created, now)}</span>
            </span>
          )}
          <button className="rk-btn" onClick={copyName} style={{ height: 26, marginLeft: 'auto' }}>
            Copy name
          </button>
        </>
      }
      tabs={[
        { id: 'overview', label: 'Overview' },
        { id: 'yaml', label: 'YAML' },
      ]}
      tab={tab}
      onTab={(t) => setTab(t as 'overview' | 'yaml')}
      onClose={onClose}
      notice={
        !binding &&
        !role && (
          <div className="rk-callout" data-tone="warn" style={{ margin: '12px 20px 0' }}>
            <WarnIcon />
            <span style={{ fontSize: 12.5 }}>This {kind} isn’t in the cluster any more, or isn’t yours to see.</span>
          </div>
        )
      }
    >
      {tab === 'yaml' && yamlKind && <YamlView kind={yamlKind} namespace={ns} name={name} onToast={onToast} />}
      {tab === 'overview' && binding && (
        <>
          {binding.findings.map((f, i) => (
            <div key={i} className="rk-callout" data-tone={f.severity === 'high' ? 'err' : f.severity === 'medium' ? 'warn' : 'info'} data-testid="rbac-finding">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{f.title}</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{f.text}</span>
              </div>
            </div>
          ))}
          {binding.roleMissing && (
            <div className="rk-callout" data-tone="warn">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>
                  {binding.roleKind} {binding.roleName} doesn’t exist
                </strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>The binding grants nothing until a role with that name exists.</span>
              </div>
            </div>
          )}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(380px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
              <Card title="Binding">
                <Facts
                  items={[
                    ['Role', `${binding.roleKind} ${binding.roleName}`],
                    ['Scope', binding.namespace ? `namespace ${binding.namespace}` : 'every namespace, and cluster-scoped objects such as nodes'],
                    ['Source', sourceText(binding.source)],
                    ['Created', binding.created ? `${fmtTs(binding.created)} · ${fmtAge(binding.created, now)} ago` : '—'],
                  ]}
                />
              </Card>
              <Card title="Role">
                <Item
                  main={`${binding.roleKind} ${binding.roleName}`}
                  sub={roleOfBinding ? [purpose(roleOfBinding), `${nWord(roleOfBinding.rules.length, 'rule', 'rules')}${roleOfBinding.powers.length ? ' · ' + roleOfBinding.powers.join(', ') : ''}`].filter(Boolean).join('\n') : 'not in the cluster'}
                  action={
                    roleOfBinding && (
                      <button className="rk-link" onClick={() => onOpen(roleOfBinding.key)} data-testid="rbac-open-role">
                        Open role
                      </button>
                    )
                  }
                />
              </Card>
            </div>
            <Card title="Subjects">
              {binding.subjects.map((s) => (
                <Item
                  key={subjectKey(s)}
                  main={subjectText(s)}
                  sub={s.kind === 'Group' ? 'members come from the identity provider' : s.kind === 'User' ? 'a person or a client certificate' : s.missing ? 'this ServiceAccount doesn’t exist' : `ServiceAccount in ${s.namespace}`}
                  action={
                    <a className="rk-link" href={href('access', [], { id: subjectKey(s) })} style={{ fontSize: 12.5, textDecoration: 'none', whiteSpace: 'nowrap' }}>
                      Open in Access
                    </a>
                  }
                  testId="rbac-subject"
                />
              ))}
              {!binding.subjects.length && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The binding names no subject.</p>}
            </Card>
          </div>
        </>
      )}
      {tab === 'overview' && role && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(380px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <Card title={role.kind}>
              <Facts
                items={[
                  ['Purpose', purpose(role) || '—'],
                  ['Notable', role.powers.join(', ') || 'nothing broad'],
                  ...(role.aggregated ? ([['Aggregated', 'its rules are gathered from ClusterRoles labelled to join it']] as [string, string][]) : []),
                  ['Source', sourceText(role.source)],
                  ['Created', role.created ? `${fmtTs(role.created)} · ${fmtAge(role.created, now)} ago` : '—'],
                ]}
              />
            </Card>
            <Card title="Bound by">
              {boundBy.map((b) => (
                <Item
                  key={b.key}
                  main={`${b.kind} ${b.namespace ? b.namespace + '/' : ''}${b.name}`}
                  sub={b.subjects.map(subjectText).join(', ') + (b.namespace ? `\nin ${b.namespace}` : '\nin every namespace')}
                  action={
                    <button className="rk-link" onClick={() => onOpen(b.key)}>
                      Open
                    </button>
                  }
                />
              ))}
              {!boundBy.length && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No binding uses this role.</p>}
            </Card>
          </div>
          <Card title="Rules">
            {role.rules.map((r, i) => {
              const t = ruleText(r)
              return <Item key={i} main={t.main} sub={t.sub} testId="rbac-rule" />
            })}
            {!role.rules.length && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No rules: it grants nothing.</p>}
          </Card>
        </div>
      )}
    </Drawer>
  )
}
