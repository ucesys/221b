// The facts read from nodes (Automation): kept apart from api/automation.ts,
// which imports a script as text, so the columns and menus that use them
// load where no bundler runs (the browser tests).
import { nWord } from "../lib/format";
import { apiPath } from "../lib/router";
import { call, post } from "./call";

export interface NodeFacts {
  cluster: string;
  node: string;
  at: string;
  facts?: Record<string, string>;
  error?: string;
}

export const fetchFacts = (cluster: string) =>
  call<{ nodes: NodeFacts[]; keys: string[] }>(
    apiPath("automation/facts", cluster),
  );
export const refreshFacts = (cluster: string, nodes?: string[]) =>
  call<{ nodes: NodeFacts[] }>(
    apiPath("automation/facts", cluster),
    post({ nodes }),
  );

/**
 * Reads the facts of these nodes (the picked ones, a fleet's) and says how
 * it went, for a toast: the server asks only nodes paired for SSH and in a
 * fleet, and the others are counted as skipped.
 */
export async function collectFacts(
  cluster: string,
  nodes: string[],
): Promise<string> {
  try {
    const got = (await refreshFacts(cluster, nodes)).nodes;
    const bad = got.filter((n) => n.error).length;
    const skipped = nodes.length - got.length;
    return [
      `Collected facts from ${got.length - bad} of ${nWord(nodes.length, "node", "nodes")}.`,
      bad ? `${bad} did not answer.` : "",
      skipped ? `${skipped} skipped: not paired for SSH, or in no fleet.` : "",
    ]
      .filter(Boolean)
      .join(" ");
  } catch (e) {
    return `Could not collect facts: ${e instanceof Error ? e.message : String(e)}`;
  }
}
