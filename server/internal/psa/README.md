# Pod Security checks

`api` and `policy` are copied from
[k8s.io/pod-security-admission](https://github.com/kubernetes/pod-security-admission)
at v0.37.1 (Apache License 2.0, see LICENSE; each file keeps its
copyright header), with these changes only:

- the import path of `api` points here (and gofmt sorted the imports);
- `api/attributes.go` and `api.GetAPIVersion` are left out: they serve
  the admission webhook and need packages 221B does not vendor.

They are the checks the API server runs when it admits a pod, so the
Policies tab (P4-06) reports exactly what a namespace label change would
warn about. To update, copy the same files from a newer tag.
