import type { Event } from '../api/types'
import { Tone } from '../columns/cells'
import { fmtAge, fmtTs } from '../lib/format'
import { ScrollBox, Table, TR } from './Table'

/** The events about one object, as every details drawer lists them. */
export function EventsTable({ events, now, label = 'Events' }: { events: Event[]; now: number; label?: string }) {
  if (!events.length) return <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No events in the last hour. Kubernetes keeps events for about an hour.</p>
  return (
    <ScrollBox label={label} className="rk-card">
      <Table label={label} template="82px 150px minmax(260px, 2fr) 60px 110px 110px" minWidth={772} cols={[{ label: 'Type' }, { label: 'Reason' }, { label: 'Message' }, { label: 'Count', right: true }, { label: 'Last seen' }, { label: 'From' }]}>
        {events.map((e, i) => (
          <TR key={i} top style={{ minHeight: 34 }}>
            <span>
              <Tone tone={e.type === 'Warning' ? 'warn' : 'muted'} text={e.type} />
            </span>
            <span className="rk-mono" style={{ fontSize: 12 }}>
              {e.reason}
            </span>
            <span className="rk-wrap" style={{ fontSize: 12.5 }}>
              {e.message}
            </span>
            <span className="rk-r rk-num">{e.count}</span>
            <span className="rk-num" title={e.last ? `${fmtTs(e.last)}; first ${e.first ? fmtTs(e.first) : '—'}` : undefined} style={{ color: 'var(--fg2)' }}>
              {e.last ? `${fmtAge(e.last, now)} ago` : '—'}
            </span>
            <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg3)' }}>
              {e.source}
            </span>
          </TR>
        ))}
      </Table>
    </ScrollBox>
  )
}
