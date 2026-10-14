// The accounts the end-to-end suite signs in with: the simulated provider's
// (make e2e-sim) or the local accounts the chart in kind is installed with.
export const E2E_USER = process.env.E2E_USER ?? 'admin@corp.example'
export const E2E_PASSWORD = process.env.E2E_PASSWORD ?? 'password'
export const E2E_VIEWER = process.env.E2E_VIEWER ?? 'viewer@corp.example'
