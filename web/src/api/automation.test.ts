import { describe, expect, it } from "vitest";
import type { AutomationFleet, FleetVariables, RunProfile } from "./automation";
import {
  runProfilesFor,
  allows,
  approvedOf,
  batches,
  canApprove,
  countsText,
  CUSTOM_NODES,
  fleetOf,
  formatGroups,
  inGroups,
  inState,
  missingParams,
  nodeParams,
  paramClashes,
  parseGroups,
  resolveParams,
  rolloutHelp,
  runnable,
  scriptGroups,
  type Script,
  type ScriptSummary,
  type Version,
  UNGROUPED,
} from "./automation";

const ver = (over: Partial<Version>): Version => ({
  n: 1,
  text: "",
  hash: "abc",
  header: { describe: "d", params: [], privilege: "none", dryRun: true },
  findings: [],
  state: "draft",
  author: { name: "Pat", user: "pat" },
  created: "2026-10-31T00:00:00Z",
  ...over,
});

const one = (params: FleetVariables): AutomationFleet[] => [
  { id: "_all", name: "All nodes", nodes: [], params },
];

describe("what a node inherits", () => {
  const fleet = (
    name: string,
    node: string,
    values: Record<string, string>,
    nodes = {},
  ): AutomationFleet => ({
    id: name,
    name,
    nodes: [{ name: node, paired: true }],
    params: { values, nodes },
  });
  it("takes its own value over a fleet's, a fleet's over All nodes', and flags two fleets", () => {
    const fleets = [
      fleet("Web", "n1", { A: "web", C: "web" }, { n1: { C: "mine" } }),
      fleet("GPU", "n1", { A: "gpu", C: "gpu", E: "gpu" }),
      {
        id: "_all",
        name: "All nodes",
        nodes: [],
        params: { values: { D: "all", E: "all" } },
      },
    ];
    const got = Object.fromEntries(
      nodeParams(fleets, "n1").map((p) => [p.name, p]),
    );
    expect(got.A).toMatchObject({ conflict: ["GPU", "Web"] });
    expect(got.A!.value).toBeUndefined();
    expect(got.C).toMatchObject({ value: "mine", source: "node" });
    expect(got.D).toMatchObject({ value: "all", from: "All nodes" });
    expect(got.E).toMatchObject({ value: "gpu", from: "GPU" });
    const params = [{ name: "A", required: true }];
    expect(paramClashes(params, fleets, ["n1"], {})).toEqual([
      "A on n1 comes from GPU and Web",
    ]);
    expect(paramClashes(params, fleets, ["n1"], { A: "run" })).toEqual([]);
    expect(missingParams(params, fleets, ["n1"], {})).toEqual([]);
  });
});

describe("resolveParams", () => {
  const params = [
    { name: "A", required: true },
    { name: "B", required: false },
    { name: "C", required: true },
  ];
  const fp = {
    values: { A: "fleet", B: "fleet" },
    nodes: { n1: { A: "node" } },
  };
  it("takes the run’s value, then the node’s, then the fleet’s", () => {
    const r = resolveParams(params, one(fp), "n1", { B: "run" });
    expect(r.map((p) => [p.name, p.value, p.source])).toEqual([
      ["A", "node", "node"],
      ["B", "run", "run"],
      ["C", undefined, undefined],
    ]);
    expect(resolveParams(params, one(fp), "n2", {})[0]).toMatchObject({
      value: "fleet",
      source: "fleet",
    });
  });
  it("counts an empty value as a value", () => {
    expect(
      resolveParams(
        [{ name: "A", required: true }],
        one({ values: { A: "" } }),
        "n",
        {},
      )[0],
    ).toMatchObject({ value: "", source: "fleet" });
  });
  it("lists required parameters with no value, per node", () => {
    expect(missingParams(params, one(fp), ["n1", "n2"], {})).toEqual([
      "n1: C",
      "n2: C",
    ]);
    expect(missingParams(params, one(fp), ["n1"], { C: "x" })).toEqual([]);
  });
});

describe("batches", () => {
  const ns = ["a", "b", "c", "d", "e"];
  it("splits as the server does", () => {
    expect(batches(ns, { mode: "all" })).toEqual([ns]);
    expect(batches(ns, { mode: "size", size: 2 })).toEqual([
      ["a", "b"],
      ["c", "d"],
      ["e"],
    ]);
    expect(batches(ns, { mode: "canary" })).toEqual([
      ["a"],
      ["b", "c", "d", "e"],
    ]);
    expect(batches(ns, { mode: "canary", size: 3 })).toEqual([
      ["a"],
      ["b", "c", "d"],
      ["e"],
    ]);
    expect(batches(["a"], { mode: "canary", size: 3 })).toEqual([["a"]]);
    expect(batches([], { mode: "all" })).toEqual([]);
  });
});

describe("approval", () => {
  const pat = { name: "Pat", user: "pat" };
  const sam = { name: "Sam", user: "sam" };
  const lee = { name: "Lee", user: "lee" };
  it("never lets those who wrote or submitted a version approve it", () => {
    const v = ver({
      state: "review",
      author: pat,
      editors: [sam],
      submitted: { by: lee, at: "" },
    });
    expect(canApprove(v, pat)).toBe(false);
    expect(canApprove(v, sam)).toBe(false);
    expect(canApprove(v, lee)).toBe(false);
    expect(canApprove(v, { name: "Dee", user: "dee" })).toBe(true);
    expect(canApprove(ver({ state: "draft", author: pat }), sam)).toBe(false);
  });
  it("asks for no more once someone else has approved", () => {
    const done = ver({
      state: "approved",
      author: pat,
      approvals: [{ by: sam, at: "" }],
    });
    expect(canApprove(done, sam)).toBe(false);
    expect(canApprove(done, lee)).toBe(false);
  });
  it("runs a version only with an approval by someone who did not write it", () => {
    expect(
      runnable(
        ver({
          state: "approved",
          author: pat,
          approvals: [{ by: pat, at: "" }],
        }),
      ),
    ).toBe(false);
    expect(
      runnable(
        ver({
          state: "approved",
          author: pat,
          approvals: [{ by: sam, at: "" }],
        }),
      ),
    ).toBe(true);
    expect(
      runnable(
        ver({ state: "review", author: pat, approvals: [{ by: sam, at: "" }] }),
      ),
    ).toBe(false);
  });
});

describe("the version a run uses", () => {
  it("is the newest approved", () => {
    const s: Script = {
      name: "s",
      created: "",
      updated: "",
      versions: [
        ver({ n: 1, state: "superseded" }),
        ver({ n: 2, state: "approved" }),
        ver({ n: 3, state: "draft" }),
      ],
    };
    expect(approvedOf(s)?.n).toBe(2);
  });
});

describe("the Run dialog’s fleet", () => {
  const fleet = (id: string, ...names: string[]) => ({
    id,
    name: id,
    params: {},
    nodes: [
      ...names.map((name) => ({ name, paired: true })),
      { name: "off", paired: false },
    ],
  });
  const fleets = [
    fleet("web", "a", "b"),
    fleet("db", "c"),
    fleet("_all", "a", "b", "c"),
  ];
  it("names the fleet whose reachable nodes are the ones picked, in any order", () => {
    expect(fleetOf(fleets, ["b", "a"])).toBe("web");
    expect(fleetOf(fleets, ["a", "b", "c"])).toBe("_all");
  });
  it("is no fleet’s once the nodes are not exactly a fleet’s", () => {
    expect(fleetOf(fleets, ["a"])).toBe(CUSTOM_NODES);
    expect(fleetOf(fleets, ["a", "c"])).toBe(CUSTOM_NODES);
    expect(fleetOf(fleets, [])).toBe(CUSTOM_NODES);
  });
});

describe("the rollout help", () => {
  it("says what each mode does with the size given", () => {
    expect(rolloutHelp("canary", 5)).toContain("One node runs alone first");
    expect(rolloutHelp("canary", 5)).toContain("5 at a time");
    expect(rolloutHelp("canary", 0)).toContain("together");
    expect(rolloutHelp("size", 3)).toContain("3 at a time");
    expect(rolloutHelp("all", 3)).toContain("at most 20");
  });
});

describe("the state filter", () => {
  const s = (over: Partial<ScriptSummary>) =>
    ({ state: "draft", inReview: 0, approved: 0, ...over }) as ScriptSummary;
  it("finds a version waiting for review behind a newer draft", () => {
    expect(inState(s({ inReview: 2 }), "review")).toBe(true);
    expect(inState(s({}), "review")).toBe(false);
  });
  it("tells drafts, approved and never approved apart", () => {
    expect(inState(s({}), "draft")).toBe(true);
    expect(inState(s({ state: "approved", approved: 1 }), "draft")).toBe(false);
    expect(inState(s({ approved: 1 }), "approved")).toBe(true);
    expect(inState(s({ approved: 1 }), "unapproved")).toBe(false);
    expect(inState(s({}), "unapproved")).toBe(true);
    expect(inState(s({}), "")).toBe(true);
  });
});

describe("the rest", () => {
  it("allows full only under a full ceiling", () => {
    expect(allows(undefined, "none")).toBe(true);
    expect(allows(undefined, "full")).toBe(false);
    expect(allows({ ceiling: "full" }, "full")).toBe(true);
  });
  it("says counts in a fixed order", () => {
    expect(countsText({ failed: 1, changed: 3, waiting: 0 })).toBe(
      "3 changed · 1 failed",
    );
  });
  it("lists the groups with their counts, Ungrouped last", () => {
    const s = (name: string, group: string) =>
      ({ name, group }) as ScriptSummary;
    const list = [s("b", ""), s("z", "os"), s("a", "os"), s("c", "k8s")];
    expect(scriptGroups(list)).toEqual([
      { id: "k8s", name: "k8s", count: 1 },
      { id: "os", name: "os", count: 2 },
      { id: UNGROUPED, name: "Ungrouped", count: 1 },
    ]);
    expect(list.filter((x) => inGroups(x, [])).length).toBe(4);
    expect(
      list.filter((x) => inGroups(x, ["os", UNGROUPED])).map((x) => x.name),
    ).toEqual(["b", "z", "a"]);
  });
  it("keeps a group’s comma through the URL", () => {
    expect(parseGroups(formatGroups(["a,b", "c d"]))).toEqual(["a,b", "c d"]);
    expect(parseGroups(null)).toEqual([]);
  });
});

describe("Run as", () => {
  const profiles: RunProfile[] = [
    { id: "ops", name: "ops", kind: "ssh", privilege: "full", fleets: ["web"] },
    { id: "read-only", name: "nobody", kind: "ssh", privilege: "none", builtin: true },
    { id: "login-user", name: "ssh-paired account", kind: "ssh", privilege: "full", builtin: true },
    { id: "as-the-person", name: "login-user", kind: "kubernetes", builtin: true },
    { id: "sa", name: "sa", kind: "kubernetes" },
  ];
  const elevated: FleetVariables = { values: {}, ceiling: "full" };
  const ids = (o: Parameters<typeof runProfilesFor>[1]) => {
    const r = runProfilesFor(profiles, o);
    return [r.fit?.id, r.options.map((c) => c.id)];
  };

  it("selects the profile that fits the script, built-ins first", () => {
    expect(ids({ onCluster: false, dryRun: false, privilege: "full", fleet: "web", fp: elevated })).toEqual(["login-user", ["login-user", "ops"]]);
    expect(ids({ onCluster: false, dryRun: false, privilege: "none", fleet: "db", fp: elevated })).toEqual(["read-only", ["read-only", "login-user"]]);
    expect(ids({ onCluster: true, dryRun: false, privilege: "", fp: undefined })).toEqual(["as-the-person", ["as-the-person", "sa"]]);
  });

  it("offers nobody alone to a dry run, and under a nobody ceiling only what fits", () => {
    expect(ids({ onCluster: false, dryRun: true, privilege: "full", fleet: "web", fp: elevated })).toEqual(["read-only", ["read-only"]]);
    expect(ids({ onCluster: false, dryRun: false, privilege: "none", fleet: "web", fp: undefined })).toEqual(["read-only", ["read-only"]]);
    expect(ids({ onCluster: false, dryRun: false, privilege: "full", fleet: "web", fp: undefined })).toEqual(["login-user", ["login-user"]]);
  });
});
