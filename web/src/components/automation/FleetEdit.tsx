import { saveFleets } from "../../api/fleets";
import { useLiveMeta, useView } from "../../api/live";
import type { Fleet, NodeRow } from "../../api/types";
import { FleetEditor, type EditorSeed } from "../FleetEditor";

/** The Nodes page's fleet editor, for a new fleet or one to change. */
export function FleetEdit({
  cluster,
  seed,
  now,
  onClose,
  onSaved,
  onDeleted,
}: {
  cluster: string;
  seed: EditorSeed;
  now: number;
  onClose: () => void;
  onSaved: (f: Fleet, isNew: boolean) => Promise<void>;
  onDeleted: (f: Fleet) => Promise<void>;
}) {
  const { rows } = useView<NodeRow>("nodes");
  const defs = useLiveMeta().settings?.fleets?.[cluster] ?? [];
  return (
    <FleetEditor
      seed={seed}
      fleets={defs}
      nodes={rows}
      now={now}
      onClose={onClose}
      onSave={async (f, isNew) => {
        await saveFleets(
          cluster,
          isNew ? [...defs, f] : defs.map((x) => (x.id === f.id ? f : x)),
        );
        await onSaved(f, isNew);
      }}
      onDelete={async (f) => {
        await saveFleets(
          cluster,
          defs.filter((x) => x.id !== f.id),
        );
        await onDeleted(f);
      }}
    />
  );
}
