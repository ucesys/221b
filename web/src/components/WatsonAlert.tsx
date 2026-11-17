// What a Watson card says before anything else: a change to the release
// that runs this console (asked November 17, 2026).

import type { WatsonPlan } from '../api/watson'

export function CardAlert({ plan }: { plan?: WatsonPlan }) {
  if (!plan?.alert) return null
  return (
    <div className="rk-tl" data-k="err" role="alert" style={{ margin: '6px 0', fontWeight: 600 }} data-testid="watson-alert">
      {plan.alert}
    </div>
  )
}
