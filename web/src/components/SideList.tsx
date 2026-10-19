import type { ReactNode } from 'react'
import { CopyName } from './CopyName'
import { SearchIcon } from './Icons'

export interface SideItem {
  key: string
  href: string
  current: boolean
  /** The first line: name, and whatever sits to its right. */
  name: string
  /** What the name is, for a copy icon beside it: "namespace name". */
  copy?: string
  right?: ReactNode
  /** The lines under the name. */
  sub?: ReactNode
  sub2?: ReactNode
}

export interface SideGroup {
  title: string
  count?: number | string
  items: SideItem[]
}

interface Props {
  label: string
  title: string
  /** Beside the title: segments, for instance. */
  aside?: ReactNode
  query: string
  onQuery: (q: string) => void
  placeholder: string
  /** Under the search field: a checkbox, for instance. */
  extra?: ReactNode
  groups: SideGroup[]
  empty: string
  foot?: ReactNode
  testId?: string
}

/**
 * The left pane the Namespaces, Services and Helm tabs share: a heading,
 * a filter field and grouped rows that link to what they name, as in the
 * prototype.
 */
export function SideList({ label, title, aside, query, onQuery, placeholder, extra, groups, empty, foot, testId }: Props) {
  const none = groups.every((g) => g.items.length === 0)
  return (
    <aside className="rk-pane" aria-label={label} data-testid={testId} style={{ flex: '1 1 320px', maxWidth: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)' }}>
      <div style={{ padding: '14px 16px 10px', display: 'flex', flexDirection: 'column', gap: 10, borderBottom: '1px solid var(--line)' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap' }}>
          <h2 className="rk-h">{title}</h2>
          {aside}
        </div>
        <label className="rk-field">
          <SearchIcon />
          <span className="rk-sr">{placeholder}</span>
          <input type="search" placeholder={placeholder} value={query} onChange={(e) => onQuery(e.target.value)} />
        </label>
        {extra}
      </div>
      <div style={{ flex: '1 1 auto' }}>
        {groups.map(
          (g) =>
            g.items.length > 0 && (
              <div key={g.title}>
                {g.title && (
                  <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, padding: '14px 16px 6px' }}>
                    <h3 className="rk-h">{g.title}</h3>
                    <span style={{ fontSize: 11, color: 'var(--fg3)' }}>{g.count ?? g.items.length}</span>
                  </div>
                )}
                {g.items.map((it) => (
                  // The name is the link, stretched over the whole row, so a
                  // copy icon can sit beside it outside the link.
                  <div key={it.key} className="rk-rrow rk-rrow-link" data-sel={it.current} style={{ position: 'relative', display: 'flex', flexDirection: 'column', gap: 3, width: '100%', padding: '9px 16px', borderTop: '1px solid var(--line)', textAlign: 'left', boxSizing: 'border-box' }}>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 8, width: '100%', minWidth: 0 }}>
                      <span className="rk-namecopy">
                        <a className="rk-mono rk-rname" href={it.href} aria-current={it.current ? 'page' : undefined} style={{ fontSize: 12.5, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0, color: 'inherit', textDecoration: 'none' }}>
                          {it.name}
                        </a>
                        {it.copy && <CopyName text={it.name} what={it.copy} />}
                      </span>
                      <span style={{ flex: '1 1 auto' }} />
                      {it.right}
                    </span>
                    {it.sub && <span style={{ fontSize: 11.5, color: 'var(--fg3)', display: 'flex', alignItems: 'center', gap: 10, width: '100%', minWidth: 0 }}>{it.sub}</span>}
                    {it.sub2 && (
                      <span className="rk-mono" style={{ maxWidth: '100%', fontSize: 11, color: 'var(--fg3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {it.sub2}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            ),
        )}
        {none && <p style={{ margin: 0, padding: '24px 16px', fontSize: 12.5, color: 'var(--fg3)' }}>{empty}</p>}
      </div>
      {foot && <p style={{ margin: 'auto 0 0', padding: '10px 16px 14px', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>{foot}</p>}
    </aside>
  )
}
