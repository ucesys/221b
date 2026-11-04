import type { SortState } from "../../lib/table";

/** A plain table's heading that sorts by its column. */
export function SortTh({
  col,
  label,
  sort,
  onSort,
}: {
  col: string;
  label: string;
  sort: SortState;
  onSort: (col: string) => void;
}) {
  const on = sort.col === col;
  const dir = sort.dir === "asc" ? "ascending" : "descending";
  return (
    <th aria-sort={on ? dir : "none"} style={{ padding: 0 }}>
      <button
        className="rk-th"
        data-active={on}
        data-col={col}
        title={`${on ? "Sorted" : "Sort"} by ${label.toLowerCase()}`}
        aria-label={`${label}: ${on ? `sorted ${dir}` : "sort"}`}
        onClick={() => onSort(col)}
      >
        {label}
        <span className="rk-arrow" aria-hidden="true">
          {on ? (sort.dir === "asc" ? "↑" : "↓") : ""}
        </span>
      </button>
    </th>
  );
}
