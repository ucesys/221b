import { describe, expect, it } from "vitest";
import {
  allows,
  approvedOf,
  batches,
  byGroup,
  canApprove,
  countsText,
  missingParams,
  resolveParams,
  runnableOn,
  type Script,
  type ScriptSummary,
  type Version,
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
    const r = resolveParams(params, fp, "n1", { B: "run" });
    expect(r.map((p) => [p.name, p.value, p.source])).toEqual([
      ["A", "node", "node"],
      ["B", "run", "run"],
      ["C", undefined, undefined],
    ]);
    expect(resolveParams(params, fp, "n2", {})[0]).toMatchObject({
      value: "fleet",
      source: "fleet",
    });
  });
  it("counts an empty value as a value", () => {
    expect(
      resolveParams(
        [{ name: "A", required: true }],
        { values: { A: "" } },
        "n",
        {},
      )[0],
    ).toMatchObject({ value: "", source: "fleet" });
  });
  it("lists required parameters with no value, per node", () => {
    expect(missingParams(params, fp, ["n1", "n2"], {})).toEqual([
      "n1: C",
      "n2: C",
    ]);
    expect(missingParams(params, fp, ["n1"], { C: "x" })).toEqual([]);
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
  it("lets each person approve a version in review once", () => {
    expect(canApprove(ver({ state: "draft" }), pat)).toBe(false);
    expect(canApprove(ver({ state: "review" }), pat)).toBe(true);
    const one = ver({ state: "approved", approvals: [{ by: pat, at: "" }] });
    expect(canApprove(one, pat)).toBe(false);
    expect(canApprove(one, sam)).toBe(true);
    expect(
      canApprove(
        ver({
          state: "approved",
          approvals: [
            { by: pat, at: "" },
            { by: sam, at: "" },
          ],
        }),
        { name: "Lee", user: "lee" },
      ),
    ).toBe(false);
  });
  it("runs on a two-person cluster only with two approvals, one not the author’s", () => {
    const byAuthor = ver({
      state: "approved",
      approvals: [{ by: pat, at: "" }],
    });
    expect(runnableOn(byAuthor, false)).toBe(true);
    expect(runnableOn(byAuthor, true)).toBe(false);
    expect(
      runnableOn(
        ver({
          state: "approved",
          approvals: [
            { by: pat, at: "" },
            { by: sam, at: "" },
          ],
        }),
        true,
      ),
    ).toBe(true);
    expect(runnableOn(ver({ state: "review" }), false)).toBe(false);
    expect(runnableOn(undefined, false)).toBe(false);
  });
  it("runs the newest approved version", () => {
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
  it("groups scripts, Ungrouped last", () => {
    const s = (name: string, group: string) =>
      ({ name, group }) as ScriptSummary;
    expect(
      byGroup([s("b", ""), s("z", "os"), s("a", "os"), s("c", "k8s")]).map(
        ([g, l]) => [g, l.map((x) => x.name)],
      ),
    ).toEqual([
      ["k8s", ["c"]],
      ["os", ["a", "z"]],
      ["Ungrouped", ["b"]],
    ]);
  });
});
