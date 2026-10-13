# Phase 2 review

This is the guide for the review that closes Phase 2 ([requirements](requirements.md#phase-2-read-only-views)). For each item on the review list it says what was built, what CI already proves on every pull request, and what is left for you to check by hand. The Phase 1 guide is [phase-1-review.md](phase-1-review.md); [architecture.md](architecture.md) has been brought up to date and gained seven decision records (ADR-18 to ADR-24).

## What Phase 2 built

Four tabs, the navigation that holds them, and the details behind every row: **Namespaces** (what each namespace holds, by kind, with the kinds funnel), **Pods** (filters by node, namespace and status; details with containers, environment, volumes, events, the owner chain, live logs and YAML), **Services** (the External columns; details with ports, endpoints and the pods behind them) and **Helm** (releases with history, notes, values, resources and pods). Every object opens as YAML. Nodes link to their pods, pods to their node, namespace, Service and release, Services to their pods. The Nodes tab gained the label and taint picker held back from Phase 1.

The server now watches 27 kinds instead of two, keeps them trimmed to what the screens show, builds the rows as named views that exist only while a browser shows them, and streams logs. Secrets are read as the Phase 1 review settled: reduced to key names and sizes as they enter the cache, Helm release records decoded with credentials masked, and a chart setting (`secrets.read`) that withholds Secrets altogether, which hides the Helm tab.

Asked for at the Phase 1 review and done here: the search box suggests as you type; every table has the same Columns and Export controls, with CSV added to Excel and JSON; a node's pod count links to its pods.

Decisions worth a look, because they shape what you will see:

- **ADR-18:** eleven kinds the Namespaces tab only lists by name are watched as metadata only, so their bodies never leave the API server; the YAML view fetches an object in full when asked.
- **ADR-19:** the Pods view and each namespace's view are built only while some browser shows them, and pod metrics are polled only then. The counts in the tab bar come from the caches, so they are right regardless.
- **ADR-20:** masking of Helm values is by key name (`password`, `token`, `secret`, `apiKey`, `cert`, `auth`, `license` and the like) and by content (PEM private keys). A credential under an unusual key would show; the details list what was masked so the gap is visible.
- **ADR-21:** pod logs stream through the server, batched, with 50 open logs per server at once; nothing is stored.
- **ADR-23:** every table is one component, an ARIA grid that renders only the rows in view once a table passes 100 rows (which supersedes Phase 1's ADR-12). Browser find sees only the rendered rows; the search box covers the rest.
- **ADR-24:** suggestions come from the rows loaded in the browser, so they are instant and never ask the server.

## 1. Navigation and the four tabs against the prototype and kubectl (P2-01 to P2-06)

**CI proves** (`web/e2e`, in Chromium, Firefox and WebKit against the simulator, and in Chromium on kind with real Kubernetes 1.35, 1.36 and 1.37):

- the bar shows Nodes, Namespaces, Pods and Helm, Services sits under More, and at 390, 700 and 1024 px no tab is cut off and the open tab always shows, in the bar or as the More button (P2-01);
- the Namespaces tab lists every namespace, and for the namespace with the most pods the count of each kind's table matches `kubectl get <kind> -n <ns>`; the kinds funnel narrows the kinds shown and the search filters by name or detail (P2-02);
- the Pods tab lists every pod with its status, node and namespace, filters by each, and a crash-looping pod shows its last termination reason in the Restarts column and its previous container's logs (P2-03);
- a pod's details show its containers, environment (a variable taken from a Secret shows the reference only), volumes, events and owner chain; its logs start within 2 seconds, follow live, switch containers and download; a pod deleted while its details are open says so (P2-04);
- the Services tab lists every Service with type, cluster IP, ports, endpoints and the External columns; a LoadBalancer without an external IP shows as pending with the reason, in the table and the details; details list the endpoints and link to the pods behind them (P2-05);
- the Helm tab matches `helm list -A` for every release (chart, app version, revision, status, update time) and the details match `helm history` revision for revision, show the notes, the user-supplied values with credentials masked, the release's resources with their state, and its pods (P2-06);
- no WCAG 2.2 AA violations (axe) on any tab, with the drawers, logs and menus open, in both themes, and a visible focus ring on every control; nothing scrolls sideways at 390 px.

**Screenshots** at 1440 px, the prototype on the left and the console on the right (a simulated cluster with the prototype's cluster name, so the numbers differ). The prototype shows parts of later phases: the alert banners and the Alerts tab, the Fleets tab, the cluster menu, the drawer's actions (Ask Sherlock, Restart, Delete) and the assistants. Phase 2 leaves them out by design. The console departs from the prototype where the review asked it to: every table has Columns and Export menus in place of the prototype's Excel button, and the search boxes suggest as you type. The pod details gained a Logs tab, which the prototype does not draw.

| Prototype | Console |
| --- | --- |
| ![Prototype, Pods, 1440 px](images/phase-2/prototype-pods-light-1440.png) | ![Console, Pods, 1440 px](images/phase-2/app-pods-light-1440.png) |
| ![Prototype, pod details](images/phase-2/prototype-pod-details-light-1440.png) | ![Console, pod details](images/phase-2/app-pod-details-light-1440.png) |
| ![Prototype, Namespaces](images/phase-2/prototype-namespaces-light-1440.png) | ![Console, Namespaces](images/phase-2/app-namespaces-light-1440.png) |
| ![Prototype, Services](images/phase-2/prototype-services-light-1440.png) | ![Console, Services](images/phase-2/app-services-light-1440.png) |
| ![Prototype, Helm](images/phase-2/prototype-helm-light-1440.png) | ![Console, Helm](images/phase-2/app-helm-light-1440.png) |
| ![Prototype, nodes by label and taint](images/phase-2/prototype-nodes-by-label-light-1440.png) | ![Console, nodes by label and taint](images/phase-2/app-nodes-by-label-light-1440.png) |
| ![Prototype, Pods, dark](images/phase-2/prototype-pods-dark-1440.png) | ![Console, Pods, dark](images/phase-2/app-pods-dark-1440.png) |
| ![Prototype, Pods, 390 px](images/phase-2/prototype-pods-light-390.png) | ![Console, Pods, 390 px](images/phase-2/app-pods-light-390.png) |

More of the console: a pod's logs, a Service's details, a release's values, and the More menu on a phone.

| Pod logs | Service details |
| --- | --- |
| ![Pod logs](images/phase-2/app-pod-logs-light-1440.png) | ![Service details](images/phase-2/app-service-details-light-1440.png) |

| Helm values | More menu at 390 px |
| --- | --- |
| ![Helm values, with the password masked](images/phase-2/app-helm-values-light-1440.png) | ![The More menu on a phone](images/phase-2/app-more-light-390.png) |

**To check by hand** on the test cluster, with the console open:

```sh
kubectl get namespaces                                   # the Namespaces list
kubectl get all,configmaps,secrets,pvc,ingress -n <ns>   # the counts on a namespace's kind cards
kubectl get pods -A -o wide                              # the Pods tab: status, restarts, node
kubectl describe pod <pod> -n <ns>                        # a pod's details: containers, volumes, events, owner
kubectl logs <pod> -n <ns> --tail=20                      # the Logs tab (and --previous after a crash)
kubectl get svc -A -o wide; kubectl get endpointslices -n <ns>   # the Services tab and a Service's endpoints
helm list -A; helm history <release> -n <ns>; helm get values <release> -n <ns>   # the Helm tab
```

Pick a pod that restarted and compare the Restarts column's tooltip with `kubectl describe pod`'s last state; open a Deployment's pod and follow the owner chain to the ReplicaSet and the Deployment; pick a Service of type LoadBalancer and compare the External columns with `kubectl get svc`.

## 2. Secret handling and the YAML view (P2-07, P2-08)

**CI proves:** a Secret with a known value planted in the simulated cluster, and a Helm release whose values hold a known password, appear nowhere: not in any API response (every view's snapshot, every namespace's rows, every pod's, Service's and release's details, every object's YAML), not in the event stream, not in the Excel, CSV or JSON exports of any table, and not in the server's log, while the Secret's key names and sizes do show in its namespace and the password's key is listed as masked (P2-07, `web/e2e/secrets.spec.ts`). The YAML view of a node and a pod matches `kubectl get -o yaml` line for line in kind, apart from `managedFields`, which is removed (and the resource version and status, which change between two reads); a Service's and a Secret's YAML show the object, and a Secret's values are replaced by `<N bytes, hidden by 221B>`, with neither the value nor its base64 form anywhere in the page, including the `last-applied-configuration` annotation that `kubectl apply` leaves on the Secret with every value in it, which the kind run caught (P2-08). Unit tests cover the Secret transform (what the cache keeps), Helm record decoding and masking by key and by PEM content, and the YAML filter.

**To check by hand** on the test cluster:

```sh
kubectl create secret generic review-test -n default --from-literal=password=correct-horse-battery-staple
# In the console: Namespaces > default > Secrets shows review-test with "password (28 B)"; its YAML shows
# password: <28 bytes, hidden by 221B>; the Pods, Services and Helm exports and the pod details never show the value.
kubectl get secret review-test -n default -o yaml     # the same YAML, with the value, for comparison
kubectl -n 221b logs deploy/console-221b | grep -c correct-horse   # 0
kubectl delete secret review-test -n default
helm get values <release> -n <ns>                      # compare with the Values tab: credential keys are masked
kubectl get pod <pod> -n <ns> -o yaml                  # compare with the pod's YAML tab: managedFields gone
```

## 3. Links between tabs and the label and taint filters (P2-09)

**CI proves:** a node's pod count opens the Pods tab filtered to that node, with exactly that node's pods; a pod's details link to its node's details, its namespace, its Service and its Helm release, and the release link opens that release; a Service's details link to each endpoint's pod (opening the pod's details, which name the Service) and to the pods behind the Service; a release's resources open as YAML and its pods are listed with their usage. On Nodes, the label and taint picker lists every label value and taint with how many nodes carry it, narrows the table (values of one label match any of them; different labels and taints must all match), keeps the choice in the link, and a chip removes it again. Every filter lives in the URL hash, so a filtered view can be bookmarked or sent to someone.

**To check by hand:** in the Nodes tab click a node's pod count, then in the Pods tab click a pod's node, namespace, Service and release; in the Services tab open a Service and click an endpoint's pod; in Nodes open the label and taint picker, pick a taint, and copy the address bar into a new window.

## 4. Permissions, with Secrets on and off (P2-10)

**CI proves** in kind: with the chart's defaults, the ServiceAccount can get, list and watch exactly the 27 kinds the tabs show, get pod logs, get and list node and pod metrics, get the kubelets' stats, and get and patch its own settings ConfigMap by name; `kubectl auth can-i --list` shows nothing more in any namespace beyond what every identity may do, every granted verb list is read-only except the settings ConfigMap's, and the API server refuses a pod deletion. After `helm upgrade --set secrets.read=false`, the ClusterRole names no Secrets, `kubectl get secrets -A` as the ServiceAccount is Forbidden, the console reports Secrets as off, the Helm view is empty and a release is not found, no namespace lists a Secret, a Secret's YAML is refused, and nodes and pods are served as before (`hack/check-rbac.sh`, `hack/e2e-kind.sh`). The chart checks validate both renderings against the Kubernetes API schemas and confirm the rules line by line (`hack/check-chart.sh`).

**To check by hand** on the test cluster:

```sh
kubectl auth can-i --list --as=system:serviceaccount:221b:console-221b
kubectl get secrets -A --as=system:serviceaccount:221b:console-221b -o name | head   # allowed by default
helm upgrade console deploy/helm/221b -n 221b --reset-then-reuse-values --set secrets.read=false
kubectl get secrets -A --as=system:serviceaccount:221b:console-221b    # Forbidden
# In the console: the Helm tab is gone, Namespaces list no Secrets, a Secret's YAML is not available.
helm upgrade console deploy/helm/221b -n 221b --reset-then-reuse-values --set secrets.read=true
```

## 5. Scale and footprint

**CI proves** with kwok in kind, on every pull request: 500 nodes and 15,000 pods, the first screen of nodes within 2 seconds, sorting within 200 ms and server memory under 1 GiB with every Phase 2 kind watched. The job's summary reports the measured numbers (about 170 ms, 60 ms and 360 MiB on the CI runner). Against the simulator with the same numbers, the Pods tab shows its first rows about a second after opening, sorts 15,000 pods in about 65 ms and narrows them by search in about 200 ms; the server holds about 450 MiB while the Pods tab is open.

## Still open

- **Which cluster hosts the test deployment** (open question 1): yours to choose; the checks above assume the chart is installed there as the README shows.
- **Phase 3** brings sign-in and per-person access (open questions 5 and 6). Until then, anyone who can reach the console sees what its ServiceAccount can read, which Phase 2 widened to pod logs, Helm values (masked) and Secret key names; the security model in [architecture.md](architecture.md#security-model) says where to keep port-forward rights.
