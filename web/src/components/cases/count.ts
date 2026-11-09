// The issue service's lists that several parts of Cases show (CS-04):
// the open reports and every known issue, read once for all of them. What
// waits for the person, counted, is the Inbox's (useInbox.ts).
import { fetchKnownAll, fetchReports, type OpKnown, type OpReport } from '../../api/issues'
import { useShared } from '../../lib/cases'

/** The open reports, shared by the top bar and the Inbox. */
export const OPEN_REPORTS = 'cases.reports.open'
export const loadOpenReports = (background: boolean) => fetchReports({ open: true }, background)
/** Every known issue, drafts too, shared by the top bar, the Inbox and Known issues. */
export const ALL_KNOWN = 'cases.known.all'
export const loadAllKnown = (background: boolean) => fetchKnownAll(background)

export function useOpenReports(on: boolean) {
  return useShared<OpReport[]>(on ? OPEN_REPORTS : null, loadOpenReports)
}

export function useAllKnown(on: boolean) {
  return useShared<OpKnown[]>(on ? ALL_KNOWN : null, loadAllKnown)
}
