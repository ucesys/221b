// The shapes the server sends. They mirror server/internal/nodes and
// server/internal/kube; keep the two in step.

export type Tone = 'ok' | 'warn' | 'err' | 'info' | 'muted'

export interface Resource {
  /** CPU in millicores or memory in bytes. */
  requested: number
  limits: number
  allocatable: number
  capacity: number
  /** null when metrics-server has no reading for the node. */
  used: number | null
}

export interface PodCounts {
  running: number
  capacity: number
  notReady: number
  failing: number
  starting: number
  unready: number
  completed: number
}

export interface Entry {
  reason: string
  message: string
  object?: string
  since?: string
}

export interface Problem {
  title: string
  severity: 'error' | 'warning'
  summary: string
  entries: Entry[]
}

export interface Condition {
  type: string
  status: string
  reason?: string
  message?: string
  since?: string
}

export interface Taint {
  key: string
  value?: string
  effect: string
}

export interface NodeRow {
  name: string
  role: 'control-plane' | 'worker'
  roles: string[]
  status: string
  tone: Tone
  statusTip: string
  cordoned: boolean
  problems: Problem[]
  errors: number
  warnings: number
  cpu: Resource
  memory: Resource
  pods: PodCounts
  taints: Taint[]
  zone: string
  kubelet: string
  created: string
  internalIP: string
  os: string
  kernel: string
  runtime: string
  arch: string
  labels: Record<string, string>
  conditions: Condition[]
  /** When the node last booted, from its kubelet; absent until read, or when it cannot be. */
  bootedAt?: string
}

export interface ClusterInfo {
  name: string
  version: string
  platform?: string
  mode: string
  state: 'connecting' | 'connected' | 'disconnected'
  since: string
  message?: string
  synced: boolean
  warnings: string[]
  metrics: 'unknown' | 'available' | 'unavailable'
  metricsMessage?: string
  metricsAt?: string
  uptime: 'unknown' | 'available' | 'unavailable'
  uptimeMessage?: string
}

/** The settings everyone shares, kept on the server. */
export interface Settings {
  refreshSeconds: number
  /** Where the server keeps them: "configmap" survives restarts, "memory" does not. */
  storage: 'configmap' | 'memory'
  changedAt?: string
}

export interface Snapshot {
  version: number
  at: string
  refreshedAt: string | null
  cluster: ClusterInfo | null
  settings: Settings | null
  unscheduled: number
  rows: NodeRow[]
}

export interface Delta {
  version: number
  at: string
  cluster?: ClusterInfo
  settings?: Settings
  upserts?: NodeRow[]
  removes?: string[]
  unscheduled?: number
}

/** Sent on every refresh tick, with or without a delta. */
export interface Refresh {
  version: number
  at: string
}
