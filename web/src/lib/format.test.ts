import { describe, expect, it } from 'vitest'
import { compareNatural, compareValues, fCores, fMem, fmtAge, fmtLocal, fmtMinutes, fmtTs, nWord, pct, pctTxt, round, trimNum } from './format'

describe('numbers', () => {
  it('trims trailing zeros', () => {
    expect(trimNum(2.5, 2)).toBe('2.5')
    expect(trimNum(3, 2)).toBe('3')
    expect(trimNum(0.125, 2)).toBe('0.13')
  })

  it('shows millicores as cores', () => {
    expect(fCores(6900)).toBe('6.9')
    expect(fCores(250)).toBe('0.25')
    expect(fCores(15800)).toBe('15.8')
    expect(fCores(0)).toBe('0')
  })

  it('shows bytes as Mi, Gi or Ti', () => {
    expect(fMem(512 * 1024 * 1024)).toBe('512Mi')
    expect(fMem(1.5 * 1024 ** 3)).toBe('1.5Gi')
    expect(fMem(250 * 1024 ** 3)).toBe('250Gi')
    expect(fMem(2 * 1024 ** 4)).toBe('2Ti')
    expect(fMem(null)).toBe('—')
  })

  it('shows percentages, with <1% for tiny shares', () => {
    expect(pct(1, 4)).toBe(25)
    expect(pct(1, 0)).toBe(0)
    expect(pctTxt(0)).toBe('0%')
    expect(pctTxt(0.4)).toBe('<1%')
    expect(pctTxt(68.6)).toBe('69%')
    expect(round(1.23456, 2)).toBe(1.23)
  })

  it('counts words', () => {
    expect(nWord(1, 'pod', 'pods')).toBe('1 pod')
    expect(nWord(0, 'pod', 'pods')).toBe('0 pods')
  })
})

describe('time', () => {
  it('formats ages as the prototype does', () => {
    expect(fmtMinutes(0.75)).toBe('45s')
    expect(fmtMinutes(12)).toBe('12m')
    expect(fmtMinutes(185)).toBe('3h 5m')
    expect(fmtMinutes(120)).toBe('2h')
    expect(fmtMinutes(52 * 60)).toBe('2d 4h')
    expect(fmtMinutes(30 * 24 * 60 + 300)).toBe('30d')
  })

  it('measures age from a timestamp', () => {
    const now = Date.parse('2026-10-12T12:00:00Z')
    expect(fmtAge('2026-10-12T11:48:00Z', now)).toBe('12m')
    expect(fmtAge(undefined, now)).toBe('—')
  })

  it('writes timestamps in UTC', () => {
    expect(fmtTs('2026-10-12T14:03:07Z')).toBe('2026-10-12 14:03:07 UTC')
    expect(fmtTs('not a time')).toBe('—')
  })
})

describe('ordering', () => {
  it('sorts names naturally', () => {
    const names = ['worker-10', 'worker-2', 'cp-1', 'worker-1']
    expect(names.sort(compareNatural)).toEqual(['cp-1', 'worker-1', 'worker-2', 'worker-10'])
  })

  it('compares numbers numerically and text naturally', () => {
    expect(compareValues(9, 10)).toBeLessThan(0)
    expect(compareValues('v1.9', 'v1.10')).toBeLessThan(0)
  })
})

describe('local times', () => {
  it('shows a date and a 12-hour time in the browser’s time zone', () => {
    expect(fmtLocal(new Date(2026, 4, 3, 13, 34).toISOString())).toBe('2026-05-03 1:34pm')
    expect(fmtLocal(new Date(2026, 0, 9, 0, 5).toISOString())).toBe('2026-01-09 12:05am')
    expect(fmtLocal(new Date(2026, 0, 9, 12, 0).toISOString())).toBe('2026-01-09 12:00pm')
    expect(fmtLocal(undefined)).toBe('—')
  })
})
