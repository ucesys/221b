import { useMemo, useState } from "react";
import { nextSort, sortRows, type ColumnDef, type SortState } from "./table";

/** A column a plain table sorts by. */
export type SortCol<R> = Pick<ColumnDef<R>, "id" | "sortValue" | "defaultDir">;

// Each table's sort as it was last left, as useTable keeps its own.
const last = new Map<string, SortState>();

/** Sorting for a table with no toolbar: the rows, and what its headings need. */
export function useSorted<R>(
  id: string,
  cols: SortCol<R>[],
  rows: R[],
  initial: SortState,
  now: number,
) {
  const [sort, setSort] = useState(() => last.get(id) ?? initial);
  const shown = useMemo(
    () => sortRows(rows, cols, sort, now),
    [rows, cols, sort, now],
  );
  const onSort = (col: string) => {
    const next = nextSort(cols, sort, col);
    last.set(id, next);
    setSort(next);
  };
  return { sort, onSort, shown };
}
