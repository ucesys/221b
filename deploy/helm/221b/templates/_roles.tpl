{{/*
The four roles, who holds them, and what the console may assert (Phase 6.1).
Everything a person can be bound to is written in values and rendered here;
nothing in the console or in Watson creates a binding.
*/}}

{{- define "b221.roleLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
221b/managed: "true"
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{/* A name safe to put inside the name of an object. */}}
{{- define "b221.slug" -}}
{{- regexReplaceAll "[^a-z0-9.-]+" (lower .) "-" | trunc 40 | trimAll "-." -}}
{{- end -}}

{{/* Whether a role entry (a map) names a group: renders "true" or nothing. */}}
{{- define "b221.hasGroup" -}}
{{- if and (kindIs "map" .) .group (kindIs "string" .group) -}}true{{- end -}}
{{- end -}}

{{/*
The role lists of the sign-in method in use, as a map of role to a list of
entries { group, namespaces }: auth.ldap.roles or auth.oidc.roles.
*/}}
{{- define "b221.roleLists" -}}
{{- if eq .Values.auth.mode "ldap" -}}
{{- toJson .Values.auth.ldap.roles -}}
{{- else if eq .Values.auth.mode "oidc" -}}
{{- toJson .Values.auth.oidc.roles -}}
{{- else -}}
{}
{{- end -}}
{{- end -}}

{{/* The prefix put before the groups of the sign-in method in use. */}}
{{- define "b221.listPrefix" -}}
{{- if eq .Values.auth.mode "ldap" -}}{{ .Values.auth.prefixes.ldap }}{{- else -}}{{ .Values.auth.kubernetes.groupPrefix }}{{- end -}}
{{- end -}}

{{/*
Checks everything in values that decides who can do what, and fails with the
name of what is wrong. Renders nothing.
*/}}
{{- define "b221.validate" -}}
{{- $v := .Values -}}
{{- if not (has $v.auth.mode (list "none" "local" "oidc" "ldap")) -}}
{{- fail "auth.mode must be oidc, ldap, local or none" -}}
{{- end -}}
{{- range $r := list "admin" "superAdmin" -}}
{{- $t := (index $v.rbac.roles $r).template -}}
{{- if not (has $t (list "full" "readOnly")) -}}
{{- fail (printf "rbac.roles.%s.template must be full or readOnly, not %q" $r (toString $t)) -}}
{{- end -}}
{{- end -}}
{{- if not (has $v.rbac.enforce (list "off" "strict")) -}}
{{- fail (printf "rbac.enforce must be off or strict, not %q" (toString $v.rbac.enforce)) -}}
{{- end -}}
{{- $readOnly := and (eq $v.rbac.roles.admin.template "readOnly") (eq $v.rbac.roles.superAdmin.template "readOnly") -}}
{{- if and $readOnly (eq $v.localCluster.mode "write") -}}
{{- fail "rbac.roles.admin.template and rbac.roles.superAdmin.template are both readOnly, which means nobody may change anything through the console, and localCluster.mode is write, which allows changes. Set localCluster.mode to read, or use the full definition of a role." -}}
{{- end -}}
{{- if and (eq $v.auth.mode "none") (eq $v.localCluster.mode "write") (or (eq $v.rbac.roles.admin.template "readOnly") (eq $v.rbac.roles.superAdmin.template "readOnly")) -}}
{{- fail "With sign-in off (auth.mode none) and localCluster.mode write, Watson's changes run as the console itself, which no readOnly definition of a role limits. Turn sign-in on, or use the full definitions, or set localCluster.mode to read." -}}
{{- end -}}
{{- /* Prefixes. */ -}}
{{- range $p := list $v.auth.prefixes.local $v.auth.prefixes.ldap -}}
{{- if not (regexMatch "^[a-z0-9][a-z0-9._-]*:$" $p) -}}
{{- fail (printf "auth.prefixes must be lowercase and end with a colon, such as 221b-local: (not %q)" $p) -}}
{{- end -}}
{{- end -}}
{{- if eq $v.auth.prefixes.local $v.auth.prefixes.ldap -}}
{{- fail "auth.prefixes.local and auth.prefixes.ldap must differ, so a local and a directory user of the same name are different subjects" -}}
{{- end -}}
{{- /* Local groups. */ -}}
{{- $names := dict -}}
{{- range $g := $v.auth.local.groups -}}
{{- if not (and (kindIs "map" $g) $g.name) -}}
{{- fail "every entry of auth.local.groups needs a name" -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9][a-z0-9._-]*$" $g.name) -}}
{{- fail (printf "auth.local.groups: the name %q must use lowercase letters, digits, '.', '-' and '_'" $g.name) -}}
{{- end -}}
{{- if hasKey $names $g.name -}}
{{- fail (printf "auth.local.groups: %q is defined twice" $g.name) -}}
{{- end -}}
{{- $_ := set $names $g.name true -}}
{{- if not (has $g.role (list "viewer" "admin")) -}}
{{- fail (printf "auth.local.groups: %q has role %q; a local group is viewer or admin (SuperAdmin and SuperViewer are given to a user)" $g.name (toString $g.role)) -}}
{{- end -}}
{{- if not $g.namespaces -}}
{{- fail (printf "auth.local.groups: %q needs at least one namespace" $g.name) -}}
{{- end -}}
{{- end -}}
{{- /* Local users. */ -}}
{{- range $u := $v.auth.local.users -}}
{{- $who := coalesce $u.username $u.email "an account" -}}
{{- if not (or $u.username $u.email) -}}
{{- fail "every entry of auth.local.users needs a username or an email" -}}
{{- end -}}
{{- if not $u.passwordHash -}}
{{- fail (printf "auth.local.users: %s needs a passwordHash (make one with the hash-password command)" $who) -}}
{{- end -}}
{{- range $g := ($u.groups | default list) -}}
{{- if not (hasKey $names $g) -}}
{{- fail (printf "auth.local.users: %s is in the group %q, which auth.local.groups does not define" $who $g) -}}
{{- end -}}
{{- end -}}
{{- range $r := ($u.roles | default list) -}}
{{- if not (has $r (list "superAdmin" "superViewer")) -}}
{{- fail (printf "auth.local.users: %s has the role %q; a user holds superAdmin or superViewer, and Admin and Viewer come from groups" $who $r) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* Directory and provider role lists. */ -}}
{{- $lists := include "b221.roleLists" . | fromJson -}}
{{- range $role, $entries := $lists -}}
{{- if not (has $role (list "viewer" "admin" "superViewer" "superAdmin")) -}}
{{- fail (printf "auth.%s.roles: %q is not a role; use viewer, admin, superViewer or superAdmin" $v.auth.mode $role) -}}
{{- end -}}
{{- range $e := $entries -}}
{{- if not (include "b221.hasGroup" $e) -}}
{{- fail (printf "auth.%s.roles.%s: every entry needs a group" $v.auth.mode $role) -}}
{{- end -}}
{{- if and (has $role (list "superViewer" "superAdmin")) $e.namespaces -}}
{{- fail (printf "auth.%s.roles.%s: %s applies in every namespace, so %q cannot take namespaces" $v.auth.mode $role $role $e.group) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* Names no source may use. */ -}}
{{- $reserved := list -}}
{{- range $g := $v.auth.local.groups -}}{{- $reserved = append $reserved $g.name -}}{{- end -}}
{{- range $role, $entries := $lists -}}{{- range $e := $entries -}}{{- $reserved = append $reserved $e.group -}}{{- end -}}{{- end -}}
{{- range $n := $reserved -}}
{{- if or (hasPrefix "system:" $n) (hasPrefix "221b:" $n) -}}
{{- fail (printf "the group name %q is reserved: names that start with system: or 221b: are never accepted" $n) -}}
{{- end -}}
{{- end -}}
{{- /* LDAP. */ -}}
{{- if eq $v.auth.mode "ldap" -}}
{{- $l := $v.auth.ldap -}}
{{- if not $l.url -}}{{- fail "LDAP sign-in needs auth.ldap.url, such as ldaps://ldap.corp.example:636" -}}{{- end -}}
{{- if not (or (hasPrefix "ldaps://" $l.url) (hasPrefix "ldap://" $l.url)) -}}
{{- fail (printf "auth.ldap.url must start with ldaps:// or ldap://, not %q" $l.url) -}}
{{- end -}}
{{- if and (hasPrefix "ldap://" $l.url) (not $l.startTLS) -}}
{{- fail "auth.ldap.url is ldap:// without startTLS: passwords would cross the network in the clear. Use ldaps://, or set auth.ldap.startTLS to true." -}}
{{- end -}}
{{- if not $l.bindDN -}}{{- fail "LDAP sign-in needs auth.ldap.bindDN, the search account" -}}{{- end -}}
{{- if not $l.existingSecret -}}{{- fail "LDAP sign-in needs auth.ldap.existingSecret, naming a Secret with the search account's password under the key bind-password" -}}{{- end -}}
{{- if not $l.userSearch.baseDN -}}{{- fail "LDAP sign-in needs auth.ldap.userSearch.baseDN" -}}{{- end -}}
{{- if not $l.groupSearch.baseDN -}}{{- fail "LDAP sign-in needs auth.ldap.groupSearch.baseDN" -}}{{- end -}}
{{- if not (contains "{username}" $l.userSearch.filter) -}}{{- fail "auth.ldap.userSearch.filter must contain {username}" -}}{{- end -}}
{{- if not (contains "{dn}" $l.groupSearch.filter) -}}{{- fail "auth.ldap.groupSearch.filter must contain {dn}" -}}{{- end -}}
{{- if not $lists -}}{{- fail "LDAP sign-in needs auth.ldap.roles: only members of a listed group may sign in" -}}{{- end -}}
{{- end -}}
{{- /* A namespace named in values must exist; checked on a live cluster only. */ -}}
{{- if lookup "v1" "Namespace" "" "kube-system" -}}
{{- range $s := (include "b221.bindingSpecs" . | fromJsonArray) -}}
{{- if not (lookup "v1" "Namespace" "" $s.namespace) -}}
{{- fail (printf "the namespace %q, named for the group %s, does not exist" $s.namespace $s.group) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Every RoleBinding to render, as a JSON list: the role, the group as
Kubernetes sees it, the namespace and the object's name.
*/}}
{{- define "b221.bindingSpecs" -}}
{{- $specs := dict -}}
{{- if or (eq .Values.auth.mode "local") (eq .Values.auth.mode "ldap") -}}
{{- range $g := .Values.auth.local.groups -}}
{{- range $ns := $g.namespaces -}}
{{- $subject := printf "%s%s" $.Values.auth.prefixes.local $g.name -}}
{{- $name := printf "221b-%s-%s-%s" $g.role (include "b221.slug" (printf "local-%s" $g.name)) (printf "%s/%s/%s" $g.role $subject $ns | sha256sum | trunc 8) -}}
{{- $_ := set $specs $name (dict "name" $name "role" $g.role "subject" $subject "group" $g.name "namespace" $ns) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $lists := include "b221.roleLists" . | fromJson -}}
{{- $prefix := include "b221.listPrefix" . -}}
{{- range $role, $entries := $lists -}}
{{- if has $role (list "viewer" "admin") -}}
{{- range $e := $entries -}}
{{- range $ns := ($e.namespaces | default list) -}}
{{- $subject := printf "%s%s" $prefix $e.group -}}
{{- $name := printf "221b-%s-%s-%s" $role (include "b221.slug" (printf "%s-%s" $.Values.auth.mode $e.group)) (printf "%s/%s/%s" $role $subject $ns | sha256sum | trunc 8) -}}
{{- $_ := set $specs $name (dict "name" $name "role" $role "subject" $subject "group" $e.group "namespace" $ns) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $out := list -}}
{{- range $name := (keys $specs | sortAlpha) -}}
{{- $out = append $out (get $specs $name) -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/*
Every group the console may assert for a person: the reserved ones, and the
listed ones with the prefix of their source, as a JSON list.
*/}}
{{- define "b221.assertedGroups" -}}
{{- $g := list "221b:console-users" "221b:superadmins" "221b:superviewers" "system:authenticated" -}}
{{- if or (eq .Values.auth.mode "local") (eq .Values.auth.mode "ldap") -}}
{{- range $x := .Values.auth.local.groups -}}{{- $g = append $g (printf "%s%s" $.Values.auth.prefixes.local $x.name) -}}{{- end -}}
{{- end -}}
{{- $lists := include "b221.roleLists" . | fromJson -}}
{{- $prefix := include "b221.listPrefix" . -}}
{{- range $role, $entries := $lists -}}
{{- range $e := $entries -}}{{- $g = append $g (printf "%s%s" $prefix $e.group) -}}{{- end -}}
{{- end -}}
{{- toJson ($g | uniq | sortAlpha) -}}
{{- end -}}

{{/*
Whether the console's right to impersonate groups can be limited to the
listed ones. OIDC with no role lists keeps every group of the token, as
before, and so cannot be. Renders "true" or nothing.
*/}}
{{- define "b221.groupsLimited" -}}
{{- if or (eq .Values.auth.mode "local") (eq .Values.auth.mode "ldap") (and (eq .Values.auth.mode "oidc") .Values.auth.oidc.roles) -}}true{{- end -}}
{{- end -}}

{{/* The file of roles, groups and the directory the console reads (JSON). */}}
{{- define "b221.authConfig" -}}
{{- $v := .Values -}}
{{- $lists := dict -}}
{{- range $role, $entries := (include "b221.roleLists" . | fromJson) -}}
{{- $names := list -}}
{{- range $e := $entries -}}{{- $names = append $names $e.group -}}{{- end -}}
{{- $_ := set $lists $role $names -}}
{{- end -}}
{{- $groups := list -}}
{{- if or (eq $v.auth.mode "local") (eq $v.auth.mode "ldap") -}}
{{- range $g := $v.auth.local.groups -}}
{{- $groups = append $groups (dict "name" $g.name "role" $g.role "namespaces" $g.namespaces) -}}
{{- end -}}
{{- end -}}
{{- $cfg := dict "lists" $lists "localGroups" $groups "rbacEnforce" $v.rbac.enforce -}}
{{- $_ := set $cfg "templates" (dict "admin" $v.rbac.roles.admin.template "superAdmin" $v.rbac.roles.superAdmin.template) -}}
{{- $_ := set $cfg "prefixes" (dict "local" $v.auth.prefixes.local "ldap" $v.auth.prefixes.ldap) -}}
{{- /* The RoleBindings this chart renders, so the console can write the same YAML for a cluster the chart is not installed in. */ -}}
{{- $_ := set $cfg "bindings" (include "b221.bindingSpecs" . | fromJsonArray) -}}
{{- if eq $v.auth.mode "ldap" -}}
{{- $l := $v.auth.ldap -}}
{{- $d := dict "url" $l.url "startTLS" (default false $l.startTLS) "bindDN" $l.bindDN "pageSize" ($l.pageSize | default 500 | int) "syncInterval" ($l.syncInterval | default "1h") -}}
{{- if $l.caSecret -}}{{- $_ := set $d "caFile" (printf "/etc/221b/ldap-ca/%s" $l.caSecretKey) -}}{{- end -}}
{{- $_ := set $d "userSearch" (dict "base" $l.userSearch.baseDN "filter" (replace "{username}" "%s" $l.userSearch.filter) "usernameAttr" $l.userSearch.usernameAttr "emailAttr" $l.userSearch.emailAttr "nameAttr" $l.userSearch.nameAttr) -}}
{{- $_ := set $d "groupSearch" (dict "base" $l.groupSearch.baseDN "filter" (replace "{dn}" "%s" $l.groupSearch.filter) "nameAttr" $l.groupSearch.nameAttr "nested" (ne (toString $l.groupSearch.nested) "false")) -}}
{{- $_ := set $cfg "ldap" $d -}}
{{- end -}}
{{- toPrettyJson $cfg -}}
{{- end -}}

{{/* The port of the directory, for the network policy. */}}
{{- define "b221.ldapPort" -}}
{{- $u := .Values.auth.ldap.url -}}
{{- $m := regexFind ":[0-9]+$" (trimSuffix "/" $u) -}}
{{- if $m -}}{{ trimPrefix ":" $m }}{{- else if hasPrefix "ldaps://" $u -}}636{{- else -}}389{{- end -}}
{{- end -}}

{{/* Whether a file of local accounts is mounted: always with local sign-in,
     and with LDAP when break-glass accounts are given. "true" or nothing. */}}
{{- define "b221.localUsers" -}}
{{- if or (eq .Values.auth.mode "local") (and (eq .Values.auth.mode "ldap") (or .Values.auth.local.users .Values.auth.local.existingSecret)) -}}true{{- end -}}
{{- end -}}
