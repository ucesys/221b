// The hash router: #/pods?node=worker-1&ns=web opens the Pods tab with
// those filters. Links between tabs (P2-09) are plain hrefs, so they work
// in a new window and in the browser's history.
//
// Another cluster's pages live under its id: #/c/analytics-dev/pods. The
// cluster the console runs in (or was started against) is "local" and
// keeps the short form, so every Phase 1 and 2 link still works.

import { useSyncExternalStore } from "react";

export type Tab =
  | "nodes"
  | "fleets"
  | "scripts"
  | "run-history"
  | "variables"
  | "run-profiles"
  | "namespaces"
  | "pods"
  | "helm"
  | "alerts"
  | "services"
  | "networks"
  | "storage"
  | "access"
  | "policies";

export const TABS: { id: Tab; label: string; tip: string }[] = [
  {
    id: "alerts",
    label: "Alerts",
    tip: "Pods failing to start or pending, node problems and warning events",
  },
  {
    id: "nodes",
    label: "Nodes",
    tip: "Every node: role, status, what is requested and in use",
  },
  {
    id: "fleets",
    label: "Fleets",
    tip: "Groups of nodes by the labels and taints they share",
  },
  // Phase 11 part 2 (AU-01, AU-13): the Automation area's, a SuperAdmin's.
  {
    id: "scripts",
    label: "Scripts",
    tip: "Shell scripts with a 221b header, their versions and approvals",
  },
  {
    id: "variables",
    label: "Variables",
    tip: "Values the scripts read on this cluster’s fleets and nodes, and each fleet’s privilege ceiling",
  },
  {
    id: "run-profiles",
    label: "Run Profiles",
    tip: "Who a script runs as, with what privilege, and on which fleets",
  },
  {
    id: "run-history",
    label: "Run History",
    tip: "Every run and dry run, with each node’s result",
  },
  {
    id: "namespaces",
    label: "Namespaces",
    tip: "Every namespace and what it holds, by kind",
  },
  {
    id: "pods",
    label: "Pods",
    tip: "Every pod, filtered by node, namespace and status",
  },
  // ST-01: after Pods (moved from after Networks on October 26, 2026).
  {
    id: "storage",
    label: "Storage",
    tip: "Storage classes, volumes, claims and the pods that mount them",
  },
  {
    id: "helm",
    label: "Helm",
    tip: "Helm releases: their source, drift and history; install and upgrade from git, a Helm repository or an OCI registry",
  },
  {
    id: "services",
    label: "Services",
    tip: "Every Service, its endpoints and the hostnames that publish it",
  },
  {
    id: "networks",
    label: "Networks",
    tip: "Ingress, DNS, pod and service networks — trace a request hop by hop",
  },
  {
    id: "access",
    label: "Access",
    tip: "Access Control: who can do what in the cluster",
  },
  {
    id: "policies",
    label: "Policies",
    tip: "Network policies, disruption budgets, quotas, Pod Security and admission webhooks",
  },
];

/** The top bar's areas: Kubernetes, Automation for a SuperAdmin (AU-01), and Cases for whoever has something in it (CS-01). */
export type Area = "kubernetes" | "automation" | "cases";

/** Whether a tab is Automation's alone; Nodes is in both areas. */
export function isAutomationTab(tab: Tab | null): boolean {
  return (
    tab === "scripts" ||
    tab === "run-history" ||
    tab === "variables" ||
    tab === "run-profiles"
  );
}

/** The connection of the cluster the console was started against. */
export const LOCAL = "local";

/** The page: every cluster at a glance (the landing page), a cluster's tabs, its own page (its connection, P3-07), the Administration area (RB-41), or Sherlock in a window of its own (P5-08). */
export type View =
  "home" | "tabs" | "cluster" | "sherlock" | "watson" | "assistants" | "admin" | "cases";

/** The first tab, where a cluster opens. */
export const FIRST_TAB: Tab = "alerts";

/** The home page: every cluster at a glance. */
export const HOME = "#/";

export interface Route {
  /** The cluster's connection id; "local" without a prefix. */
  cluster: string;
  view: View;
  tab: Tab;
  /** Path segments after the tab: #/namespaces/web → ["web"]. */
  path: string[];
  params: URLSearchParams;
}

/** A path segment decoded; a malformed escape such as "50%" is kept as written. */
function decodeSeg(seg: string): string {
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}

/** Pages that changed their names, by the name a saved link still uses. */
const RENAMED: Record<string, Tab> = {
  executions: "run-history",
  parameters: "variables",
  configurations: "run-profiles",
};

export function parseHash(hash: string): Route {
  const h = hash.replace(/^#\/?/, "");
  const [pathPart = "", query = ""] = h.split("?", 2) as [string?, string?];
  let segs = pathPart.split("/").filter(Boolean).map(decodeSeg);
  let cluster = LOCAL;
  if (segs[0] === "c" && segs[1]) {
    cluster = segs[1];
    segs = segs.slice(2);
  }
  const first = segs[0] ?? "";
  // Nothing at all, or #/home: the home page.
  if (
    cluster === LOCAL &&
    !h.startsWith("c/") &&
    (first === "" || first === "home")
  )
    return {
      cluster,
      view: "home",
      tab: FIRST_TAB,
      path: [],
      params: new URLSearchParams(query),
    };
  if (first === "cluster")
    return {
      cluster,
      view: "cluster",
      tab: FIRST_TAB,
      path: [],
      params: new URLSearchParams(query),
    };
  // The Administration area (RB-41): #/admin/roles, #/admin/permissions, #/admin/users and #/admin/settings/<section>.
  // Roles and Permissions describe a cluster, so they take the cluster prefix; Users and Settings belong to the console.
  // Sessions and Audit log moved under Logs: their old links land there.
  // --- Cases (Phase 12, CS-01) ---
  // Administration › Approvals moved to AIOps › Inbox (CS-06): its old
  // link opens the Inbox on approvals.
  if (first === "admin" && segs[1] === "approvals") {
    const params = new URLSearchParams(query);
    params.set("kind", "approvals");
    return { cluster, view: "cases", tab: FIRST_TAB, path: ["inbox"], params };
  }
  // #/cases is the Inbox; #/cases/reports/12 a report, and so on.
  if (first === "cases") {
    // Old addresses of tabs that moved: Runs under Jobs; cases and reports
    // in the Inbox; known issues and the wiki under Mycroft.
    const moved: Record<string, string[]> = {
      cases: ["inbox", "case"],
      reports: ["inbox", "report"],
      known: ["mycroft", "known"],
      knowledge: ["mycroft", "wiki"],
    };
    // --- Mycroft tab (Phase 12, wave 5) ---
    // Since wave 6e an RCA is part of its case, and past RCAs are found by
    // searching the closed cases: Mycroft › RCAs' old addresses, and the
    // wiki's before it, open the Inbox showing all (an RCA's id opens
    // nothing now).
    if ((segs[1] === "mycroft" || segs[1] === "knowledge") && segs[2] === "rcas") {
      const params = new URLSearchParams(query);
      params.set("show", "all");
      return { cluster, view: "cases", tab: FIRST_TAB, path: ["inbox"], params };
    }
    // Add moved to Settings › Knowledge's Added in Mycroft card (MY-11):
    // the Wiki's Add view's old addresses open it there.
    if (
      ((segs[1] === "mycroft" && segs[2] === "wiki") || segs[1] === "knowledge") &&
      segs[segs[1] === "mycroft" ? 3 : 2] === "add"
    )
      return {
        cluster: LOCAL,
        view: "admin",
        tab: FIRST_TAB,
        path: ["settings", "knowledge", "added"],
        params: new URLSearchParams(query),
      };
    // The wiki's exceptions are Mycroft's tab of their own now:
    // #/cases/knowledge/exceptions/EX-3 opens #/cases/mycroft/exceptions/EX-3.
    if (segs[1] === "knowledge" && segs[2] === "exceptions")
      return {
        cluster,
        view: "cases",
        tab: FIRST_TAB,
        path: ["mycroft", ...segs.slice(2)],
        params: new URLSearchParams(query),
      };
    // --- end of the Mycroft tab.
    // Validation rules moved from Mycroft to Jobs (CF-89):
    // #/cases/mycroft/rules/<id>/<section> opens #/cases/jobs/rules/<id>/<section>.
    if (segs[1] === "mycroft" && segs[2] === "rules")
      return {
        cluster,
        view: "cases",
        tab: FIRST_TAB,
        path: ["jobs", ...segs.slice(2)],
        params: new URLSearchParams(query),
      };
    const to = moved[segs[1] ?? ""];
    if (to) {
      const params = new URLSearchParams(query);
      return {
        cluster,
        view: "cases",
        tab: FIRST_TAB,
        path: [...(segs.length > 2 || to[0] === "mycroft" ? to : ["inbox"]), ...segs.slice(2)],
        params,
      };
    }
    if (segs[1] === "runs")
      return {
        cluster,
        view: "cases",
        tab: FIRST_TAB,
        path: ["jobs", "runs", ...segs.slice(2)],
        params: new URLSearchParams(query),
      };
    const page = CASES_TABS.some((t) => t.id === segs[1])
      ? (segs[1] as CasesTab)
      : "inbox";
    return {
      cluster,
      view: "cases",
      tab: FIRST_TAB,
      path: [page, ...(page === segs[1] ? segs.slice(2) : [])],
      params: new URLSearchParams(query),
    };
  }
  // --- end Cases ---
  if (first === "admin")
    return {
      cluster,
      view: "admin",
      tab: FIRST_TAB,
      path:
        segs[1] === "sessions" || segs[1] === "audit"
          ? ["logs", ...segs.slice(1)]
          : segs.slice(1),
      params: new URLSearchParams(query),
    };
  // Settings moved under Administration (RB-47): an old #/settings link lands on it.
  if (cluster === LOCAL && first === "settings")
    return {
      cluster,
      view: "admin",
      tab: FIRST_TAB,
      path: ["settings", ...segs.slice(1)],
      params: new URLSearchParams(query),
    };
  // Sherlock in a window of its own (P5-08).
  if (first === "sherlock" || first === "watson" || first === "assistants")
    return {
      cluster,
      view: first,
      tab: FIRST_TAB,
      path: [],
      params: new URLSearchParams(query),
    };
  // #/automation is where the area's pages were before they were tabs.
  if (first === "automation") {
    const old = segs[1];
    return {
      cluster,
      view: "tabs",
      tab: old === "runs" ? "run-history" : old === "params" ? "variables" : "scripts",
      path: [],
      params: new URLSearchParams(query),
    };
  }
  // The Automation pages' earlier names still open them.
  const was = RENAMED[first];
  if (was)
    return {
      cluster,
      view: "tabs",
      tab: was,
      path: segs.slice(1),
      params: new URLSearchParams(query),
    };
  const tab: Tab = TABS.some((t) => t.id === first)
    ? (first as Tab)
    : FIRST_TAB;
  return {
    cluster,
    view: "tabs",
    tab,
    path: tab === first ? segs.slice(1) : [],
    params: new URLSearchParams(query),
  };
}

function query(params: Record<string, string | string[] | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === "" || (Array.isArray(v) && v.length === 0))
      continue;
    q.set(k, Array.isArray(v) ? v.join(",") : v);
  }
  const s = q.toString();
  return s ? "?" + s : "";
}

function prefix(cluster: string): string {
  return cluster === LOCAL ? "#/" : "#/c/" + encodeURIComponent(cluster) + "/";
}

/**
 * Builds a hash in the cluster open now: href('pods', [], {node: 'a'}) →
 * "#/pods?node=a", or "#/c/dev/pods?node=a" when another cluster is open.
 * Every link between tabs stays in the cluster it was made in.
 */
export function href(
  tab: Tab,
  path: string[] = [],
  params: Record<string, string | string[] | undefined> = {},
): string {
  return clusterHref(current.cluster, tab, path, params);
}

/** A hash in a given cluster, for the cluster menu (P3-04). */
export function clusterHref(
  cluster: string,
  tab: Tab,
  path: string[] = [],
  params: Record<string, string | string[] | undefined> = {},
): string {
  return (
    prefix(cluster) +
    [tab, ...path.map(encodeURIComponent)].join("/") +
    query(params)
  );
}

/** The pages of the Administration area, in order (RB-41). Approvals moved to AIOps › Inbox (CS-06). */
export type AdminPage =
  | "roles"
  | "permissions"
  | "users"
  | "toolbox"
  | "logs"
  | "settings"
  | "backup";

export const ADMIN_PAGES: AdminPage[] = [
  "roles",
  "permissions",
  "users",
  "toolbox",
  "logs",
  "settings",
  "backup",
];

// --- Cases (Phase 12, CS-01) ---

/** The tabs of the Cases area, in order (CS-02). */
export type CasesTab =
  | "inbox"
  | "cases"
  | "runs"
  | "reports"
  | "known"
  | "jobs"
  | "knowledge"
  | "mycroft";

export const CASES_TABS: { id: CasesTab; label: string; tip: string }[] = [
  {
    id: "inbox",
    label: "Inbox",
    tip: "Every case, proposed change, approval and draft, pending first",
  },
  {
    id: "jobs",
    label: "Jobs",
    tip: "The agents' schedules and triggers, and each run with its report, what its findings did and its proposed changes",
  },
  {
    id: "mycroft",
    label: "Mycroft",
    tip: "The wiki, known issues, accepted exceptions and saved fixes",
  },
];

/** A page of the Cases area: "#/cases/reports/12", in the cluster open now so the dock stays on it. */
export function casesHref(
  tab: CasesTab = "inbox",
  path: string[] = [],
  params: Record<string, string | string[] | undefined> = {},
): string {
  return (
    prefix(current.cluster) +
    ["cases", tab, ...path.map(encodeURIComponent)].join("/") +
    query(params)
  );
}

// --- end Cases ---

/** A page of the Administration area: "#/admin/roles/admin", or "#/c/dev/admin/permissions" for another cluster. */
export function adminHref(
  page: AdminPage,
  path: string[] = [],
  cluster: string = LOCAL,
  params: Record<string, string | undefined> = {},
): string {
  return (
    prefix(cluster) +
    ["admin", page, ...path.map(encodeURIComponent)].join("/") +
    query(params)
  );
}

/** A cluster's own page: "#/cluster", or "#/c/dev/cluster". */
export function clusterPageHref(cluster: string): string {
  return prefix(cluster) + "cluster";
}

/** The API path of a cluster's data: "pods/web/api" → "/api/v1/pods/web/api", or under /api/v1/clusters/‹id›/ for another cluster. */
export function apiPath(
  path: string,
  cluster: string = current.cluster,
): string {
  return cluster === LOCAL
    ? "/api/v1/" + path
    : "/api/v1/clusters/" + encodeURIComponent(cluster) + "/" + path;
}

/** A comma-separated list parameter: ?ns=a,b → ["a", "b"]. */
export function listParam(params: URLSearchParams, key: string): string[] {
  const v = params.get(key);
  return v ? v.split(",").filter(Boolean) : [];
}

// Each tab as it was last left, per cluster: the tab bar opens a tab with
// its filters, its selection and the object open in it, rather than
// empty. Kept for this browser tab's session. A details pane over the
// tab (peek) and a question for Sherlock (ask) are for once.
const ONCE = ["peek", "ask"];
const LAST_KEY = "221b.tabs.last";
const lastTab = new Map<string, string>(readLast());

function readLast(): [string, string][] {
  try {
    const raw =
      typeof window === "undefined"
        ? null
        : window.sessionStorage.getItem(LAST_KEY);
    return raw ? (JSON.parse(raw) as [string, string][]) : [];
  } catch {
    return [];
  }
}

function remember(r: Route) {
  if (r.view !== "tabs") return;
  const params: Record<string, string> = {};
  for (const [k, v] of r.params.entries()) if (!ONCE.includes(k)) params[k] = v;
  lastTab.set(
    r.cluster + "/" + r.tab,
    clusterHref(r.cluster, r.tab, r.path, params),
  );
  try {
    window.sessionStorage.setItem(LAST_KEY, JSON.stringify([...lastTab]));
  } catch {
    // A convenience; without storage it lasts until reload.
  }
}

/** A tab of the cluster open now, as it was last left: for the tab bar. */
export function tabHref(tab: Tab): string {
  return lastTab.get(current.cluster + "/" + tab) ?? href(tab);
}

/** For tests: forgets every tab's last state. */
export function forgetTabsForTests(): void {
  lastTab.clear();
}

const listeners = new Set<() => void>();
let current = parseHash(
  typeof window === "undefined" ? "" : window.location.hash,
);
if (typeof window !== "undefined") remember(current);

function onChange() {
  current = parseHash(window.location.hash);
  remember(current);
  for (const fn of listeners) fn();
}

function subscribe(fn: () => void) {
  if (listeners.size === 0 && typeof window !== "undefined")
    window.addEventListener("hashchange", onChange);
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0 && typeof window !== "undefined")
      window.removeEventListener("hashchange", onChange);
  };
}

/** Calls fn after every change of route, outside React. */
export function onRouteChange(fn: () => void): () => void {
  return subscribe(fn);
}

/** The route as it is now, outside React: for links built at click time. */
export function currentRoute(): Route {
  return current;
}

export function useRoute(): Route {
  return useSyncExternalStore(
    subscribe,
    () => current,
    () => current,
  );
}

/** Goes to a hash. replace keeps the history entry, for filter changes. */
export function navigate(hash: string, replace = false): void {
  if (window.location.hash === hash || (hash === "#/" && !window.location.hash))
    return;
  if (replace) {
    window.history.replaceState(null, "", hash);
    onChange();
  } else {
    window.location.hash = hash;
  }
}

/** Changes some parameters of the current route, keeping the rest. */
export function withParams(
  route: Route,
  changes: Record<string, string | string[] | undefined>,
): string {
  const merged: Record<string, string | string[] | undefined> = {};
  for (const [k, v] of route.params.entries()) merged[k] = v;
  Object.assign(merged, changes);
  if (route.view === "home") return HOME + query(merged);
  if (route.view === "admin" || route.view === "cases")
    return (
      prefix(route.cluster) +
      [route.view, ...route.path.map(encodeURIComponent)].join("/") +
      query(merged)
    );
  return clusterHref(route.cluster, route.tab, route.path, merged);
}

/** For tests: sets the route the links are built in. */
export function setRouteForTests(hash: string): void {
  current = parseHash(hash);
}
