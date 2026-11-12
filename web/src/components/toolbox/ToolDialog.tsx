import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { fmtTs } from '../../lib/format'
import { COMMAND_TOOLS, fetchToolbox, HOST_TOOLS, requestSession, ToolboxError, type ToolboxInfo, type ToolName, type ToolRequest } from '../../api/toolbox'
import type { PodDetails } from '../../api/types'
import { apiPath } from '../../lib/router'
import { BoltIcon, BoxIcon, CloseIcon, ForwardIcon, ShieldSmallIcon, TerminalIcon } from '../Icons'
import type { OpenTool } from './context'
import { trapTab } from '../../lib/focus'

interface Props {
  cluster: string
  clusterName: string
  open: OpenTool
  onClose: () => void
  /** The session opened, or its card waits in Watson for a second person. */
  onSent: (r: { opened?: string; summary: string }) => void
}

const DURATIONS = [15, 30, 60]

const PROFILE_WORDS: Record<string, string> = {
  general: 'Runs as the image’s own user, as capable as the namespace allows',
  restricted: 'Runs as non-root (user 65532) and can’t escalate',
  netadmin: 'NET_ADMIN and NET_RAW, for tcpdump and iptables',
  sysadmin: 'Privileged',
}

const NODE_PROFILES: Record<string, { title: string; words: string }> = {
  general: { title: 'Restricted', words: 'The host’s PID, network and IPC namespaces and its root at /host; not privileged. Enough for journalctl, crictl, df, ss.' },
  netadmin: { title: 'Network admin', words: 'The host’s network with NET_ADMIN and NET_RAW, for tcpdump, ip and iptables.' },
  sysadmin: { title: 'System admin', words: 'Privileged, with the host’s namespaces and /host: nsenter, mount, sysctl. Root over every pod on the node.' },
}

/**
 * A tool's dialog (TB-06): what to open, for how long, and on production
 * or for a node why (TB-33). Sending opens the session: the person filled
 * it in, so it needs no other approval, unless a second person's is
 * required, when it becomes a Watson card (TB-36).
 */
export function ToolDialog({ cluster, clusterName, open, onClose, onSent }: Props) {
  const { tool, pod: podRef } = open
  // Several nodes (the Nodes page's picked ones) pair on one card, one
  // session each; what the dialog starts with comes from the first.
  const nodes = tool === 'ssh-pair' && open.nodes && open.nodes.length > 1 ? open.nodes : undefined
  const node = open.node ?? open.nodes?.[0]
  const [info, setInfo] = useState<ToolboxInfo | null>(null)
  const [pod, setPod] = useState<PodDetails | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const pre = open.prefill ?? {}
  const [container, setContainer] = useState(pre.container ?? '')
  const [shell, setShell] = useState(pre.shell ?? 'auto')
  const [command, setCommand] = useState(pre.command ?? '')
  const [port, setPort] = useState(pre.port ? String(pre.port) : '')
  const [localPort, setLocalPort] = useState(pre.localPort ? String(pre.localPort) : '')
  const [image, setImage] = useState(pre.image ?? '')
  const [target, setTarget] = useState(pre.target ?? '')
  const [mount, setMount] = useState(pre.mountVolumes ?? true)
  const [readOnly, setReadOnly] = useState(pre.readOnlyVolumes ?? true)
  const [profile, setProfile] = useState(pre.profile ?? '')
  const [namespace, setNamespace] = useState(pre.namespace ?? podRef?.namespace ?? 'default')
  const [serviceAccount, setServiceAccount] = useState(pre.serviceAccount ?? '')
  const [labelsFrom, setLabelsFrom] = useState(pre.labelsFrom ?? '')
  // Undefined until typed in, so an emptied field stays empty.
  const [loginUser, setLoginUser] = useState(pre.loginUser)
  const [sshPort, setSshPort] = useState(pre.port ? String(pre.port) : undefined)
  const [minutes, setMinutes] = useState(pre.minutes ?? 30)
  const [reason, setReason] = useState(pre.reason ?? '')
  const first = useRef<HTMLElement | null>(null)
  const dlg = useRef<HTMLDivElement | null>(null)

  const ns = podRef?.namespace ?? (tool === 'debug-pod' ? namespace : '')
  useEffect(() => {
    let live = true
    fetchToolbox(cluster, ns ? [ns] : [], !!node).then(
      (i) => live && setInfo(i),
      (e: unknown) => live && setLoadErr(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      live = false
    }
  }, [cluster, ns, node])
  useEffect(() => {
    if (!podRef) return
    let live = true
    fetch(apiPath(`pods/${encodeURIComponent(podRef.namespace)}/${encodeURIComponent(podRef.name)}`, cluster), { headers: { Accept: 'application/json' } })
      .then((r) => (r.ok ? (r.json() as Promise<PodDetails>) : Promise.reject(new Error(`The pod could not be read (${r.status}).`))))
      .then(
        (d) => live && setPod(d),
        (e: unknown) => live && setLoadErr(e instanceof Error ? e.message : String(e)),
      )
    return () => {
      live = false
    }
  }, [cluster, podRef])
  // The fields arrive with the values and the pod, after the dialog opens:
  // the focus goes to the first of them then, or to the dialog until then.
  useEffect(() => {
    if (!dlg.current?.contains(document.activeElement)) (first.current ?? dlg.current)?.focus()
  }, [info, pod])
  // Esc cancels wherever the focus is.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      onClose()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onClose])

  const containers = useMemo(() => (pod?.containers ?? []).filter((c) => !c.init || c.sidecar), [pod])
  // What the person has not chosen yet comes from the pod and the values.
  const level = info?.namespaces[ns]?.level ?? 'privileged'
  const pick = (list: string[] | undefined) => (list?.includes('network') ? 'network' : (list?.[0] ?? ''))
  const containerV = container || containers[0]?.name || ''
  const targetV = target || containers[0]?.name || ''
  const imageV = image || (tool === 'debug-container' ? pick(info?.debugContainerImages) : tool === 'debug-pod' ? pick(info?.debugPodProfiles) : '')
  const profileV = profile || (tool === 'node-shell' ? (info?.nodeProfiles[0] ?? 'general') : tool === 'debug-container' ? 'restricted' : '')
  const minutesV = info ? Math.min(minutes, info.maxMinutes) : minutes

  const onNode = HOST_TOOLS.includes(tool)
  const needsTwo = onNode && info?.nodeApproval === 'always'
  const interactive = !COMMAND_TOOLS.includes(tool)
  const pairing = node ? info?.ssh?.pairings[node] : undefined
  const loginUserV = loginUser ?? pairing?.loginUser ?? info?.ssh?.loginUser ?? 'root'
  const sshPortV = sshPort ?? String(pairing?.port || info?.ssh?.port || 22)
  const approvers = !!info?.twoPerson || needsTwo
  // A reason is asked for whoever approves, and for a node on production;
  // a node shell elsewhere takes one if the person gives it.
  const needReason = !!info && (approvers || (onNode && info.production))
  const askReason = needReason || onNode

  const request = (): ToolRequest => {
    const r: ToolRequest = { tool, reason: reason.trim() || undefined, minutes: interactive ? minutesV : undefined }
    if (podRef) Object.assign(r, { namespace: podRef.namespace, pod: podRef.name })
    switch (tool) {
      case 'shell':
        return { ...r, container: containerV, shell: shell === 'auto' ? '' : shell }
      case 'command':
        return { ...r, container: containerV, command }
      case 'forward':
        return { ...r, port: Number(port), localPort: localPort === '' || localPort === 'auto' ? 0 : Number(localPort) }
      case 'debug-container':
        return { ...r, image: imageV, target: targetV, profile: profileV, mountVolumes: !!info?.mountVolumes && mount, readOnlyVolumes: mount && readOnly }
      case 'debug-pod':
        return { ...r, namespace, node, image: imageV, serviceAccount: serviceAccount.trim() || undefined, labelsFrom: labelsFrom.trim() || undefined }
      case 'node-shell':
        return { ...r, node, profile: profileV }
      case 'node-command':
        return { ...r, node, command }
      case 'ssh':
        return { ...r, node }
      case 'ssh-pair':
        return { ...r, node, ...(nodes ? { nodes: nodes.slice(1) } : {}), loginUser: loginUserV.trim(), port: Number(sshPortV) }
      case 'ssh-unpair':
        return { ...r, node }
    }
    return r
  }

  const problem = (): string => {
    if (!info) return 'Loading…'
    if ((tool === 'command' || tool === 'node-command') && !command.trim()) return 'Type the command to run.'
    if (tool === 'forward' && !(Number(port) >= 1 && Number(port) <= 65535)) return 'Choose the pod’s port.'
    if (tool === 'forward' && localPort !== '' && localPort !== 'auto' && !(Number(localPort) >= 1 && Number(localPort) <= 65535)) return 'The local port is a number from 1 to 65535, or auto.'
    if (tool === 'ssh-pair' && !/^[a-z_][a-z0-9_-]{0,31}$/.test(loginUserV.trim())) return 'The login user is a Unix user name: lowercase letters, digits, dash and underscore.'
    if (tool === 'ssh-pair' && !(Number(sshPortV) >= 1 && Number(sshPortV) <= 65535)) return 'The SSH port is a number from 1 to 65535.'
    if (needReason && reason.trim().length < 10) return 'Say why, in 10 characters or more.'
    return ''
  }
  const send = () => {
    const p = problem()
    if (p) {
      setErr(p)
      return
    }
    setBusy(true)
    setErr('')
    // Several nodes go on one card: one approval, one session each.
    requestSession(cluster, request(), open.from ?? 'Tools menu').then(
      (it) => {
        setBusy(false)
        if (it.opened) {
          onSent({ opened: it.opened.id, summary: it.opened.summary })
          onClose()
          return
        }
        if (it.status === 'refused') {
          setErr(it.reason || 'Watson refused it.')
          return
        }
        onSent({ summary: it.command.replace(/^session: /, '') })
        onClose()
      },
      (e: unknown) => {
        setBusy(false)
        setErr(e instanceof ToolboxError ? e.message : String(e))
      },
    )
  }

  const icon = {
    shell: <TerminalIcon size={18} />,
    'node-shell': <TerminalIcon size={18} />,
    command: <BoltIcon />,
    'node-command': <BoltIcon />,
    forward: <ForwardIcon />,
    'debug-container': <BoxIcon />,
    'debug-pod': <BoxIcon />,
    ssh: <TerminalIcon size={18} />,
    'ssh-pair': <TerminalIcon size={18} />,
    'ssh-unpair': <TerminalIcon size={18} />,
  }[tool]
  const title: ReactNode = {
    shell: (
      <>
        Shell in pod <span className="rk-mono">{podRef?.name}</span>
      </>
    ),
    command: (
      <>
        Run a command in pod <span className="rk-mono">{podRef?.name}</span>
      </>
    ),
    forward: (
      <>
        Port forward from pod <span className="rk-mono">{podRef?.name}</span>
      </>
    ),
    'debug-container': (
      <>
        Debug container for pod <span className="rk-mono">{podRef?.name}</span>
      </>
    ),
    'debug-pod': (
      <>
        Debug pod on node <span className="rk-mono">{node}</span>
      </>
    ),
    'node-shell': (
      <>
        K8S node shell on <span className="rk-mono">{node}</span>
      </>
    ),
    'node-command': (
      <>
        Run a command on node <span className="rk-mono">{node}</span>
      </>
    ),
    ssh: (
      <>
        SSH to node <span className="rk-mono">{node}</span>
      </>
    ),
    'ssh-pair': nodes ? (
      <>Pair {nodes.length} nodes for SSH</>
    ) : (
      <>
        {pairing ? 'Pair again' : 'Pair'} node <span className="rk-mono">{node}</span> for SSH
      </>
    ),
    'ssh-unpair': (
      <>
        Unpair node <span className="rk-mono">{node}</span> from SSH
      </>
    ),
  }[tool]
  const verb = approvers
    ? {
        shell: 'Request shell',
        command: 'Request command',
        forward: 'Request forward',
        'debug-container': 'Request debug container',
        'debug-pod': 'Request debug pod',
        'node-shell': 'Request K8S node shell',
        'node-command': 'Request command',
        ssh: 'Request SSH',
        'ssh-pair': 'Request pairing',
        'ssh-unpair': 'Request unpairing',
      }[tool]
    : {
        shell: 'Open shell',
        command: 'Run',
        forward: 'Start forward',
        'debug-container': 'Attach and open shell',
        'debug-pod': 'Start debug pod',
        'node-shell': 'Open K8S node shell',
        'node-command': 'Run',
        ssh: 'Open SSH',
        'ssh-pair': 'Pair the node',
        'ssh-unpair': 'Unpair the node',
      }[tool]
  const durationField = interactive && info && (
    <label className="tb-lbl" style={{ flex: '0 0 auto' }}>
      <span>For how long</span>
      <span className="rk-seg" role="group" aria-label="For how long">
        {DURATIONS.filter((d) => d <= info.maxMinutes).map((d) => (
          <button key={d} type="button" aria-pressed={minutesV === d} onClick={() => setMinutes(d)}>
            {d} min
          </button>
        ))}
      </span>
    </label>
  )
  const containerField = (label: string, value: string, set: (v: string) => void) => (
    <label className="tb-lbl">
      <span>{label}</span>
      <span className="rk-field" data-mono="true">
        <select ref={(el) => void (first.current ??= el)} value={value} onChange={(e) => set(e.target.value)} aria-label={label}>
          {containers.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name}
              {c.state?.kind && c.state.kind !== 'running' ? ` (${c.state.kind})` : ''}
            </option>
          ))}
          {!containers.length && <option value="">{pod ? 'no containers' : 'loading…'}</option>}
        </select>
      </span>
    </label>
  )
  const quick = (list: string[] | undefined) =>
    list && list.length > 0 ? (
      <div className="tb-chips" aria-label="Common commands" role="group">
        {list.map((c) => (
          <button key={c} type="button" className="rk-chip" onClick={() => setCommand(c)}>
            {c}
          </button>
        ))}
      </div>
    ) : null

  let body: ReactNode = null
  switch (tool) {
    case 'shell':
      body = (
        <div className="tb-row">
          {containerField('Container', containerV, setContainer)}
          <label className="tb-lbl">
            <span>Shell</span>
            <span className="rk-field">
              <select value={shell} onChange={(e) => setShell(e.target.value)} aria-label="Shell">
                <option value="auto">Auto: bash, then sh</option>
                <option value="bash">bash</option>
                <option value="sh">sh</option>
                <option value="ash">ash</option>
                <option value="zsh">zsh</option>
              </select>
            </span>
          </label>
          {durationField}
        </div>
      )
      break
    case 'command':
    case 'node-command':
      body = (
        <>
          {tool === 'command' && <div className="tb-row">{containerField('Container', containerV, setContainer)}</div>}
          <label className="tb-lbl">
            <span>Command{tool === 'node-command' ? ' · runs on the node through chroot /host' : ''}</span>
            <textarea
              ref={(el) => void (first.current ??= el)}
              className="rk-area tb-area"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              spellCheck={false}
              placeholder={tool === 'command' ? 'cat /etc/resolv.conf' : 'journalctl -u kubelet -n 200 --no-pager'}
              aria-label="Command"
            />
          </label>
          {quick(tool === 'command' ? info?.podCommands : info?.nodeCommands)}
          <p className="tb-hint" style={{ margin: 0 }}>
            Runs once, as {tool === 'command' ? 'a shell would, with sh -c' : 'root on the node'}; its output shows in the dock and goes to the audit log.
          </p>
        </>
      )
      break
    case 'forward': {
      const ports = containers.flatMap((c) => c.ports.filter((p) => p.protocol === 'TCP').map((p) => ({ c: c.name, p })))
      body = (
        <>
          <div className="tb-row">
            <label className="tb-lbl">
              <span>Pod port</span>
              <span className="rk-field" data-mono="true">
                <input ref={(el) => void (first.current ??= el)} list="tb-ports" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} placeholder="5432" aria-label="Pod port" />
                <datalist id="tb-ports">
                  {ports.map(({ c, p }) => (
                    <option key={c + p.port} value={p.port}>
                      {(p.name ? p.name + ' · ' : '') + 'container ' + c}
                    </option>
                  ))}
                </datalist>
              </span>
            </label>
            <label className="tb-lbl">
              <span>Local port</span>
              <span className="rk-field" data-mono="true">
                <input inputMode="numeric" value={localPort} onChange={(e) => setLocalPort(e.target.value.replace(/[^\dauto]/g, ''))} placeholder="auto" aria-label="Local port" />
              </span>
            </label>
            {durationField}
          </div>
          {ports.length > 0 && (
            <div className="tb-chips">
              {ports.map(({ c, p }) => (
                <button key={c + p.port} type="button" className="rk-chip" aria-pressed={port === String(p.port)} onClick={() => setPort(String(p.port))}>
                  {p.port}
                  {p.name ? ` ${p.name}` : ''}
                </button>
              ))}
            </div>
          )}
          <p className="tb-hint" style={{ margin: 0 }}>
            The dock then shows one command to run on your computer: the <span className="rk-mono">221b</span> command{info?.cliMinVersion ? ` (${info.cliMinVersion} or newer)` : ''} listens on the local port, and any program there reaches
            the pod through the console. It ends after {Math.round((info?.idleSeconds ?? 600) / 60)} min without traffic.
          </p>
        </>
      )
      break
    }
    case 'debug-container': {
      const allowed = level === 'restricted' ? ['restricted'] : ['restricted', 'general']
      body = (
        <>
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className="tb-lbl" style={{ marginBottom: 4 }}>
              Image
            </legend>
            {(info?.debugContainerImages ?? []).map((n) => {
              const im = info?.images.find((i) => i.name === n)
              return (
                <label key={n} className="tb-radio">
                  <input type="radio" name="tb-image" checked={imageV === n} onChange={() => setImage(n)} ref={(el) => void (first.current ??= el)} />
                  <span>
                    <span>
                      <b style={{ fontWeight: 500 }}>{n}</b>{' '}
                      <span className="rk-mono rk-dim" style={{ fontSize: 12 }}>
                        {im?.image}
                      </span>
                    </span>
                    <small>
                      {im?.tools}
                      {im?.caps?.length ? ` · adds ${im.caps.join(', ')} where allowed` : ''}
                    </small>
                  </span>
                </label>
              )
            })}
          </fieldset>
          <div className="tb-row">
            {containerField('Sees the processes of', targetV, setTarget)}
            <label className="tb-lbl">
              <span>Capabilities · namespace is {level}</span>
              <span className="rk-field">
                <select value={profileV} onChange={(e) => setProfile(e.target.value)} aria-label="Capabilities">
                  {allowed.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </select>
              </span>
            </label>
            {durationField}
          </div>
          <p className="tb-hint" style={{ margin: 0 }}>
            {PROFILE_WORDS[profileV] ?? ''}
          </p>
          {info?.mountVolumes && (
            <div className="tb-row" style={{ gap: 16 }}>
              <label className="rk-tcheck">
                <input type="checkbox" checked={mount} onChange={(e) => setMount(e.target.checked)} /> Mount the container’s volumes at the same paths
              </label>
              <label className="rk-tcheck" aria-disabled={!mount}>
                <input type="checkbox" checked={readOnly} disabled={!mount} onChange={(e) => setReadOnly(e.target.checked)} /> read-only
              </label>
            </div>
          )}
          <div className="rk-callout" data-tone="warn">
            <ShieldSmallIcon />
            <span>A debug container cannot be removed: it stays in the pod, stopped, until the pod is replaced, and shares the pod’s memory limit.</span>
          </div>
        </>
      )
      break
    }
    case 'debug-pod':
      body = (
        <>
          <div className="tb-row">
            <label className="tb-lbl">
              <span>Namespace</span>
              <span className="rk-field" data-mono="true">
                <input ref={(el) => void (first.current ??= el)} value={namespace} onChange={(e) => setNamespace(e.target.value.trim())} aria-label="Namespace" />
              </span>
            </label>
            {info?.admin && (
              <label className="tb-lbl">
                <span>Service account · its token goes with it</span>
                <span className="rk-field" data-mono="true">
                  <input value={serviceAccount} onChange={(e) => setServiceAccount(e.target.value.trim())} placeholder="none: no token" aria-label="Service account" />
                </span>
              </label>
            )}
            {durationField}
          </div>
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className="tb-lbl" style={{ marginBottom: 4 }}>
              Image
            </legend>
            {(info?.debugPodProfiles ?? []).map((n) => {
              const im = info?.images.find((i) => i.name === n)
              return (
                <label key={n} className="tb-radio">
                  <input type="radio" name="tb-image" checked={imageV === n} onChange={() => setImage(n)} />
                  <span>
                    <span>
                      <b style={{ fontWeight: 500 }}>{n}</b>{' '}
                      <span className="rk-mono rk-dim" style={{ fontSize: 12 }}>
                        {im?.image}
                      </span>
                    </span>
                    <small>{im?.tools}</small>
                  </span>
                </label>
              )
            })}
          </fieldset>
          <label className="tb-lbl">
            <span>Copy the labels of a pod (optional) · NetworkPolicies then treat it like that pod</span>
            <span className="rk-field" data-mono="true">
              <input value={labelsFrom} onChange={(e) => setLabelsFrom(e.target.value.trim())} placeholder="api-5d48wxfrd-8sd44" aria-label="Copy the labels of" />
            </span>
          </label>
          <p className="tb-hint" style={{ margin: 0 }}>
            A pod of its own in the namespace, for looking at its Services and the network from inside.{' '}
            {info?.admin && serviceAccount.trim() ? 'It carries that account’s token, and a shell in it the account’s rights.' : 'It carries no ServiceAccount token.'} It is deleted when the session ends.
          </p>
        </>
      )
      break
    case 'ssh':
      body = (
        <>
          <p className="tb-hint" style={{ margin: 0 }}>
            A terminal over SSH, as <span className="rk-mono">{pairing?.loginUser ?? '?'}</span> on port {pairing?.port ?? '?'}: the console signs you a certificate for this session alone (principal{' '}
            <span className="rk-mono">{pairing?.principal ?? info?.ssh?.principal}</span>) and checks the node's host key against what was pinned when it was paired. It works when the kubelet does not, as a node shell cannot.
          </p>
          <div className="tb-row">{durationField}</div>
          <div className="rk-callout" data-tone="err">
            <ShieldSmallIcon />
            <span>
              A shell on the node itself, as <span className="rk-mono">{pairing?.loginUser ?? 'the login user'}</span>: whatever that account may do there, you may. The session is recorded like a node shell.
            </span>
          </div>
        </>
      )
      break
    case 'ssh-pair':
      body = (
        <>
          <div className="tb-row">
            <label className="tb-field" style={{ flex: '1 1 200px' }}>
              <span className="tb-lbl">Login user</span>
              <span className="rk-field">
                <input className="rk-mono" value={loginUserV} onChange={(e) => setLoginUser(e.target.value)} placeholder={info?.ssh?.loginUser ?? 'root'} ref={(el) => void (first.current ??= el)} data-testid="tool-ssh-user" />
              </span>
              <small className="tb-hint">The OS account sessions log in as; it must exist on the node already. The pairing refuses a missing one.</small>
            </label>
            <label className="tb-field" style={{ flex: '0 1 120px' }}>
              <span className="tb-lbl">SSH port</span>
              <span className="rk-field">
                <input className="rk-mono" value={sshPortV} onChange={(e) => setSshPort(e.target.value.replace(/\D/g, ''))} inputMode="numeric" data-testid="tool-ssh-port" />
              </span>
              <small className="tb-hint">The console reaches port {info?.ssh?.port ?? 22} (toolbox.ssh.port); another needs that value changed too.</small>
            </label>
          </div>
          {nodes && (
            <p className="tb-hint" style={{ margin: 0 }} data-testid="tool-ssh-nodes">
              One card to approve, then one pairing session per node: <span className="rk-mono">{nodes.join(', ')}</span>. Every node gets the same login user and port.
            </p>
          )}
          <p className="tb-hint" style={{ margin: 0 }}>
            Runs a command on the node, as root through a short-lived pod (as <i>Run a command on the node</i> does): it writes the console's certificate authority
            {info?.ssh?.caFingerprint ? (
              <>
                {' '}
                (<span className="rk-mono">{info.ssh.caFingerprint}</span>)
              </>
            ) : null}{' '}
            to <span className="rk-mono">/etc/ssh/221b/ca.pub</span>, a principals file admitting <span className="rk-mono">{info?.ssh?.principal}</span> as the login user, and a drop-in{' '}
            <span className="rk-mono">/etc/ssh/sshd_config.d/221b.conf</span>; checks that sshd takes all that, or puts everything back and changes nothing; reloads sshd; and reads the node's host key, which every later session checks.
            Nothing personal goes on the node, and nothing long-lived: unpairing removes what this console put there, and another console's pairing of the node stays.
          </p>
          <div className="rk-callout" data-tone="err">
            <ShieldSmallIcon />
            <span>Afterwards every SuperAdmin of this console may open a shell on the node as that user. Root on the node is root over every pod that runs there, their Secrets included.</span>
          </div>
        </>
      )
      break
    case 'ssh-unpair':
      body = (
        <>
          <p className="tb-hint" style={{ margin: 0 }}>
            Runs a command on the node, as root through a short-lived pod: it removes this console's key and login user, and the drop-in <span className="rk-mono">/etc/ssh/sshd_config.d/221b.conf</span> and{' '}
            <span className="rk-mono">/etc/ssh/221b</span> when no other console is paired there, reloads sshd, and the console forgets the pairing{pairing ? ` (as ${pairing.loginUser}, since ${fmtTs(pairing.pairedAt)})` : ''}. No session
            from here can open afterwards.
          </p>
        </>
      )
      break
    case 'node-shell':
      body = (
        <>
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className="tb-lbl" style={{ marginBottom: 4 }}>
              Profile
            </legend>
            {(info?.nodeProfiles ?? []).map((p) => (
              <label key={p} className="tb-radio">
                <input type="radio" name="tb-profile" checked={profileV === p} onChange={() => setProfile(p)} ref={(el) => void (first.current ??= el)} />
                <span>
                  <b style={{ fontWeight: 500 }}>{NODE_PROFILES[p]?.title ?? p}</b>
                  <small>{NODE_PROFILES[p]?.words}</small>
                </span>
              </label>
            ))}
          </fieldset>
          <div className="tb-row">{durationField}</div>
          <div className="rk-callout" data-tone="err">
            <ShieldSmallIcon />
            <span>
              A pod in <span className="rk-mono">{info?.nodeNamespace}</span> on <span className="rk-mono">{node}</span>, with the node’s root at <span className="rk-mono">/host</span>. Root on a node is root over every pod that runs there,
              their Secrets included. Run <span className="rk-mono">chroot /host</span> for the node’s own tools.
            </span>
          </div>
        </>
      )
      break
  }

  const idleMin = Math.round((info?.idleSeconds ?? 600) / 60)
  return (
    <div className="tb-dlg-wrap">
      <button className="tb-dlg-bd" aria-label="Cancel" tabIndex={-1} onClick={onClose} />
      <div className="tb-dlg" role="dialog" aria-modal="true" aria-labelledby="tb-dlg-title" ref={dlg} tabIndex={-1} onKeyDown={trapTab} data-testid="tool-dialog">
        <div className="tb-dlg-head">
          <span className="tb-dlg-icon" data-tone={tool === 'node-shell' ? 'err' : undefined}>
            {icon}
          </span>
          <div style={{ minWidth: 0 }}>
            <h2 id="tb-dlg-title">{title}</h2>
            <span className="tb-kicker">
              {podRef ? <span className="rk-mono">{podRef.namespace}</span> : tool === 'debug-pod' ? <span className="rk-mono">{namespace}</span> : null}
              {(podRef || tool === 'debug-pod') && ' · '}
              <span className="rk-mono">{clusterName}</span>
              {info?.production && (
                <>
                  {' '}
                  <span className="rk-tag" data-tone="warn">
                    production
                  </span>
                </>
              )}
              {info?.me && (
                <>
                  {' · runs as '}
                  <span className="rk-mono">{info.me}</span>
                </>
              )}
            </span>
          </div>
          <button className="rk-icon-btn" aria-label="Close" onClick={onClose}>
            <CloseIcon />
          </button>
        </div>
        <div className="tb-dlg-body">
          {open.from === 'Sherlock' && (
            <p className="tb-hint" style={{ margin: 0 }} data-testid="tool-prefilled">
              Filled in by Sherlock. Read it and change what you need: nothing is sent until you press {verb}.
            </p>
          )}
          {loadErr && <p className="tb-err">{loadErr}</p>}
          {body}
          {askReason && (
            <label className="tb-lbl">
              <span>{needReason ? (approvers ? 'Reason · shown to the approver, kept in the audit log' : 'Reason · kept in the audit log') : 'Reason (optional) · kept in the audit log'}</span>
              <textarea className="rk-area" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="What you are looking for, and why here" aria-label="Reason" />
            </label>
          )}
          <div className="tb-facts">
            {interactive && info?.transcripts === 'pvc' && tool !== 'forward' && <span>Recorded</span>}
            {interactive && (
              <span>
                Ends after <b>{idleMin} min</b> without activity
              </span>
            )}
          </div>
          {approvers && (
            <div className="rk-callout" data-tone="info">
              <ShieldSmallIcon />
              <span>
                <b>Needs another SuperAdmin.</b> The session would open as you, with your rights, so the card appears in Watson for another SuperAdmin to approve; nothing opens until they do. It lapses after 30 minutes unanswered.
              </span>
            </div>
          )}
          {err && (
            <p className="tb-err" role="alert">
              {err}
            </p>
          )}
        </div>
        <div className="tb-dlg-foot">
          <span className="tb-hint" />
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="rk-btn" data-primary="true" onClick={send} disabled={busy || !info} aria-busy={busy} data-testid="tool-send">
            {verb}
          </button>
        </div>
      </div>
    </div>
  )
}

export type { ToolName }
