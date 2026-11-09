// One page layout for every sub-tab of Cases › Mycroft (CF-55): the intro
// line on top; then one toolbar row with the filter on the left, a search
// box (and the rules' job select) where the page has one, a spacer, and
// the page's main action on the right; then the content. The page fills
// the Mycroft column whatever it holds, and its table is the same card
// and table in every sub-tab, an empty list still showing the header
// with one line saying there is nothing yet.
import type { JSX, ReactNode } from 'react'
import { MYCROFT_TABLE } from './mycroftTable'

interface PageProps {
  /** The intro line. */
  lead: ReactNode
  /** The filter segment, first in the toolbar. */
  filter: ReactNode
  /** A search box or a select, after the filter. */
  tools?: ReactNode
  /** The page's main action, last in the toolbar. */
  action?: ReactNode
  testId: string
  children?: ReactNode
}

/** A Mycroft sub-tab: intro, one toolbar row, then the content. */
export function MycroftPage({ lead, filter, tools, action, testId, children }: PageProps): JSX.Element {
  return (
    <div className="cs-col" data-testid={testId}>
      <p className="cs-lead">{lead}</p>
      <div className="cs-bar" data-testid="mycroft-toolbar">
        {filter}
        {tools}
        <span className="cs-grow" />
        {action}
      </div>
      {children}
    </div>
  )
}

/** A Mycroft table: the card that scrolls sideways when narrower, and the table as wide as the others. */
export function MycroftTable({ label, testId, children }: { label: string; testId: string; children: ReactNode }): JSX.Element {
  return (
    <section className="rk-card" style={{ overflowX: 'auto' }} aria-label={label}>
      <table className="tb-table" style={MYCROFT_TABLE} data-testid={testId}>
        {children}
      </table>
    </section>
  )
}

/** The one row of an empty table, saying there is nothing yet. */
export function EmptyRow({ cols, testId, children }: { cols: number; testId?: string; children: ReactNode }): JSX.Element {
  return (
    <tr>
      <td colSpan={cols} style={{ color: 'var(--fg3)' }} data-testid={testId}>
        {children}
      </td>
    </tr>
  )
}
