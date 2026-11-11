// A link into the report page (CS-15 to CS-19): a new tab at the address
// the issue service's settings give, shown only to people who may report.
// Text for the box is made and handed over as the link is followed, never
// before, and never in the address's query (lib/reportLink.ts).
import type { CSSProperties, ReactNode } from 'react'
import { mayReport, useIssueMe } from '../../lib/cases'
import { reportHref } from '../../lib/reportLink'

interface Props {
  /** The text for the report page's box, made when the link is followed. */
  text?: () => string
  /** Files for someone else (an operator's On behalf of). */
  forUser?: string
  /** The person's own reports alone. */
  mine?: boolean
  className?: string
  style?: CSSProperties
  role?: string
  testId?: string
  title?: string
  onOpened?: () => void
  children: ReactNode
}

export function ReportPageLink({ text, forUser, mine, className = 'rk-btn', style, role, testId, title, onOpened, children }: Props) {
  const me = useIssueMe()
  if (!mayReport(me)) return null
  const base = me?.reportURL
  return (
    <a
      className={className}
      style={style}
      role={role}
      href={reportHref(base, { forUser, mine })}
      target="_blank"
      rel="noopener"
      title={title ?? 'Opens the report page in a new tab'}
      data-testid={testId}
      onClick={(e) => {
        // The text is handed over now, as the tab opens.
        if (text) e.currentTarget.href = reportHref(base, { text: text(), forUser, mine })
        onOpened?.()
      }}
    >
      {children}
    </a>
  )
}
