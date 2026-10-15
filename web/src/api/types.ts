// The shapes the server sends. They mirror server/internal/nodes,
// server/internal/views, server/internal/live and server/internal/kube;
// keep the two in step.

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
  /** The problem is the node's pods, not the node itself. */
  pods?: boolean
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

/** CPU and memory in use, as metrics-server reports them. */
export interface Usage {
  cpuMilli: number
  memoryBytes: number
}

/** One pod as the Pods tab lists it. Usage comes apart from the row. */
export interface PodRow {
  key: string
  name: string
  namespace: string
  uid: string
  node: string
  status: string
  tone: Tone
  statusTip?: string
  phase: string
  ready: number
  total: number
  restarts: number
  lastRestart?: string
  lastTermination?: string
  cpuRequest: number
  cpuLimit: number
  memoryRequest: number
  memoryLimit: number
  started?: string
  created: string
  ownerKind?: string
  ownerName?: string
  release?: string
  qos?: string
  labels: Record<string, string>
  done: boolean
}

/** A pod row with its usage, as the table shows it. */
export interface PodView extends PodRow {
  usage: Usage | null
}

export interface NamespaceRow {
  name: string
  status: string
  created: string
  group: 'apps' | 'system'
  counts: Record<string, number>
  objects: number
  kinds: number
  pods: number
  podsNotRunning: number
  podsFailing: number
  releases: string[]
  labels: Record<string, string>
}

/** One object of a namespace, in the kind tables of the Namespaces tab. */
export interface ObjectRow {
  key: string
  kind: string
  kindName: string
  apiVersion: string
  name: string
  namespace: string
  status: string
  tone: Tone
  details: string
  created: string
  order: number
}

/** One line of an object's details. */
export interface Fact {
  label: string
  value: string
  mono?: boolean
}

/** A ConfigMap's or Secret's key with its size; values never travel. */
export interface KeySize {
  key: string
  bytes: number
}

/** The drawer of any namespaced object: GET /api/v1/objects/{kind}/{ns}/{name}. */
export interface ObjectDetails extends ObjectRow {
  facts: Fact[]
  labels: Record<string, string>
  annotations: Record<string, string>
  conditions: PodCondition[]
  owners: Owner[]
  events: Event[]
  /** The pods a workload runs; empty for other kinds. */
  pods: PodRow[]
  workload: boolean
  keys?: KeySize[]
  /** The Helm release the object belongs to, by Helm's labels or the release manifest. */
  release?: string
  usage?: Record<string, Usage>
}

export interface ServiceRow {
  key: string
  name: string
  namespace: string
  uid: string
  type: string
  headless: boolean
  clusterIP: string
  ports: string[]
  ready: number
  total: number
  selector: boolean
  externalIPs: string[]
  pending: boolean
  pendingWhy?: string
  nodePorts: number[]
  hostnames: string[]
  release?: string
  created: string
  status: string
  tone: Tone
}

export interface ReleaseRow {
  key: string
  name: string
  namespace: string
  chart: string
  chartVersion: string
  appVersion: string
  revision: number
  status: string
  tone: Tone
  description: string
  updated?: string
  firstDeployed?: string
  revisions: number
  pods: number
  podsReady: number
  podsFailed: number
  group: 'apps' | 'addons'
}

// ---- details ----

export interface Owner {
  kind: string
  name: string
}

export interface Port {
  name?: string
  port: number
  protocol: string
  hostPort?: number
}

export interface Quantities {
  cpu: number
  memory: number
  ephemeral: number
}

export interface Mount {
  path: string
  volume: string
  subPath?: string
  readOnly: boolean
}

export interface EnvVar {
  name: string
  value?: string
  /** "Secret", "ConfigMap", "field" or "resource"; Secret values are never sent. */
  source?: string
  from?: string
  key?: string
  optional?: boolean
}

export interface EnvFrom {
  source: string
  name: string
  prefix?: string
  optional?: boolean
}

export interface ContainerRun {
  kind: 'running' | 'waiting' | 'terminated'
  reason?: string
  message?: string
  started?: string
  finished?: string
  exitCode?: number
  signal?: number
}

export interface ContainerDetail {
  name: string
  init: boolean
  sidecar: boolean
  image: string
  imageID?: string
  pullPolicy?: string
  command?: string[]
  args?: string[]
  workingDir?: string
  ports: Port[]
  requests: Quantities
  limits: Quantities
  securityContext: string[]
  probes: string[]
  mounts: Mount[]
  env: EnvVar[]
  envFrom: EnvFrom[]
  state: ContainerRun | null
  lastState: ContainerRun | null
  ready: boolean
  restarts: number
}

export interface ContainerMount extends Mount {
  container: string
}

export interface Claim {
  name: string
  phase: string
  capacity?: string
  storageClass?: string
  accessModes: string[]
  volume?: string
}

export interface Volume {
  name: string
  type: string
  source: string
  mounts: ContainerMount[]
  claim?: Claim
}

export interface Event {
  type: string
  reason: string
  message: string
  count: number
  first?: string
  last?: string
  source: string
}

export interface PodCondition {
  type: string
  status: string
  reason?: string
  message?: string
  since?: string
}

export interface PodDetails extends PodRow {
  podIP: string
  hostIP: string
  priority: number
  priorityClass?: string
  serviceAccount: string
  restartPolicy: string
  hostNetwork: boolean
  owners: Owner[]
  conditions: PodCondition[]
  nodeSelector: Record<string, string>
  tolerations: string[]
  affinity: string[]
  containers: ContainerDetail[]
  volumes: Volume[]
  annotations: Record<string, string>
  events: Event[]
  services: string[]
  message?: string
  unschedulable?: string
  usage: Usage | null
}

export interface ServicePort {
  name?: string
  port: number
  protocol: string
  targetPort: string
  nodePort?: number
}

export interface Endpoint {
  address: string
  ready: boolean
  pod?: string
  namespace?: string
  node?: string
  podStatus?: string
  tone?: Tone
}

/** One Ingress path that sends traffic to a Service. */
export interface IngressRef {
  name: string
  host: string
  path: string
  tls: boolean
}

export interface ServiceDetails extends ServiceRow {
  dnsName: string
  selectorText: string[]
  sessionAffinity: string
  trafficPolicy?: string
  portDetails: ServicePort[]
  endpoints: Endpoint[]
  ingresses: IngressRef[]
  labels: Record<string, string>
  annotations: Record<string, string>
  events: Event[]
}

export interface Revision {
  revision: number
  updated?: string
  status: string
  tone: Tone
  chart: string
  chartVersion: string
  appVersion: string
  description: string
}

export interface ReleaseResource {
  kind: string
  name: string
  namespace: string
  status?: string
  tone?: Tone
  details?: string
  watched: boolean
}

export interface ReleaseDetails extends ReleaseRow {
  notes: string
  values: string
  maskedValues: string[]
  history: Revision[]
  resources: ReleaseResource[]
  podRows: PodRow[]
  usage?: Record<string, Usage>
}

// ---- the stream ----

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
  /** "read", or "off" when the chart turned Secret reading off. */
  secrets: 'read' | 'off'
}

/** The settings everyone shares, kept on the server. */
export interface Settings {
  refreshSeconds: number
  /** Each cluster's fleets (P4-01), by connection id. */
  fleets?: Record<string, Fleet[]>
  /** Where the server keeps them: "configmap" survives restarts, "memory" does not. */
  storage: 'configmap' | 'memory'
  changedAt?: string
}

/** Cluster-wide totals, for the tabs' counts. */
export type Counts = Record<string, number>

export interface ViewSnapshot<R = unknown> {
  version: number
  rows: R[]
  usage?: Record<string, Usage>
}

export interface Snapshot {
  at: string
  refreshedAt: string | null
  cluster: ClusterInfo | null
  settings: Settings | null
  unscheduled: number
  counts?: Counts
  views: Record<string, ViewSnapshot>
}

/** A change to one view. Versions increase by one per change of a view. */
export interface RowsEvent<R = unknown> {
  view: string
  version: number
  at: string
  upserts?: R[]
  removes?: string[]
  usage?: Record<string, Usage>
  usageRemoves?: string[]
}

export interface MetaEvent {
  at: string
  cluster?: ClusterInfo
  settings?: Settings
  unscheduled?: number
  counts?: Counts
}

/** Sent on every refresh tick, with the versions of the views subscribed. */
export interface RefreshEvent {
  at: string
  versions: Record<string, number>
}

/** Who is signed in: GET /api/v1/me (P3-01, P3-03). */
export interface Me {
  /** How people sign in: none (sign-in off), oidc or local. */
  mode: 'none' | 'oidc' | 'local'
  user: {
    subject: string
    username: string
    email?: string
    name?: string
    groups?: string[]
  }
  admin: boolean
  method: string
  provider?: string
  sessionIdleSeconds: number
}

/** One row of an alert's table: an event, or a node's condition or state (P4-07). */
export interface AlertEntry {
  source: 'event' | 'condition' | 'state'
  reason: string
  message: string
  tone: string
  count: number
  first?: string
  last?: string
  reportedBy?: string
  /** A related pod, "namespace/name". */
  pod?: string
}

export interface AlertProblem {
  title: string
  severity: 'error' | 'warning'
  summary: string
}

/** An object with alerts: a pod, a node or a volume claim (P4-07). */
export interface AlertRow {
  key: string
  kind: 'pod' | 'node' | 'pvc'
  severity: 'failed' | 'pending' | 'recovered' | 'error' | 'warning'
  name: string
  namespace?: string
  status: string
  statusTone: string
  node?: string
  release?: string
  ready?: number
  total?: number
  restarts?: number
  started?: string
  lastReason?: string
  lastExit?: number
  lastMessage?: string
  lastAt?: string
  waiting?: string
  waitingMessage?: string
  why?: string
  oom?: boolean
  role?: 'control-plane' | 'worker'
  ip?: string
  zone?: string
  pool?: string
  problems?: AlertProblem[]
  volume?: string
  storageClass?: string
  capacity?: number
  requested?: number
  resizing?: boolean
  accessModes?: string[]
  users?: { pod: string; status: string; node?: string }[]
  events: AlertEntry[]
  /** Who acknowledged the alert as it is now, if anyone did. */
  ack?: { by: string; at: string }
}

/** A fleet's definition (P4-01). */
export interface Fleet {
  id: string
  name: string
  /** One of six swatches, 0 to 5. */
  color: number
  desc?: string
  /** "key=value"; "key=" requires an empty value. */
  labels: string[]
  /** "key[=value]:Effect". */
  taints: string[]
}

export interface FleetStats {
  nodes: number
  cpu: number
  memory: number
  cpuReq: number
  memoryReq: number
  pods: number
  cordoned: number
  errors: number
  warnings: number
}

/** A row of the fleets view: a fleet, the nodes in none, or a group worth saving. */
export interface FleetRow {
  key: string
  kind: 'fleet' | 'none' | 'suggestion'
  id?: string
  name?: string
  color: number
  desc?: string
  labels: string[]
  taints: string[]
  order: number
  members: string[]
  stats: FleetStats
}

/** Who made an RBAC object (P4-03), read from its labels. */
export interface RBACSource {
  kind: 'k8s' | 'kubeadm' | 'helm' | 'managed' | 'manual'
  release?: string
  manager?: string
}

/** One rule of a role. */
export interface RBACRule {
  verbs: string[]
  apiGroups?: string[]
  resources?: string[]
  resourceNames?: string[]
  nonResourceURLs?: string[]
}

/** A ClusterRole or Role, in the access view (key "role:…"). */
export interface RoleRow {
  key: string
  kind: 'ClusterRole' | 'Role'
  namespace?: string
  name: string
  rules: RBACRule[]
  aggregated?: boolean
  powers: string[]
  source: RBACSource
  created?: string
}

export interface RBACSubject {
  kind: 'User' | 'Group' | 'ServiceAccount'
  name: string
  namespace?: string
  /** A ServiceAccount that does not exist. */
  missing?: boolean
}

export type FindingSeverity = 'high' | 'medium' | 'low' | 'info'

export interface Finding {
  severity: FindingSeverity
  title: string
  text: string
  subject?: RBACSubject
  /** What would fix it, to copy, or to send to Watson when it runs that command. */
  command?: string
}

/** A ClusterRoleBinding or RoleBinding, in the access view (key "binding:…"). */
export interface BindingRow {
  key: string
  kind: 'ClusterRoleBinding' | 'RoleBinding'
  namespace?: string
  name: string
  roleKind: 'ClusterRole' | 'Role'
  roleName: string
  roleMissing?: boolean
  subjects: RBACSubject[]
  source: RBACSource
  created?: string
  findings: Finding[]
}

/** A ServiceAccount, in the access view (key "sa:ns/name"). */
export interface SARow {
  key: string
  namespace: string
  name: string
  created?: string
  pods: { name: string; status: string; tone: string }[]
  tokens: { name: string; created?: string }[]
}

export type AccessRow = RoleRow | BindingRow | SARow

// ---------- Policies (P4-04 to P4-06) ----------

export interface CNIRow {
  key: string
  name?: string
  enforces: 'yes' | 'no' | 'unknown'
  note?: string
}

export interface NoClassRow {
  key: string
  pods: number
  waiting?: string
  default?: string
}

export interface PSAPod {
  name: string
  reasons: string[]
  detail: string
}

export interface PSAResult {
  level: string
  version: string
  fail: PSAPod[]
  warnings: string[]
}

export interface PolicyNSRow {
  key: string
  name: string
  system?: boolean
  pods: number
  hostNetwork: number
  policies: number
  isolatedIn: number
  isolatedOut: number
  denyIn?: boolean
  denyOut?: boolean
  open: string[]
  enforce?: string
  enforceVersion?: string
  warn?: string
  warnVersion?: string
  audit?: string
  auditVersion?: string
  baseline: PSAResult
  restricted: PSAResult
  quotas: string[]
  limitRanges: string[]
  budgets: number
}

export interface NetPolRow {
  key: string
  namespace: string
  name: string
  selector: string
  ingress: boolean
  egress: boolean
  allows?: string
  allowsOut?: string
  pods: string[]
  created?: string
}

export interface PDBRow {
  key: string
  namespace: string
  name: string
  selector: string
  rule: string
  currentHealthy: number
  desiredHealthy: number
  expectedPods: number
  disruptionsAllowed: number
  unhealthyPolicy?: string
  pods: { name: string; node?: string; ready: boolean }[]
  overlap: string[]
  release?: string
  created?: string
}

export interface DrainPod {
  namespace: string
  name: string
  workload?: string
  emptyDir?: boolean
  budget?: string
  error?: string
}

export interface DrainRow {
  key: string
  node: string
  cordoned?: boolean
  evict: DrainPod[]
  daemonSet: string[]
  mirror: number
  blockers: DrainPod[]
  budgets: string[]
  verdict: 'blocked' | 'waits' | 'clean'
  why: string
  command: string
}

export interface QuotaItem {
  resource: string
  used: string
  hard: string
  pct: number
  tone: Tone
  usedValue: number
  hardValue: number
}

export interface QuotaRow {
  key: string
  namespace: string
  name: string
  items: QuotaItem[]
  scopes: string[]
  release?: string
  created?: string
}

export interface RolloutRow {
  key: string
  namespace: string
  name: string
  replicas: number
  strategy: string
  surge: number
  unavailable: number
  requests: Record<string, string>
  limits: Record<string, string>
  short: { quota: string; resource: string; need: string; left: string }[]
  stalls: boolean
}

export interface LimitsRow {
  key: string
  namespace: string
  name: string
  items: { type: string; defaultRequest?: Record<string, string>; default?: Record<string, string>; max?: Record<string, string>; min?: Record<string, string>; maxLimitRequestRatio?: Record<string, string> }[]
  atDefaults: string[]
  created?: string
}

export interface HookRow {
  key: string
  kind: 'Validating' | 'Mutating'
  config: string
  webhook: string
  rules: string[]
  service?: string
  serviceNamespace?: string
  serviceName?: string
  url?: string
  failurePolicy: string
  timeout: number
  sideEffects?: string
  matchPolicy?: string
  namespaces: string
  objects: string
  reinvocation?: string
  caBundle: boolean
  reach: { state: string; ready: number; total: number; text: string; tone: Tone; pods: { name: string; node?: string; ready: boolean }[] }
  release?: string
  created?: string
}

export interface PriorityRow {
  key: string
  name: string
  value: number
  globalDefault?: boolean
  preemption: string
  description?: string
  builtIn?: boolean
  pods: number
  workloads: string[]
  created?: string
}

export type PolicyRow = CNIRow | NoClassRow | PolicyNSRow | NetPolRow | PDBRow | DrainRow | QuotaRow | RolloutRow | LimitsRow | HookRow | PriorityRow

export interface NetSide {
  isolated: boolean
  allowed: boolean
  selecting: string[]
  allowedBy: string[]
  rules?: Record<string, string>
}

export interface NetCheck {
  from: string
  to: string
  port: number
  protocol: string
  allowed: boolean
  egress: NetSide
  ingress: NetSide
  fromHost?: boolean
  toHost?: boolean
  toIP?: string
}

export interface NetCheckResult {
  checks: NetCheck[]
  service?: { name: string; port: number; portName?: string; targetPort: string; protocol: string }
  allowed: number
}

// ---------- Networks (P4-02) ----------

export interface HopLink {
  kind: 'ingress' | 'ingressclass' | 'service' | 'pod' | 'secret' | 'netpol' | 'release' | 'node'
  namespace?: string
  name: string
}

export interface Hop {
  k: string
  t: string
  d: string
  s: string
  tone: Tone
  link?: HopLink
}

export interface Health {
  text: string
  tone: Tone
  tip?: string
}

export interface TraceRow {
  key: string
  host: string
  path: string
  pathType?: string
  url: string
  namespace: string
  ingress: string
  class?: string
  backend: string
  verdict: string
  tone: Tone
  text: string
  hops: Hop[]
  endpoints: Health
  policy: Health
  cert: Health
  notAfter?: string
  created?: string
}

export interface NodeNetRow {
  key: string
  name: string
  role: 'control-plane' | 'worker'
  internalIP: string
  externalIP?: string
  podCIDR: string
  capacity: number
  podIPs: number
  hostNetwork: number
  zone?: string
}

export interface NetFactsRow {
  key: string
  cni: CNIRow
  podCIDRs: string[]
  serviceIP?: string
  clusterIPs: number
  dns?: { service: string; ip: string; ready: number; total: number; nodes: string[] }
  kubeProxy?: string
  classes: { name: string; controller: string; default?: boolean; ready: number; total: number; hosts: number }[]
  pendingLBs: string[]
  policies: number
  policyNamespaces: string[]
}

export type NetworkRow = TraceRow | NodeNetRow | NetFactsRow
