// The kinds 221B watches, as the server's registry names them, so screens
// label and link them alike: NetworkPolicy → NetworkPolicies → networkpolicies.

interface KindNames {
  /** The server's name for the kind: "deployments". */
  name: string
  /** How screens label the kind in the plural: "Deployments". */
  plural: string
}

const KNOWN: Record<string, KindNames> = {
  Node: { name: 'nodes', plural: 'Nodes' },
  Namespace: { name: 'namespaces', plural: 'Namespaces' },
  Pod: { name: 'pods', plural: 'Pods' },
  Deployment: { name: 'deployments', plural: 'Deployments' },
  StatefulSet: { name: 'statefulsets', plural: 'StatefulSets' },
  DaemonSet: { name: 'daemonsets', plural: 'DaemonSets' },
  Job: { name: 'jobs', plural: 'Jobs' },
  CronJob: { name: 'cronjobs', plural: 'CronJobs' },
  ReplicaSet: { name: 'replicasets', plural: 'ReplicaSets' },
  ControllerRevision: { name: 'controllerrevisions', plural: 'ControllerRevisions' },
  Service: { name: 'services', plural: 'Services' },
  EndpointSlice: { name: 'endpointslices', plural: 'EndpointSlices' },
  Endpoints: { name: 'endpoints', plural: 'Endpoints' },
  Ingress: { name: 'ingresses', plural: 'Ingresses' },
  NetworkPolicy: { name: 'networkpolicies', plural: 'NetworkPolicies' },
  PersistentVolumeClaim: { name: 'persistentvolumeclaims', plural: 'PersistentVolumeClaims' },
  ConfigMap: { name: 'configmaps', plural: 'ConfigMaps' },
  Secret: { name: 'secrets', plural: 'Secrets' },
  ServiceAccount: { name: 'serviceaccounts', plural: 'ServiceAccounts' },
  Role: { name: 'roles', plural: 'Roles' },
  RoleBinding: { name: 'rolebindings', plural: 'RoleBindings' },
  PodDisruptionBudget: { name: 'poddisruptionbudgets', plural: 'PodDisruptionBudgets' },
  HorizontalPodAutoscaler: { name: 'horizontalpodautoscalers', plural: 'HorizontalPodAutoscalers' },
  ResourceQuota: { name: 'resourcequotas', plural: 'ResourceQuotas' },
  LimitRange: { name: 'limitranges', plural: 'LimitRanges' },
  Lease: { name: 'leases', plural: 'Leases' },
  Event: { name: 'events', plural: 'Events' },
}

/** The kinds that run pods, whose drawers list them. */
export const WORKLOAD_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob'])

/**
 * The plural of a kind, as Kubernetes pluralizes its resources: Ingress →
 * Ingresses, NetworkPolicy → NetworkPolicies, Endpoints → Endpoints. A kind
 * outside the registry (a CRD in a Helm manifest) follows the same rules.
 */
export function pluralKind(kind: string): string {
  const known = KNOWN[kind]
  if (known) return known.plural
  if (/(ss|sh|ch|x|z)$/.test(kind)) return kind + 'es'
  if (/s$/.test(kind)) return kind
  if (/[^aeiou]y$/.test(kind)) return kind.slice(0, -1) + 'ies'
  return kind + 's'
}

/** The server's name for a kind: Deployment → deployments, as the YAML and details URLs want it. */
export function kindNameOf(kind: string): string {
  return KNOWN[kind]?.name ?? pluralKind(kind).toLowerCase()
}

/** Whether 221B watches the kind, so its drawer can read it. */
export function isWatchedKind(kind: string): boolean {
  return kind in KNOWN
}
