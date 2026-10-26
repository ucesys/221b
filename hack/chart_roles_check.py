#!/usr/bin/env python3
"""Checks what the chart renders for the four roles (Phase 6.1, RB-01 to
RB-14, RB-37, RB-50, RB-52): the rules are the definitions the console ships
(deploy/helm/221b/files/roles.json), the bindings come from values and name
groups only, and the console's own account has no right on RBAC.

usage: chart_roles_check.py WORKDIR   (the files hack/check-chart.sh renders)
"""
import json
import os
import sys

import yaml

WORK = sys.argv[1]
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHIPPED = json.load(open(os.path.join(ROOT, "deploy/helm/221b/files/roles.json")))
FAILED = []


def docs(name):
    with open(os.path.join(WORK, name)) as f:
        return [d for d in yaml.safe_load_all(f) if d]


def of(ds, kind, name=None):
    return [d for d in ds if d["kind"] == kind and (name is None or d["metadata"]["name"] == name)]


def check(what, ok, detail=""):
    print(("ok: " if ok else "FAIL: ") + what + ("" if ok else (" :: " + str(detail))))
    if not ok:
        FAILED.append(what)


def role_rules(key, template, secrets):
    t = SHIPPED["roles"][key]["templates"][template]
    return t["rules"] + (t.get("secretRules", []) if secrets else [])


def writes(rules):
    return [r for r in rules if set(r["verbs"]) - {"get", "list", "watch"}]


def console_role(ds):
    return [d for d in of(ds, "ClusterRole") if not d["metadata"]["name"].startswith("221b-")][0]


def settings_bindings(ds):
    return [d for d in of(ds, "RoleBinding") if not d["metadata"]["name"].endswith("-settings")]


# ---- the defaults: full definitions, Secrets on
d = docs("chart-default.yaml")
by = {x["metadata"]["name"]: x for x in of(d, "ClusterRole")}
for key, role in SHIPPED["roles"].items():
    template = "full" if key in ("admin", "superAdmin") else "readOnly"
    got = by.get(role["clusterRole"])
    check(f"{role['clusterRole']} is installed with the shipped {template} definition",
          got is not None and got["rules"] == role_rules(key, template, True))
    check(f"{role['clusterRole']} is labelled 221b/managed",
          got is not None and got["metadata"]["labels"].get("221b/managed") == "true")
check("the nodes role is installed and bound to the group 221b:console-users",
      by.get("221b-nodes", {}).get("rules") == SHIPPED["nodes"]["rules"] and
      of(d, "ClusterRoleBinding", "221b-nodes")[0]["subjects"][0]["name"] == "221b:console-users")
check("the super roles are bound to their reserved groups only",
      [of(d, "ClusterRoleBinding", "221b-super-admin")[0]["subjects"][0]["name"],
       of(d, "ClusterRoleBinding", "221b-super-viewer")[0]["subjects"][0]["name"]] == ["221b:superadmins", "221b:superviewers"])
check("no namespaced binding without groups in values", settings_bindings(d) == [])
check("no role gets a verb on roles or bindings, whatever the template",
      not [r for k in ("admin", "viewer", "superViewer") for r in by[SHIPPED['roles'][k]['clusterRole']]["rules"]
           if "rbac.authorization.k8s.io" in r.get("apiGroups", []) and set(r["verbs"]) - {"get", "list", "watch"}])
check("the shipped Admin has no pods/exec and nothing cluster-scoped",
      not [r for r in by["221b-admin"]["rules"] if "pods/exec" in r.get("resources", []) or "nodes" in r.get("resources", [])
           and set(r["verbs"]) - {"get", "list", "watch"}])

# ---- the console's own account (RB-14, RB-37, RB-52)
c = console_role(d)
rbac_writes = [r for r in c["rules"] if "rbac.authorization.k8s.io" in r.get("apiGroups", []) and set(r["verbs"]) - {"get", "list", "watch"}]
check("the console's ClusterRole has no verb on roles or bindings", not rbac_writes, rbac_writes)
check("it holds neither bind, escalate nor impersonate on serviceaccounts or uids",
      not [r for r in c["rules"] if set(r["verbs"]) & {"bind", "escalate"} or
           (set(r.get("resources", [])) & {"serviceaccounts", "uids"} and "impersonate" in r["verbs"])])
groups = [r for r in c["rules"] if r.get("resources") == ["groups"]][0]
check("with local sign-in it may impersonate only the reserved groups and the listed ones",
      set(groups.get("resourceNames", [])) == {"221b:console-users", "221b:superadmins", "221b:superviewers", "system:authenticated"} and
      "system:masters" not in groups.get("resourceNames", []), groups)
users = [r for r in c["rules"] if r.get("resources") == ["users"]][0]
check("any user name may be asserted: names are dynamic", "resourceNames" not in users)

# ---- without Secrets
by = {x["metadata"]["name"]: x for x in of(docs("chart-nosecrets.yaml"), "ClusterRole")}
check("secrets.read=false removes every rule on Secrets from the four roles except SuperAdmin's wildcard",
      all(by[SHIPPED["roles"][k]["clusterRole"]]["rules"] == role_rules(k, "full" if k in ("admin", "superAdmin") else "readOnly", False)
          for k in SHIPPED["roles"]) and
      not [r for k in ("admin", "viewer", "superViewer") for r in by[SHIPPED["roles"][k]["clusterRole"]]["rules"] if "secrets" in r.get("resources", [])])

# ---- both definitions read-only: the fully protected deployment (RB-03)
d = docs("chart-protected.yaml")
by = {x["metadata"]["name"]: x for x in of(d, "ClusterRole")}
for key in ("admin", "superAdmin"):
    role = by[SHIPPED["roles"][key]["clusterRole"]]
    check(f"{key} readOnly has no write verb and keeps its name", not writes(role["rules"]) and role["rules"] == role_rules(key, "readOnly", True), writes(role["rules"]))
check("the read-only SuperAdmin may only get non-resource URLs",
      all(set(r["verbs"]) <= {"get", "list", "watch"} for r in by["221b-super-admin"]["rules"]))
# get on nodes/proxy or pods/exec runs a command through a kubelet or the
# API server: no get on "*" in the core group, and none of them by name.
RUNS = {"nodes/proxy", "pods/exec", "pods/attach", "pods/portforward", "pods/proxy", "services/proxy"}
check("the read-only SuperAdmin cannot get what runs a command",
      not [r for r in by["221b-super-admin"]["rules"] if "get" in r["verbs"] and
           ({"", "*"} & set(r.get("apiGroups", []))) and ("*" in r.get("resources", []) or RUNS & set(r.get("resources", [])))])
cm = [x for x in of(d, "ConfigMap") if x["metadata"]["name"].endswith("-auth-config")][0]
check("it tells the console which definitions are in use",
      json.loads(cm["data"]["auth.json"])["templates"] == {"admin": "readOnly", "superAdmin": "readOnly", "adminExec": False})

# ---- local groups and users (RB-06, RB-50)
d = docs("chart-groups.yaml")
rbs = settings_bindings(d)
check("a local group yields one RoleBinding per namespace",
      sorted((b["metadata"]["namespace"], b["roleRef"]["name"], b["subjects"][0]["name"]) for b in rbs) ==
      [("checkout", "221b-admin", "221b-local:checkout-ops"), ("web", "221b-admin", "221b-local:checkout-ops"), ("web", "221b-viewer", "221b-local:web-readers")],
      [(b["metadata"]["namespace"], b["roleRef"]["name"]) for b in rbs])
check("every binding names a group, never a user or a service account",
      all(len(b["subjects"]) == 1 and b["subjects"][0]["kind"] == "Group" for b in rbs))
check("the bindings are labelled 221b/managed", all(b["metadata"]["labels"].get("221b/managed") == "true" for b in rbs))
check("no ClusterRoleBinding gives Admin or Viewer",
      not [b for b in of(d, "ClusterRoleBinding") if b["roleRef"]["name"] in ("221b-admin", "221b-viewer")])
groups = [r for r in console_role(d)["rules"] if r.get("resources") == ["groups"]][0]
check("the console may assert the local groups, with their prefix",
      {"221b-local:web-readers", "221b-local:checkout-ops"} <= set(groups["resourceNames"]))

# ---- LDAP (RB-19, RB-20, RB-37)
d = docs("chart-ldap.yaml")
rbs = settings_bindings(d)
check("LDAP groups under viewer and admin become RoleBindings with the LDAP prefix",
      sorted((b["metadata"]["namespace"], b["roleRef"]["name"], b["subjects"][0]["name"]) for b in rbs) ==
      [("api", "221b-viewer", "221b-ldap:team-a-readers"), ("web", "221b-admin", "221b-ldap:team-a-ops"), ("web", "221b-viewer", "221b-ldap:team-a-readers")])
dep = of(d, "Deployment")[0]
cont = dep["spec"]["template"]["spec"]["containers"][0]
env = {e["name"]: e for e in cont["env"]}
check("the search account's password comes from the named Secret, which the chart does not create",
      env["LDAP_BIND_PASSWORD"]["valueFrom"]["secretKeyRef"] == {"name": "221b-ldap", "key": "bind-password"} and
      not [x for x in of(d, "Secret") if "bind-password" in json.dumps(x)])
check("the CA is mounted from its Secret", any(v["name"] == "ldap-ca" for v in dep["spec"]["template"]["spec"]["volumes"]) and
      any(m["name"] == "ldap-ca" for m in cont["volumeMounts"]))
groups = [r for r in console_role(d)["rules"] if r.get("resources") == ["groups"]][0]
check("the console may assert the LDAP groups with their prefix and no others",
      "221b-ldap:platform-admins" in groups["resourceNames"] and "system:masters" not in groups["resourceNames"])
np = of(d, "NetworkPolicy")[0]
check("the directory's port is allowed out", {"ports": [{"port": 636, "protocol": "TCP"}]} in np["spec"]["egress"])
cm = [x for x in of(d, "ConfigMap") if x["metadata"]["name"].endswith("-auth-config")][0]
cfg = json.loads(cm["data"]["auth.json"])
check("the file the console reads lists the groups of each role",
      cfg["lists"] == {"viewer": ["team-a-readers"], "admin": ["team-a-ops"], "superViewer": ["auditors"], "superAdmin": ["platform-admins"]} and
      cfg["ldap"]["userSearch"]["filter"] == "(uid=%s)" and cfg["ldap"]["groupSearch"]["filter"] == "(member=%s)", cfg)

check("the file carries the RoleBindings it renders, for the YAML of other clusters (RB-29)",
      sorted(b["name"] for b in cfg["bindings"]) == sorted(b["metadata"]["name"] for b in rbs) and
      all(b["role"] in ("viewer", "admin") and b["subject"].startswith("221b-ldap:") for b in cfg["bindings"]), cfg["bindings"])

# ---- OIDC with role lists keeps the provider's prefix
d = docs("chart-oidc-roles.yaml")
rbs = settings_bindings(d)
check("OIDC groups with namespaces are bound with auth.kubernetes.groupPrefix",
      [(b["metadata"]["namespace"], b["subjects"][0]["name"]) for b in rbs] == [("web", "oidc:devs")])
groups = [r for r in console_role(d)["rules"] if r.get("resources") == ["groups"]][0]
check("with OIDC role lists the console's groups are limited too", "oidc:devs" in groups.get("resourceNames", []))
d = docs("chart-oidc.yaml")
groups = [r for r in console_role(d)["rules"] if r.get("resources") == ["groups"]][0]
check("OIDC without lists keeps every group of the token, as before (RB-57)", "resourceNames" not in groups)

sys.exit(1 if FAILED else 0)
