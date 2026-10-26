// What a credential in the connection editor says about itself before it
// is sent (P3-05): a token's account and expiry, a kubeconfig's contexts.
// The server checks everything again; this only saves a round trip and
// fills the form in from a pasted kubeconfig.

import type { AuthMethod, CAMode, Draft } from '../api/clusters'
import type { Tone } from '../api/types'

export const NAME_RE = /^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$/
export const URL_RE = /^https?:\/\/([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(?::(\d{1,5}))?(\/\S*)?$/
export const JWT_RE = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$/
export const PEM_RE = /-----BEGIN CERTIFICATE-----[\s\S]+?-----END CERTIFICATE-----/
export const KEY_RE = /-----BEGIN (?:EC |RSA )?PRIVATE KEY-----[\s\S]+?-----END (?:EC |RSA )?PRIVATE KEY-----/

export interface Claims {
  sub?: string
  exp?: number
  iss?: string
  [k: string]: unknown
}

/** The claims of a JWT, without checking its signature; null when it is not one. */
export function jwtClaims(token: string): Claims | null {
  const parts = token.trim().split('.')
  if (parts.length !== 3) return null
  try {
    const b64 = parts[1]!.replace(/-/g, '+').replace(/_/g, '/')
    const json = decodeURIComponent(
      atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4))
        .split('')
        .map((c) => '%' + c.charCodeAt(0).toString(16).padStart(2, '0'))
        .join(''),
    )
    const claims = JSON.parse(json) as unknown
    return claims && typeof claims === 'object' ? (claims as Claims) : null
  } catch {
    return null
  }
}

/** Who a token signs in as: "namespace/name" for a service account, else its subject. */
export function jwtWho(claims: Claims | null): string {
  if (!claims) return ''
  const sub = typeof claims.sub === 'string' ? claims.sub : ''
  const m = /^system:serviceaccount:([^:]+):(.+)$/.exec(sub)
  if (m) return `${m[1]}/${m[2]}`
  return sub
}

export interface Kubeconfig {
  clusters: Record<string, string>[]
  contexts: Record<string, string>[]
  users: Record<string, string>[]
  current: string
}

/**
 * The parts of a kubeconfig the editor needs: cluster servers and CA data,
 * contexts, users with exec plugins. A light reader of the usual layout;
 * the server parses it properly when it is saved.
 */
export function parseKubeconfig(text: string): Kubeconfig {
  const out: Kubeconfig = { clusters: [], contexts: [], users: [], current: '' }
  let section: keyof Kubeconfig | '' = ''
  let item: Record<string, string> | null = null
  let nested = ''
  const clean = (v: string) => v.trim().replace(/^["']|["']$/g, '')
  for (const line of (text || '').split(/\r?\n/)) {
    if (/^\s*#/.test(line) || !line.trim()) continue
    if (/^[A-Za-z]/.test(line)) {
      const m = /^([\w-]+):\s*(.*)$/.exec(line)
      const key = m?.[1] ?? ''
      section = key === 'clusters' || key === 'contexts' || key === 'users' ? key : ''
      if (key === 'current-context') out.current = clean(m?.[2] ?? '')
      item = null
      continue
    }
    if (!section) continue
    const start = /^\s*-\s+([\w-]+):\s*(.*)$/.exec(line)
    if (start) {
      item = {}
      out[section].push(item)
      nested = ''
      if (start[2]) item[start[1]!] = clean(start[2])
      continue
    }
    if (!item) continue
    // The item's own fields are one level in; "cluster:", "context:" and
    // "user:" hold the fields that matter one level further.
    const kv = /^(\s+)([\w-]+):\s*(.*)$/.exec(line)
    if (!kv) continue
    const key = kv[2]!
    const value = clean(kv[3] ?? '')
    if (!value) {
      nested = key
      if (key === 'exec') item.exec = 'yes'
      continue
    }
    if (nested === 'exec' && key === 'command') item.command = value
    if (/^(name|server|cluster|user|namespace|certificate-authority-data|certificate-authority|token|insecure-skip-tls-verify|client-certificate-data|client-key-data|command)$/.test(key)) item[key] = value
  }
  return out
}

/** The context a kubeconfig uses: the one wanted, else the current one, else the first. */
export function kubeContext(k: Kubeconfig, want: string): { name: string; ctx: Record<string, string> | null; cluster: Record<string, string> | null; user: Record<string, string> | null } {
  const name = want || k.current || k.contexts[0]?.name || ''
  const ctx = k.contexts.find((c) => c.name === name) ?? null
  const cluster = ctx ? (k.clusters.find((c) => c.name === ctx.cluster) ?? null) : null
  const user = ctx ? (k.users.find((u) => u.name === ctx.user) ?? null) : null
  return { name, ctx, cluster, user }
}

/** "2026-11-30" */
function ymd(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}

function listText(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return items.slice(0, -1).join(', ') + ' and ' + items[items.length - 1]
}

export interface CredNote {
  tone: Tone
  text: string
}

/** What the credential fields of a draft say, for the note under them; null when nothing is typed yet. */
export function credentialNote(d: Draft, now = Date.now()): CredNote | null {
  switch (d.auth as AuthMethod) {
    case 'token': {
      const t = (d.token ?? '').trim()
      if (!t) return null
      if (!JWT_RE.test(t)) return { tone: 'err', text: 'That isn’t a service account token: those are three dot-separated parts that start with eyJ.' }
      const claims = jwtClaims(t)
      const who = jwtWho(claims)
      const exp = claims?.exp ? claims.exp * 1000 : 0
      if (exp && exp < now) return { tone: 'err', text: `This token expired on ${ymd(exp)}. Create a new one.` }
      return { tone: 'ok', text: `Token for ${who || 'an account the token doesn’t name'}${exp ? ', valid until ' + ymd(exp) : ', with no expiry'}.` }
    }
    case 'kubeconfig': {
      if (!(d.kubeconfig ?? '').trim()) return null
      const k = parseKubeconfig(d.kubeconfig ?? '')
      if (!k.contexts.length) return { tone: 'err', text: 'That kubeconfig has no contexts.' }
      const kc = kubeContext(k, (d.context ?? '').trim())
      if (!kc.ctx) return { tone: 'err', text: `The kubeconfig has no context “${kc.name}”. It has ${listText(k.contexts.map((c) => c.name ?? '?'))}.` }
      if (kc.user?.exec) return { tone: 'warn', text: `Context ${kc.name} signs in through an exec plugin${kc.user.command ? ' (' + kc.user.command + ')' : ''}, which the console can’t run. Use a token or a client certificate for it instead.` }
      const srv = kc.cluster?.server
      const field = (d.server ?? '').trim()
      const differs = !!(srv && field && srv !== field)
      return { tone: differs ? 'warn' : 'ok', text: `Context ${kc.name}: user ${kc.ctx.user || '?'} on cluster ${kc.ctx.cluster || '?'}.${differs ? ` Its server is ${srv}, not ${field}.` : ''}` }
    }
    case 'oidc': {
      const iss = (d.issuer ?? '').trim()
      const cid = (d.clientID ?? '').trim()
      if (!iss && !cid) return null
      if (!/^https:\/\//.test(iss) || !URL_RE.test(iss)) return { tone: 'err', text: 'The issuer URL starts with https://, for example https://login.corp.example/realms/data.' }
      if (!cid) return { tone: 'err', text: 'Add the client ID the API server trusts (its --oidc-client-id).' }
      return { tone: 'ok', text: `People sign in at ${URL_RE.exec(iss)![1]} as client “${cid}”; Kubernetes sees each person’s own user and groups, so RBAC decides what they see.` }
    }
    case 'cert': {
      if (!(d.certPem ?? '').trim() && !(d.keyPem ?? '').trim()) return null
      if (!PEM_RE.test(d.certPem ?? '')) return { tone: 'err', text: 'Paste the client certificate (PEM).' }
      if (!KEY_RE.test(d.keyPem ?? '')) return { tone: 'err', text: 'Paste the private key (PEM) that goes with the certificate.' }
      return { tone: 'ok', text: 'Client certificate and key provided.' }
    }
    default:
      return null
  }
}

/** The certificate authority data of a kubeconfig's context, decoded; "" when it has none. */
export function kubeconfigCA(k: Kubeconfig, context: string): string {
  const kc = kubeContext(k, context)
  const data = kc.cluster?.['certificate-authority-data']
  if (!data) return ''
  try {
    const pem = atob(data)
    return PEM_RE.test(pem) ? pem : ''
  } catch {
    return ''
  }
}

/** The RBAC an account in another cluster needs for the console (P3-05). */
export function rbacYAML(mode: 'read' | 'write', impersonate: boolean): string {
  const role = mode === 'write' ? 'cluster-console-operate' : 'cluster-console-read'
  let y = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: cluster-console
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${role}
rules:
  # Everything the tabs show, read only. Secrets are listed by
  # key names and sizes only; their values never leave the console.
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
`
  if (impersonate) {
    y += `  # Read as each signed-in person, so their own RBAC decides
  # what they see.
  - apiGroups: [""]
    resources: ["users", "groups"]
    verbs: ["impersonate"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]
    verbs: ["create"]
`
  }
  if (mode === 'write') {
    y += `  # Changes people approve in Watson. With sign-in on they run as
  # the person who approves them, whose own RBAC decides; these
  # rules are for a console with sign-in off. Label, annotate and
  # Helm rollbacks need patch on whatever they touch: bind the
  # built-in edit role too for those.
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["patch", "update"] # cordon, label
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["delete"]
  - apiGroups: [""]
    resources: ["pods/eviction"]
    verbs: ["create"] # drain
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "daemonsets", "replicasets"]
    verbs: ["patch"] # rollout restart and undo
  - apiGroups: ["apps"]
    resources: ["deployments/scale", "statefulsets/scale", "replicasets/scale"]
    verbs: ["patch", "update"] # scale
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["patch", "delete"] # expand and delete a claim (ST-18)
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses"]
    verbs: ["patch"] # set or remove the default class
`
  }
  y += `---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${role}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${role}
subjects:
  - kind: ServiceAccount
    name: cluster-console
    namespace: kube-system

# Then a token that lasts a year:
# kubectl -n kube-system create token cluster-console --duration=8760h
`
  return y
}

/** Whether a method needs the CA pasted and whether it needs credentials typed. */
export function needsCA(ca: CAMode, replacing: boolean, savedCA: CAMode | null): boolean {
  return ca === 'pem' && (savedCA !== 'pem' || replacing)
}
