import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  AutomationFleets,
  AutomationIndex,
  Run,
  Script,
} from "../api/automation";
import type { Me } from "../api/types";
import type { Route } from "../lib/router";
import { AutomationTab, rowProblems } from "./AutomationTab";

const admin = {
  mode: "local",
  user: { subject: "pat", username: "pat" },
  admin: true,
  method: "local",
  sessionIdleSeconds: 0,
} as Me;
const route = (path: string[] = []): Route => ({
  cluster: "local",
  view: "tabs",
  tab: "automation",
  path,
  params: new URLSearchParams(),
});

const header = {
  describe: "Keep chrony",
  params: [{ name: "PACKAGE", required: true }],
  privilege: "full" as const,
  dryRun: true,
};
const script: Script = {
  name: "ensure-chrony",
  group: "os",
  created: "2026-10-30T10:00:00Z",
  updated: "2026-10-31T10:00:00Z",
  versions: [
    {
      n: 1,
      text: "#v1",
      hash: "h1",
      header,
      findings: [],
      state: "approved",
      author: { name: "Pat", user: "pat" },
      created: "2026-10-30T10:00:00Z",
      approvals: [
        { by: { name: "Pat", user: "pat" }, at: "2026-10-30T11:00:00Z" },
      ],
    },
    {
      n: 2,
      text: "#v2",
      hash: "h2",
      header,
      findings: [
        { level: "warning", line: 3, code: "reboot", msg: "it reboots" },
      ],
      state: "draft",
      author: { name: "Pat", user: "pat" },
      created: "2026-10-31T10:00:00Z",
    },
  ],
};
const index: AutomationIndex = {
  off: "",
  me: { name: "Pat", user: "pat" },
  running: 0,
  limits: { text: 32768, timeout: 60, parallel: 20, nodes: 500 },
  scripts: [
    {
      name: "ensure-chrony",
      group: "os",
      describe: "Keep chrony",
      version: 2,
      state: "draft",
      approved: 1,
      approvals: 1,
      privilege: "full",
      errors: 0,
      warnings: 1,
      created: script.created,
      updated: script.updated,
    },
  ],
};
const fleets: AutomationFleets = {
  cluster: "local",
  production: false,
  twoPerson: false,
  fleets: [
    {
      id: "workers",
      name: "Workers",
      nodes: [
        { name: "w1", paired: true, loginUser: "root" },
        { name: "w2", paired: false, why: "not paired" },
      ],
      params: { values: { PACKAGE: "chrony" }, ceiling: "full" },
    },
  ],
};
const run: Run = {
  id: 7,
  script: "ensure-chrony",
  version: 1,
  hash: "h1",
  dryRun: false,
  privilege: "full",
  cluster: "local",
  fleet: "workers",
  fleetName: "Workers",
  rollout: { mode: "canary", stopAfter: 1, timeoutMinutes: 10 },
  by: { name: "Pat" },
  state: "succeeded",
  started: "2026-10-31T10:00:00Z",
  ended: "2026-10-31T10:00:30Z",
  beat: "2026-10-31T10:00:30Z",
  nodes: [
    { node: "w1", batch: 0, state: "changed", exit: 0, msg: "installed" },
  ],
};

let calls: { url: string; method: string; body?: unknown }[] = [];
beforeEach(() => {
  calls = [];
  Element.prototype.scrollTo = () => {};
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = init.method ?? "GET";
      calls.push({
        url,
        method,
        body: init.body ? JSON.parse(init.body as string) : undefined,
      });
      const ok = (v: unknown) =>
        new Response(JSON.stringify(v), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      if (url === "/api/v1/automation") return ok(index);
      if (url === "/api/v1/automation/runs" && method === "GET")
        return ok({ runs: [run] });
      if (url === "/api/v1/automation/runs" && method === "POST")
        return ok({ ...run, id: 8, state: "running" });
      if (url.startsWith("/api/v1/automation/runs/"))
        return ok({
          run,
          output: { w1: 'hello from w1\n{"changed": true}' },
          live: false,
        });
      if (url === "/api/v1/automation/scripts/ensure-chrony") return ok(script);
      if (url === "/api/v1/automation/check")
        return ok({ header, findings: [] });
      if (url === "/api/v1/automation/fleets") return ok(fleets);
      return new Response(JSON.stringify({ error: "unexpected " + url }), {
        status: 500,
      });
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const props = (path?: string[]) => ({
  cluster: null,
  now: Date.parse("2026-10-31T10:01:00Z"),
  onToast: vi.fn(),
  route: route(path),
  me: admin,
});

describe("AutomationTab", () => {
  it("says the tab is a SuperAdmin’s to anyone else", () => {
    render(<AutomationTab {...props()} me={{ ...admin, admin: false }} />);
    expect(screen.getByTestId("automation-denied").textContent).toContain(
      "Only a SuperAdmin",
    );
  });

  it("lists scripts by group with their state and last run", async () => {
    render(<AutomationTab {...props()} />);
    const row = await screen.findByTestId("script-row");
    expect(row.textContent).toContain("ensure-chrony");
    expect(row.textContent).toContain("v2 draft");
    expect(row.textContent).toContain("v1, 1 approval");
    expect(row.textContent).toContain("1 warnings");
  });

  it("starts a run of the approved version with the fleet’s paired nodes", async () => {
    render(<AutomationTab {...props()} />);
    fireEvent.click(await screen.findByTestId("script-run"));
    const dialog = await screen.findByTestId("run-dialog");
    await waitFor(() =>
      expect(within(dialog).getByTestId("run-nodes").textContent).toContain(
        "w2",
      ),
    );
    expect(
      within(dialog)
        .getByTestId("run-param-PACKAGE")
        .getAttribute("placeholder"),
    ).toBe("chrony");
    const start = within(dialog).getByTestId("run-start") as HTMLButtonElement;
    expect(start.disabled).toBe(false);
    fireEvent.click(start);
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url === "/api/v1/automation/runs",
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.url === "/api/v1/automation/runs",
    )!;
    expect(post.body).toMatchObject({
      script: "ensure-chrony",
      dryRun: false,
      fleet: "workers",
      rollout: { mode: "canary", stopAfter: 1, timeoutMinutes: 10 },
    });
    expect((post.body as { nodes?: string[] }).nodes).toBeUndefined();
    expect(await screen.findByTestId("run-drawer")).toBeTruthy();
  });

  it("refuses a full script on a fleet whose ceiling is none", async () => {
    fleets.fleets[0]!.params.ceiling = "none";
    try {
      render(<AutomationTab {...props()} />);
      fireEvent.click(await screen.findByTestId("script-run"));
      const blocked = await screen.findByTestId("run-blocked");
      expect(blocked.textContent).toContain("allows only none");
      expect(
        (screen.getByTestId("run-start") as HTMLButtonElement).disabled,
      ).toBe(true);
    } finally {
      fleets.fleets[0]!.params.ceiling = "full";
    }
  });

  it("shows a run’s nodes and output", async () => {
    render(<AutomationTab {...props(["runs"])} />);
    fireEvent.click(await screen.findByTestId("run-row"));
    const out = await screen.findByTestId("run-output");
    await waitFor(() => expect(out.textContent).toContain("hello from w1"));
    expect(screen.getByTestId("run-state").textContent).toBe("succeeded");
  });
});

describe("rowProblems", () => {
  it("checks names as the server does", () => {
    expect(rowProblems([{ k: "GOOD", v: "" }])).toEqual([]);
    expect(
      rowProblems([
        { k: "bad-name", v: "" },
        { k: "NODE", v: "" },
        { k: "X", v: "" },
        { k: "X", v: "" },
      ]),
    ).toEqual([
      "bad-name: a name is capitals, digits and underscores",
      "NODE is set by the run",
      "X is there twice",
    ]);
  });
});
