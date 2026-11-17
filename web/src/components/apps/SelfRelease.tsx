// The console's own release, and where an application's values came from
// (asked November 17, 2026, after an upgrade of the console's release with
// an adopted application's few values reset the rest to the chart's
// defaults and took the console down).

import type { AppView } from '../../api/apps'

/** The callout at the top of the panel of the release that runs this console. */
export function SelfReleaseCallout() {
  return (
    <div className="rk-callout" data-tone="err" role="note" data-testid="app-self">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
        <strong style={{ fontSize: 12.5, fontWeight: 600 }}>This is the release that runs this console: if it fails, the console goes down.</strong>
        <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
          Upgrade, roll back or uninstall it with care. Watson refuses an upgrade of it that would remove values it has now (helm get values): keep them all in the application&rsquo;s values.
        </span>
      </div>
    </div>
  )
}

/** Where the application's values file came from, and how masked credentials save. */
export function ValuesOriginNote({ app }: { app: Pick<AppView, 'adoptedRevision' | 'valuesMasked'> }) {
  const masked = app.valuesMasked ?? []
  if (!app.adoptedRevision && masked.length === 0) return null
  return (
    <p className="rk-hint" style={{ margin: 0 }} data-testid="app-values-origin">
      {app.adoptedRevision
        ? `These values came from the release: its own values (helm get values) at revision ${app.adoptedRevision}, when the console took it over, with any typed then laid over them. A Redeploy deploys exactly what is here, so keep every value the release needs. `
        : ''}
      {masked.length > 0 ? `Credentials show masked (${masked.join(', ')}): leave a masked value as it is to keep it, or type over it to change it.` : ''}
    </p>
  )
}
