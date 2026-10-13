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
{{- $name = printf "console-%s" $name -}}
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

{{- define "b221.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- define "b221.allowedHosts" -}}
{{- $svc := include "b221.fullname" . -}}
{{- $ns := .Release.Namespace -}}
{{- $hosts := list $svc (printf "%s.%s" $svc $ns) (printf "%s.%s.svc" $svc $ns) (printf "%s.%s.svc.cluster.local" $svc $ns) -}}
{{- concat $hosts .Values.allowedHosts | uniq | join "," -}}
{{- end -}}
