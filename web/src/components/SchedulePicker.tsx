import { useEffect, useState } from 'react'
import { fmtTs } from '../lib/format'
import { Select } from './Select'

/** A schedule as the console keeps it (package schedule's Spec); Administration › Backup's has the same shape. */
export interface Schedule {
  kind: 'daily' | 'weekdays' | 'weekly' | 'hours' | 'cron'
  at?: string
  days?: string[]
  every?: number
  cron?: string
  timeZone?: string
}

/** What the server says of a schedule: in words and its next times, or what is wrong. */
export interface ScheduleWords {
  description?: string
  next?: string[]
  error?: string
}

export const SCHEDULE_KINDS: [Schedule['kind'], string][] = [
  ['daily', 'Every day'],
  ['weekdays', 'Weekdays'],
  ['weekly', 'Weekly on…'],
  ['hours', 'Every N hours'],
  ['cron', 'Cron expression'],
]

const DAYS: [string, string][] = [
  ['mon', 'Mon'],
  ['tue', 'Tue'],
  ['wed', 'Wed'],
  ['thu', 'Thu'],
  ['fri', 'Fri'],
  ['sat', 'Sat'],
  ['sun', 'Sun'],
]

/** The time zones the browser knows. */
function zones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    return []
  }
}

function Labeled({ label, children, width }: { label: string; children: React.ReactNode; width?: number | string }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)', width: width ?? 'auto', minWidth: 0 }}>
      {label}
      {children}
    </label>
  )
}

/** The cron expression a schedule stands for, for the line under the picker. */
export function cronOf(s: Schedule): string {
  if (s.kind === 'cron') return s.cron ?? ''
  const [h, m] = (s.at ?? '00:00').split(':').map((x) => Number(x) || 0)
  switch (s.kind) {
    case 'daily':
      return `${m} ${h} * * *`
    case 'weekdays':
      return `${m} ${h} * * 1-5`
    case 'weekly': {
      const idx = (s.days ?? []).map((d) => ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'].indexOf(d)).filter((i) => i >= 0)
      return `${m} ${h} * * ${idx.join(',') || '?'}`
    }
    case 'hours':
      return `${m} ${h}/${s.every ?? 1} * * *`
  }
}

/**
 * The schedule picker (Phase 12): every day, weekdays, weekly on some
 * days, every N hours or a cron expression, with a time zone, read back
 * in plain words with its next three times as it is edited. AIOps › Jobs
 * uses it; Administration › Backup's scheduled backups can too.
 */
export function SchedulePicker({ value, onChange, describe, defaultZone = 'UTC', disabled, testId = 'sched' }: { value: Schedule; onChange: (s: Schedule) => void; describe?: (s: Schedule) => Promise<ScheduleWords>; defaultZone?: string; disabled?: boolean; testId?: string }) {
  const [tz] = useState(zones)
  const [words, setWords] = useState<ScheduleWords>({})
  const set = (p: Partial<Schedule>) => onChange({ ...value, ...p })
  const key = JSON.stringify(value)
  useEffect(() => {
    if (!describe) return
    let live = true
    const t = setTimeout(() => {
      describe(value)
        .then((w) => live && setWords(w))
        .catch((e: unknown) => live && setWords({ error: e instanceof Error ? e.message : String(e) }))
    }, 250)
    return () => {
      live = false
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  const list = `${testId}-zones`
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid={testId}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 10, alignItems: 'end' }}>
        <Labeled label="Repeat">
          <Select label="Repeat" value={value.kind} options={SCHEDULE_KINDS} disabled={disabled} onChange={(v) => set({ kind: v as Schedule['kind'], at: value.at || '02:00', every: value.every || 6, days: value.days?.length ? value.days : ['mon'] })} testId={`${testId}-kind`} width="100%" />
        </Labeled>
        {value.kind === 'hours' && (
          <Labeled label="Every (hours)">
            <span className="rk-field">
              <input type="number" min={1} max={23} value={value.every ?? 6} disabled={disabled} onChange={(e) => set({ every: Number(e.target.value) || 1 })} data-testid={`${testId}-every`} />
            </span>
          </Labeled>
        )}
        {value.kind !== 'cron' ? (
          <Labeled label={value.kind === 'hours' ? 'From' : 'At'}>
            <span className="rk-field">
              <input type="time" value={value.at ?? '02:00'} disabled={disabled} onChange={(e) => set({ at: e.target.value })} data-testid={`${testId}-at`} />
            </span>
          </Labeled>
        ) : (
          <Labeled label="Cron (minute hour day month weekday)">
            <span className="rk-field">
              <input spellCheck={false} className="rk-mono" value={value.cron ?? ''} placeholder="0 2 * * *" disabled={disabled} onChange={(e) => set({ cron: e.target.value })} data-testid={`${testId}-cron`} />
            </span>
          </Labeled>
        )}
        <Labeled label="Time zone">
          <span className="rk-field">
            <input spellCheck={false} list={list} value={value.timeZone ?? ''} placeholder={defaultZone} disabled={disabled} onChange={(e) => set({ timeZone: e.target.value })} data-testid={`${testId}-tz`} />
          </span>
          <datalist id={list}>
            {tz.map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
        </Labeled>
      </div>
      {value.kind === 'weekly' && (
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', fontSize: 12.5 }} role="group" aria-label="Days">
          {DAYS.map(([d, l]) => (
            <label key={d} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <input type="checkbox" disabled={disabled} checked={!!value.days?.includes(d)} onChange={(e) => set({ days: e.target.checked ? [...(value.days ?? []), d] : (value.days ?? []).filter((x) => x !== d) })} data-testid={`${testId}-day-${d}`} />
              {l}
            </label>
          ))}
        </div>
      )}
      {words.error ? (
        <span className="rk-t" data-tone="err" style={{ fontSize: 12 }} data-testid={`${testId}-error`}>
          {words.error}
        </span>
      ) : (
        <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)' }} data-testid={`${testId}-words`}>
          {cronOf(value)}
          {words.description ? ` · ${words.description}` : ''}
          {words.next?.length ? ` · next ${words.next.map((t) => fmtTs(t)).join(', ')}` : ''}
        </span>
      )}
    </div>
  )
}
