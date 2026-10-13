import { memo } from 'react'
import type { NodeRow } from '../api/types'
import { clampPct, fmtAge, fmtTs } from '../lib/format'
import { cpuMeter, memoryMeter, problemsCell, taintId, taintShort, type Column, type ColumnId, type Meter, type SortDir } from '../lib/nodes'

interface Props {
  rows: NodeRow[]
  cols: Column[]
  template: string
  minWidth: number
  sortCol: ColumnId
  sortDir: SortDir
  onSort: (c: ColumnId) => void
  selected: string | null
  onOpen: (name: string) => void
  rowRef: (name: string, el: HTMLButtonElement | null) => void
  now: number
}

function MeterCell({ m }: { m: Meter }) {
  return (
    <span className="rk-meter" title={m.tip}>
      <span className="rk-bar" style={{ width: 56 }}>
        <span className="rk-req" style={{ width: `${clampPct(m.reqPct).toFixed(1)}%` }} />
        <span className="rk-use" style={{ width: `${clampPct(m.usePct).toFixed(1)}%` }} />
      </span>
      <span className="rk-num" style={{ minWidth: 30, textAlign: 'right' }}>
        {m.text}
      </span>
      <span className="rk-num" style={{ color: 'var(--fg3)' }}>
        {m.sub}
      </span>
    </span>
  )
}

function Cell({ r, id, now }: { r: NodeRow; id: ColumnId; now: number }) {
  switch (id) {
    case 'name':
      return (
        <span>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, maxWidth: '100%' }}>
            <span className="rk-mono rk-pname" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {r.name}
            </span>
            <span className="rk-role" data-role={r.role}>
              {r.role}
            </span>
          </span>
        </span>
      )
    case 'status':
      return (
        <span title={r.statusTip}>
          <span className="rk-t" data-tone={r.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
            <span className="rk-dot" />
            {r.status}
          </span>
        </span>
      )
    case 'problems': {
      const p = problemsCell(r)
      return (
        <span title={p.tip}>
          <span className="rk-t" data-tone={p.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
            <span className="rk-dot" />
            {p.text}
          </span>
        </span>
      )
    }
    case 'cpu':
      return (
        <span>
          <MeterCell m={cpuMeter(r)} />
        </span>
      )
    case 'memory':
      return (
        <span>
          <MeterCell m={memoryMeter(r)} />
        </span>
      )
    case 'pods':
      return (
        <span className="rk-r rk-num" title={`${r.pods.running} pods use slots on this node, of ${r.pods.capacity}`}>
          {r.pods.running} / {r.pods.capacity}
        </span>
      )
    case 'taints':
      return (
        <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg2)' }} title={r.taints.map(taintId).join('\n') || 'No taints'}>
          {r.taints.length ? r.taints.map(taintShort).join(', ') : '—'}
        </span>
      )
    case 'zone':
      return (
        <span className="rk-mono" style={{ color: 'var(--fg2)' }} title={r.zone || 'No zone label'}>
          {r.zone || '—'}
        </span>
      )
    case 'kubelet':
      return (
        <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)' }}>
          {r.kubelet}
        </span>
      )
    case 'age':
      return (
        <span className="rk-num" style={{ color: 'var(--fg2)' }} title={`Joined ${fmtTs(r.created)}`}>
          {fmtAge(r.created, now)}
        </span>
      )
  }
}

function label(r: NodeRow, open: boolean): string {
  const p = problemsCell(r)
  return `${r.name}, ${r.role}, ${r.status}${p.text !== '—' ? ', ' + p.text : ''}${open ? ' — details open, press to close' : ' — show details'}`
}

const Row = memo(function Row({
  r,
  cols,
  selected,
  onOpen,
  rowRef,
  now,
}: {
  r: NodeRow
  cols: Column[]
  selected: boolean
  onOpen: (name: string) => void
  rowRef: (name: string, el: HTMLButtonElement | null) => void
  now: number
}) {
  return (
    <button
      ref={(el) => rowRef(r.name, el)}
      className="rk-gr"
      data-sel={selected}
      data-node={r.name}
      aria-label={label(r, selected)}
      aria-pressed={selected}
      onClick={() => onOpen(r.name)}
    >
      {cols.map((c) => (
        <Cell key={c.id} r={r} id={c.id} now={now} />
      ))}
    </button>
  )
})

export function NodeTable({ rows, cols, template, minWidth, sortCol, sortDir, onSort, selected, onOpen, rowRef, now }: Props) {
  return (
    <div className="rk-card" style={{ overflowX: 'auto' }}>
      <div className="rk-grid" style={{ ['--cols' as string]: template, minWidth }} role="group" aria-label="Nodes">
        <div className="rk-gh">
          {cols.map((c) => {
            const on = c.id === sortCol
            return (
              <button
                key={c.id}
                className={c.right ? 'rk-th rk-r' : 'rk-th'}
                data-active={on}
                data-col={c.id}
                title={(on ? 'Sorted by ' : 'Sort by ') + c.sortName}
                aria-label={`${c.label}: ${on ? `sorted ${sortDir === 'asc' ? 'ascending' : 'descending'}` : 'sort'}`}
                onClick={() => onSort(c.id)}
              >
                {c.label}
                <span className="rk-arrow" aria-hidden="true">
                  {on ? (sortDir === 'asc' ? '↑' : '↓') : ''}
                </span>
              </button>
            )
          })}
        </div>
        {rows.map((r) => (
          <Row key={r.name} r={r} cols={cols} selected={selected === r.name} onOpen={onOpen} rowRef={rowRef} now={now} />
        ))}
        {rows.length === 0 && <p style={{ margin: 0, padding: '28px 16px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg3)' }}>No nodes match this filter.</p>}
      </div>
    </div>
  )
}
