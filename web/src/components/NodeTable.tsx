import { memo, useEffect, useState } from 'react'
import type { NodeRow } from '../api/types'
import { clampPct, fCores, fMem, fmtAge, fmtTs } from '../lib/format'
import { cpuMeter, free, memoryMeter, ownLabels, problemsCell, roleTag, taintId, taintShort, type Column, type ColumnId, type Meter, type SortDir } from '../lib/nodes'

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

/** A free amount, in warning colour when little is left. */
function FreeCell({ amount, share, title }: { amount: string; share: number; title: string }) {
  return (
    <span className="rk-r rk-num" title={title} style={{ color: share < 10 ? 'var(--warn)' : undefined }}>
      {amount}
    </span>
  )
}

function Cell({ r, id, now }: { r: NodeRow; id: ColumnId; now: number }) {
  switch (id) {
    case 'name':
      return (
        <span className="rk-mono rk-pname" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {r.name}
        </span>
      )
    case 'role': {
      const t = roleTag(r)
      return (
        <span>
          <span className="rk-role" data-role={r.role} title={t.title}>
            {t.text}
          </span>
        </span>
      )
    }
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
    case 'cpuAlloc':
      return (
        <span className="rk-r rk-num" style={{ color: 'var(--fg2)' }} title={`${fCores(r.cpu.allocatable)} cores allocatable to pods, of ${fCores(r.cpu.capacity)} on the machine`}>
          {fCores(r.cpu.allocatable)}
        </span>
      )
    case 'cpuFree':
      return <FreeCell amount={fCores(free(r.cpu))} share={100 - (r.cpu.allocatable ? (r.cpu.requested / r.cpu.allocatable) * 100 : 100)} title={`${fCores(free(r.cpu))} of ${fCores(r.cpu.allocatable)} cores free to schedule`} />
    case 'memory':
      return (
        <span>
          <MeterCell m={memoryMeter(r)} />
        </span>
      )
    case 'memAlloc':
      return (
        <span className="rk-r rk-num" style={{ color: 'var(--fg2)' }} title={`${fMem(r.memory.allocatable)} allocatable to pods, of ${fMem(r.memory.capacity)} on the machine`}>
          {fMem(r.memory.allocatable)}
        </span>
      )
    case 'memFree':
      return <FreeCell amount={fMem(free(r.memory))} share={100 - (r.memory.allocatable ? (r.memory.requested / r.memory.allocatable) * 100 : 100)} title={`${fMem(free(r.memory))} of ${fMem(r.memory.allocatable)} free to schedule`} />
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
    case 'labels': {
      const labels = ownLabels(r)
      return (
        <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg2)' }} title={labels.join('\n') || 'Only kubernetes.io labels'}>
          {labels.length ? labels.join(', ') : '—'}
        </span>
      )
    }
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

/** Rows in the first render: more than a tall screen shows. */
const FIRST_ROWS = 60

/**
 * Renders the first screenful at once and the rest over the next few
 * frames, so a large cluster's first screen does not wait for every row.
 */
function useProgressiveCount(total: number): number {
  const [limit, setLimit] = useState(FIRST_ROWS)
  useEffect(() => {
    if (limit >= total) return
    const frame = requestAnimationFrame(() => setLimit((l) => l * 4))
    return () => cancelAnimationFrame(frame)
  }, [limit, total])
  return Math.min(limit, total)
}

export function NodeTable({ rows, cols, template, minWidth, sortCol, sortDir, onSort, selected, onOpen, rowRef, now }: Props) {
  const count = useProgressiveCount(rows.length)
  const shown = count < rows.length ? rows.slice(0, count) : rows
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
        {shown.map((r) => (
          <Row key={r.name} r={r} cols={cols} selected={selected === r.name} onOpen={onOpen} rowRef={rowRef} now={now} />
        ))}
        {rows.length === 0 && <p style={{ margin: 0, padding: '28px 16px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg3)' }}>No nodes match this filter.</p>}
      </div>
    </div>
  )
}
