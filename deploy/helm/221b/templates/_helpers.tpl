{{/* Resource names must start with a letter for Services, so a release
     named after the chart still gets a valid name. */}}
{{- define "b221.fullname" -}}
{{- $name := .Values.fullnameOverride -}}
{{- if not $name -}}
{{- $name = .Release.Name -}}
{{- if not (contains .Chart.Name $name) -}}
{{- $name = printf "%s-%s" $name .Chart.Name -}}
{{- end -}}
{{- end -}}
{{- if regexMatch "^[0-9]" $name -}}
{{- $name = printf "k8s-console-%s" $name -}}
{{- end -}}
{{- $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Cluster-scoped objects carry the namespace too, so two releases with
     the same name in different namespaces do not collide. */}}
{{- define "b221.clusterScopedName" -}}
{{- printf "%s-%s" .Release.Namespace (include "b221.fullname" .) | trunc 253 | trimSuffix "-" -}}
{{- end -}}

{{- define "b221.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "b221.labels" -}}
{{ include "b221.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{- define "b221.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "b221.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* The image: a digest pins one build whatever its tag points at. */}}
{{- define "b221.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{/* A moving tag (main, pr-7, latest, ci) is republished under the same
     name with every build; a release (0.1.0, v0.1.0), a sha- tag or a
     digest names one build for good. Renders "true" or nothing. */}}
{{- define "b221.movingTag" -}}
{{- $tag := toString (default .Chart.AppVersion .Values.image.tag) -}}
{{- if and (not .Values.image.digest) (not (regexMatch "^(v?[0-9]+\\.[0-9]+\\.[0-9]+([-+].*)?|sha-[0-9a-f]{7,})$" $tag)) -}}
true
{{- end -}}
{{- end -}}

{{/* Always for a moving tag, so every pod start fetches the current
     build; IfNotPresent for a build that cannot change under its name.
     An explicit image.pullPolicy wins. */}}
{{- define "b221.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if include "b221.movingTag" . -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}

{{- define "b221.allowedHosts" -}}
{{- $svc := include "b221.fullname" . -}}
{{- $ns := .Release.Namespace -}}
{{- $hosts := list $svc (printf "%s.%s" $svc $ns) (printf "%s.%s.svc" $svc $ns) (printf "%s.%s.svc.cluster.local" $svc $ns) -}}
{{- if and .Values.ingress.enabled .Values.ingress.host -}}
{{- $hosts = concat $hosts (list .Values.ingress.host) -}}
{{- end -}}
{{- concat $hosts .Values.allowedHosts | uniq | join "," -}}
{{- end -}}

{{/* Whether the console must reach something besides the API server: an
     OIDC provider, an audit webhook, the Claude API, or what
     networkPolicy.outboundCIDRs names, such as a Helm application's git
     host. Renders "true" or nothing. */}}
{{- define "b221.outbound" -}}
{{- if or (eq .Values.auth.mode "oidc") .Values.audit.webhook.url .Values.sherlock.egress .Values.networkPolicy.outboundCIDRs -}}
true
{{- end -}}
{{- end -}}

{{/* The Secret that holds the sign-in credentials. */}}
{{- define "b221.authSecret" -}}
{{- printf "%s-auth" (include "b221.fullname" .) -}}
{{- end -}}

{{/* Whether node shells are on: the values say so and a full SuperAdmin
can create their pods (TB-49). */}}
{{- /* The namespace node shells run in: toolbox.nodeShell.namespace, or
<release namespace>-privileged (what it is: the one namespace of the
console's that runs at Pod Security privileged), so two consoles in one
cluster do not fight over it (Phase 10 review). */ -}}
{{- define "b221.toolboxNamespace" -}}
{{- .Values.toolbox.nodeShell.namespace | default (printf "%s-privileged" .Release.Namespace) -}}
{{- end -}}

{{- define "b221.nodeShellOn" -}}
{{- $t := .Values.toolbox -}}
{{- if and $t.enabled $t.nodeShell.enabled (eq .Values.rbac.roles.superAdmin.template "full") -}}true{{- end -}}
{{- end -}}

{{/* SSH is root on a node like a node shell, and Kubernetes never sees it,
so a read-only SuperAdmin definition turns it off too. Values kept by
--reuse-values from an earlier release have no toolbox.ssh. */}}
{{- define "b221.sshOn" -}}
{{- $t := .Values.toolbox -}}
{{- $ssh := $t.ssh | default dict -}}
{{- if and $t.enabled $ssh.enabled (eq .Values.rbac.roles.superAdmin.template "full") -}}true{{- end -}}
{{- end -}}

{{/* The toolbox values as the console reads them (TOOLBOX_CONFIG_FILE). */}}
{{- define "b221.toolboxConfig" -}}
{{- $t := .Values.toolbox -}}
{{- $c := omit $t "transcriptsVolume" -}}
{{- $node := deepCopy $t.nodeShell -}}
{{- $_ := set $node "enabled" (include "b221.nodeShellOn" . | eq "true") -}}
{{- $_ := set $node "namespace" (include "b221.toolboxNamespace" .) -}}
{{- $_ := set $c "nodeShell" $node -}}
{{- $ssh := deepCopy ($t.ssh | default dict) -}}
{{- $_ := set $ssh "enabled" (include "b221.sshOn" . | eq "true") -}}
{{- $_ := set $c "ssh" $ssh -}}
{{- if eq $t.transcripts "pvc" -}}{{- $_ := set $c "transcriptDir" "/var/lib/221b/transcripts" -}}{{- end -}}
{{- $_ := set $c "adminExec" (and (eq .Values.rbac.roles.admin.template "full") (.Values.rbac.roles.admin.exec | default false)) -}}
{{- toPrettyJson $c -}}
{{- end -}}

{{/* Whether the issue service is on (Phase 12): "true" or nothing. A values
     file from before it has no issues block: on. */}}
{{- define "b221.issuesOn" -}}
{{- if ne (toString (dig "enabled" true (.Values.issues | default dict))) "false" -}}true{{- end -}}
{{- end -}}

{{/*
The agents' baseline house instructions (Phase 12), as the data of the
<release>-agents ConfigMap: <agent>.md for each agent given some. A values
file from before has no agents block; a key that is no agent is refused.
*/}}
{{- define "b221.agentBaselines" -}}
{{- $ids := list "sherlock" "troubleshooter" "changePlanner" "auditor" "hostDoctor" "automationAuthor" "scriptReviewer" "mycroft" "summarizer" }}
{{- $agents := .Values.agents | default dict }}
{{- range $k, $_ := $agents }}
{{- if not (has $k $ids) }}
{{- fail (printf "agents.%s is not an agent: %s" $k (join ", " $ids)) }}
{{- end }}
{{- end }}
{{- range $id := $ids }}
{{- $a := get $agents $id | default dict }}
{{- with $a.instructions }}
{{ $id }}.md: {{ . | quote }}
{{- end }}
{{- end }}
{{- end }}
