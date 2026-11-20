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


{{/*
--- The ConfigMaps of before (DB-72, ADR-167) ---

A release before 0.13 kept its records in ConfigMaps this chart made. The
console copies them into the database at its first start and then deletes
each one whose record sets are all copied. Helm must never delete one
first: a ConfigMap the chart stops rendering is deleted by helm upgrade
unless the live object carries helm.sh/resource-policy keep, and 0.12
made them without it. So this release renders each one while it is in
the cluster, with keep, which the upgrade from 0.12 adds to it before the
console starts; once the console has deleted it, nothing renders it, and
Helm neither deletes it nor makes it again (a keep on the live object, or
no object, is all Helm's deletion looks at). Where the chart cannot see
the cluster (helm template, a dry run on the client, a GitOps tool that
renders with helm template) it renders every one, as 0.12 did, with keep,
so that whatever applies the output deletes none first; such a tool makes
one again, empty, after the console deleted it, and the console deletes
it again at its next start (docs/upgrading.md).

b221.clusterSeen is "true" when lookup sees the cluster, as in _roles.tpl.
b221.copySource renders one, given (list . suffix on), where on says
whether the console reads it at all (Automation's, with automation.enabled):
in the cluster it is rendered while it is there whatever on says.
*/}}
{{- define "b221.clusterSeen" -}}
{{- if lookup "v1" "Namespace" "" "kube-system" -}}true{{- end -}}
{{- end -}}

{{- define "b221.copySource" -}}
{{- $ := index . 0 -}}
{{- $name := printf "%s%s" (include "b221.fullname" $) (index . 1) -}}
{{- $there := lookup "v1" "ConfigMap" $.Release.Namespace $name -}}
{{- if or $there (and (ne (include "b221.clusterSeen" $) "true") (index . 2)) }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $name }}
  namespace: {{ $.Release.Namespace }}
  labels:
    {{- include "b221.labels" $ | nindent 4 }}
  annotations:
    # Kept by helm upgrade until the console has copied it into the
    # database and deleted it (DB-72).
    helm.sh/resource-policy: keep
{{- end }}
{{- end -}}

{{/* --- The database (Phase 13) --- */}}

{{/* bundled or external (DB-69); bundled when nothing says. Values kept
     from a release before 0.13 have no block, which validateDatabase
     refuses. */}}
{{- define "b221.dbMode" -}}
{{- (.Values.database | default dict).mode | default "bundled" -}}
{{- end -}}

{{/* The bundled database's objects are named after this, and its
     read-write Service has this name. */}}
{{- define "b221.dbName" -}}
{{- printf "%s-db" (include "b221.fullname" . | trunc 56 | trimSuffix "-") -}}
{{- end -}}

{{/* The database pods carry a name of their own, so the console's
     selectors (its Service, its network policy) never match them. */}}
{{- define "b221.dbSelectorLabels" -}}
app.kubernetes.io/name: "221b-db"
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "b221.dbLabels" -}}
{{ include "b221.dbSelectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{- define "b221.dbImage" -}}
{{- $i := .Values.database.bundled.image -}}
{{- if $i.digest -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (default .Chart.AppVersion $i.tag) -}}
{{- end -}}
{{- end -}}

{{/* The Secret with the bundled database's passwords. */}}
{{- define "b221.dbSecret" -}}
{{- .Values.database.bundled.existingSecret | default (include "b221.dbName" .) -}}
{{- end -}}

{{- define "b221.dbTLSSecret" -}}
{{- .Values.database.bundled.tls.secretName | default (printf "%s-tls" (include "b221.dbName" .)) -}}
{{- end -}}

{{/* What cannot work is refused when the chart is rendered. */}}
{{- define "b221.validateDatabase" -}}
{{- $mode := include "b221.dbMode" . -}}
{{- if not (and .Values.database .Values.database.bundled .Values.database.external) -}}
{{- fail "the values have no database block: they were kept from a release before 0.13 (helm upgrade --reuse-values), which had no database. The console now keeps its records in PostgreSQL and does not run without it: upgrade with --reset-then-reuse-values, or with your values file, so the chart's defaults (database.mode=bundled) apply (docs/upgrading.md)" -}}
{{- end -}}
{{- if eq $mode "none" -}}
{{- fail "database.mode=none was removed in 0.13.0: the console keeps its records in PostgreSQL and does not run without it. Remove database.mode to get the bundled database (the default), or set database.mode=external with database.external.host for one you run; the records in the ConfigMaps are copied in at the first start (docs/upgrading.md)" -}}
{{- end -}}
{{- if not (has $mode (list "bundled" "external")) -}}
{{- fail (printf "database.mode must be bundled or external, not %q" $mode) -}}
{{- end -}}
{{- if eq $mode "external" -}}
{{- $e := .Values.database.external -}}
{{- if not $e.host -}}
{{- fail "database.mode=external needs database.external.host, the database's read-write address" -}}
{{- end -}}
{{- if not (has $e.sslMode (list "verify-full" "verify-ca" "require" "disable")) -}}
{{- fail (printf "database.external.sslMode must be verify-full, verify-ca or require, not %q" (toString $e.sslMode)) -}}
{{- end -}}
{{- $iam := default "none" $e.iamAuth -}}
{{- if not (has $iam (list "none" "aws" "gcp")) -}}
{{- fail (printf "database.external.iamAuth must be none, aws or gcp, not %q" (toString $iam)) -}}
{{- end -}}
{{- if ne $iam "none" -}}
{{- if $e.passwordSecret.name -}}
{{- fail (printf "database.external.iamAuth=%s signs in with a token in place of the password: remove database.external.passwordSecret.name" $iam) -}}
{{- end -}}
{{- if eq $e.sslMode "disable" -}}
{{- fail (printf "database.external.iamAuth=%s sends its token over TLS only: set database.external.sslMode to verify-full, verify-ca or require" $iam) -}}
{{- end -}}
{{- if and (eq $iam "aws") (not (($e.aws).region)) -}}
{{- fail "database.external.iamAuth=aws needs database.external.aws.region, the RDS database's region" -}}
{{- end -}}
{{- end -}}
{{- if and (eq $e.sslMode "disable") (not $e.allowInsecure) -}}
{{- fail "database.external.sslMode=disable sends the password and every record in the clear; set database.external.allowInsecure=true if that is meant" -}}
{{- end -}}
{{- if lt (int $e.maxConnections) (mul 2 (int .Values.replicaCount)) -}}
{{- fail (printf "database.external.maxConnections=%d leaves a replica fewer than 2 connections; give %d or more" (int $e.maxConnections) (mul 2 (int .Values.replicaCount))) -}}
{{- end -}}
{{- end -}}
{{- if eq $mode "bundled" -}}
{{- $b := .Values.database.bundled -}}
{{- if not (has $b.synchronous (list "quorum" "off")) -}}
{{- fail (printf "database.bundled.synchronous must be quorum or off, not %q" (toString $b.synchronous)) -}}
{{- end -}}
{{- if not (regexMatch "^([01][0-9]|2[0-3]):[0-5][0-9]$" (toString $b.backup.dailyAt)) -}}
{{- fail (printf "database.bundled.backup.dailyAt is a time of day in UTC such as \"02:00\", not %q" (toString $b.backup.dailyAt)) -}}
{{- end -}}
{{- if and $b.restore.fromBackup (not $b.backup.enabled) -}}
{{- fail "database.bundled.restore.fromBackup needs database.bundled.backup.enabled, which says where the archive is" -}}
{{- end -}}
{{- if and (not $b.ha) (not $b.acknowledgeNoHA) -}}
{{- $ns := lookup "v1" "Namespace" "" .Release.Namespace -}}
{{- if $ns -}}
{{- if eq (get ($ns.metadata.labels | default dict) "221b.io/environment") "production" -}}
{{- fail (printf "the namespace %s is labelled 221b.io/environment=production and database.bundled.ha is off: one database member means the console's records are away whenever its node is. Set database.bundled.ha=true, or database.bundled.acknowledgeNoHA=true if one member is meant" .Release.Namespace) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* An installed release keeps its mode: bundled to external, or back, is
a migration. The Deployment says which it has. After a rollback to a
revision from before the database it says nothing, so the bundled
database's Secret, which is kept through a rollback as its volumes are,
is asked too. A release that has no database (a 0.12 release, whose
Deployment has no annotation, or one of mode none, which 0.13 builds
before DB-69 had) gets one: bundled, the default, with nothing to ask, or
external, unless a bundled database's Secret says one was there. */ -}}
{{- $was := "" -}}
{{- $d := lookup "apps/v1" "Deployment" .Release.Namespace (include "b221.fullname" .) -}}
{{- if $d -}}
{{- $was = get ($d.metadata.annotations | default dict) "221b.io/database-mode" | default "" -}}
{{- end -}}
{{- if and (or (not $was) (eq $was "none")) (ne $mode "bundled") (not .Values.database.allowModeChange) -}}
{{- if lookup "v1" "Secret" .Release.Namespace (include "b221.dbSecret" .) -}}
{{- fail (printf "this release has a bundled database from an earlier revision (its Secret %s and its volumes are kept), and database.mode is now %s, which leaves the records in it behind. Set database.mode=bundled to go on with it. If it is no longer wanted, delete that Secret, the Secret %s, the volume claims of %s-db and the ConfigMaps %s-db-config, -db-leader, -db-failover, -db-sync and -db-failsafe that Patroni made, or set database.allowModeChange=true (docs/database.md)" (include "b221.dbSecret" .) $mode (include "b221.dbTLSSecret" .) (include "b221.fullname" .) (include "b221.fullname" .)) -}}
{{- end -}}
{{- end -}}
{{- if and $was (ne $was "none") (ne $was $mode) (not .Values.database.allowModeChange) -}}
{{- fail (printf "this release keeps its records in a %s database and database.mode is now %s. The records do not move by themselves: take a backup in Administration › Backup, install with the new mode, restore it there (docs/database.md), and set database.allowModeChange=true for the upgrade that switches" $was $mode) -}}
{{- end -}}
{{- end -}}
