// The pending cases, read once and shared (CW-11, CU-08): whether the
// person works the cases (New Case, an alert's New Case) comes with them.
import { fetchCases, type CasesView } from '../../api/casework'

/** The pending cases, shared by what asks whether the person works the cases. */
export const WAITING_CASES = 'cases.cases.waiting'
// A person who may not see the cases has none pending.
export const loadWaiting = (background: boolean): Promise<CasesView> => fetchCases({ pending: true }, background).catch(() => ({ cases: [], clusters: [], mayChange: false, me: '' }))
