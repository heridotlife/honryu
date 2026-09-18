import type { ReactNode } from 'react';
import useMinWidth from '../hooks/useMinWidth';

/**
 * CardTable (phase 79) -- the one table-or-cards switch every list page
 * opts into. The UX rule: below Tailwind's sm breakpoint a wide table
 * never scrolls sideways as the ONLY mechanism; it becomes a card list of
 * label:value pairs built from the SAME column definitions, so the two
 * branches cannot disagree about what a row says.
 *
 * Branch choice is a real DOM switch (useMinWidth over `(min-width:
 * 640px)`), not CSS visibility: only one branch ever mounts, so a row's
 * data-testid can sit on both the <tr> and the card root without ever
 * appearing twice -- the e2e harness's selectors keep working unchanged
 * (they run at desktop width, where the table branch renders).
 *
 * Parity contract, mode for mode:
 *   - rowTestId        -> <tr> / card root
 *   - cellTestId       -> <td> / the card pair's value slot
 *   - onRowClick       -> tr / card click
 *   - rowClassName     -> tr classes / (cardClassName covers the card)
 *   - actions.render   -> right-aligned last column / inline card footer
 *
 * Phase 86 additions, same discipline (RunCompare's delta table pins its
 * rows/cells by data-metric/data-delta, and its verdict chip lives in the
 * candidate's <th>):
 *   - rowAttributes    -> extra attributes on the <tr> / card root
 *   - cellAttributes   -> extra attributes on the <td> / value slot
 *   - tdClassName      -> td classes, static string OR per-row (delta tone)
 *   - thAttributes + headerExtra -> the <th> ONLY: a column header has no
 *     card counterpart, so below sm headerExtra does not render -- the
 *     per-row cells it summarizes (colored deltas) render in both modes.
 * The primary column (smallest card) becomes the card title and is left
 * out of the label:value pairs, so the title never repeats itself.
 */

export interface CardTableColumn<Row> {
  /** Stable identity for React keys and the card pair's label. */
  key: string;
  /** The <th> text at sm+; the same string labels the card pair below sm. */
  header: string;
  /** Cell content for BOTH modes -- never a <td>, the wrapper owns that. */
  render: (row: Row) => ReactNode;
  /** Below sm this column becomes the card title instead of a pair. */
  primary?: boolean;
  /** Per-row testid for the cell wrapper (<td> at sm+, value slot below). */
  cellTestId?: (row: Row) => string | undefined;
  /** Extra cell attributes (e.g. data-delta) on the <td> and the value slot. */
  cellAttributes?: (row: Row) => Record<string, string | undefined>;
  /** Extra <th> attributes (e.g. data-run-id); table branch only. */
  thAttributes?: Record<string, string | undefined>;
  /** Content rendered in the <th> after the header (verdict chips); table
   * branch only -- never a card pair, so card labels stay plain strings. */
  headerExtra?: ReactNode;
  /** The <th> classes at sm+ (unchanged from the page's old markup). */
  thClassName?: string;
  /** The <td> classes at sm+ -- a static string, or per-row when a cell's
   * tone depends on the row (RunCompare's delta coloring). */
  tdClassName?: string | ((row: Row) => string);
}

export interface CardTableProps<Row> {
  columns: CardTableColumn<Row>[];
  rows: Row[];
  rowKey: (row: Row) => string;
  /** data-testid of the <table> at sm+; the card list gets `${tableTestId}-cards`. */
  tableTestId: string;
  /** The <table> classes at sm+ (unchanged from the page's old markup). */
  tableClassName?: string;
  /** The thead <tr> classes at sm+. */
  headerRowClassName?: string;
  /** The <tbody> classes at sm+ (e.g. the divide-y separator style). */
  tbodyClassName?: string;
  /** Per-row testid on the <tr> at sm+ and the card root below sm. */
  rowTestId?: (row: Row) => string | undefined;
  /** Extra row attributes (e.g. data-metric) on the <tr> and card root. */
  rowAttributes?: (row: Row) => Record<string, string | undefined>;
  /** The <tr> classes at sm+ (separators, hover, selection). */
  rowClassName?: (row: Row) => string;
  /** The card root classes below sm (selection, cursor), after the base card classes. */
  cardClassName?: (row: Row) => string;
  /** Native title tooltip on the <tr> / card root (Clusters-style hints). */
  rowTitle?: (row: Row) => string | undefined;
  /** Row click, fired from the <tr> at sm+ and the card root below sm. */
  onRowClick?: (row: Row) => void;
  /**
   * Row actions: a right-aligned trailing column at sm+ (header defaults
   * to empty, the Tenants roster style), an inline card footer below sm.
   */
  actions?: { header?: string; thClassName?: string; tdClassName?: string; render: (row: Row) => ReactNode };
  /**
   * Shown when rows is empty, mode for mode (a colSpan cell at sm+, a
   * plain line below sm) -- for tables that must stay in the DOM even
   * empty, so tooling hooks keep finding them.
   */
  emptyMessage?: string;
  /** The empty cell's classes at sm+. */
  emptyTdClassName?: string;
}

/** The card list's own data-testid, derived so both branches are findable. */
export function cardListTestId(tableTestId: string): string {
  return `${tableTestId}-cards`;
}

export default function CardTable<Row>({
  columns,
  rows,
  rowKey,
  tableTestId,
  tableClassName = 'w-full text-left text-body-sm',
  headerRowClassName,
  tbodyClassName,
  rowTestId,
  rowAttributes,
  rowClassName,
  cardClassName,
  rowTitle,
  onRowClick,
  actions,
  emptyMessage,
  emptyTdClassName,
}: CardTableProps<Row>) {
  const wide = useMinWidth('(min-width: 640px)');

  if (wide) {
    const columnCount = columns.length + (actions !== undefined ? 1 : 0);
    return (
      <table className={tableClassName} data-testid={tableTestId}>
        <thead>
          <tr className={headerRowClassName}>
            {columns.map((col) => (
              <th key={col.key} scope="col" className={col.thClassName} {...(col.thAttributes ?? {})}>
                {col.header}
                {col.headerExtra}
              </th>
            ))}
            {actions !== undefined && (
              <th scope="col" className={actions.thClassName}>
                {actions.header ?? ''}
              </th>
            )}
          </tr>
        </thead>
        <tbody className={tbodyClassName}>
          {rows.length === 0 && emptyMessage !== undefined ? (
            <tr>
              <td colSpan={columnCount} className={emptyTdClassName}>
                {emptyMessage}
              </td>
            </tr>
          ) : (
            rows.map((row) => (
              <tr
                key={rowKey(row)}
                data-testid={rowTestId?.(row)}
                title={rowTitle?.(row)}
                className={rowClassName?.(row)}
                onClick={onRowClick !== undefined ? () => onRowClick(row) : undefined}
                {...(rowAttributes?.(row) ?? {})}
              >
                {columns.map((col) => (
                  <td
                    key={col.key}
                    className={typeof col.tdClassName === 'function' ? col.tdClassName(row) : col.tdClassName}
                    data-testid={col.cellTestId?.(row)}
                    {...(col.cellAttributes?.(row) ?? {})}
                  >
                    {col.render(row)}
                  </td>
                ))}
                {actions !== undefined && <td className={actions.tdClassName}>{actions.render(row)}</td>}
              </tr>
            ))
          )}
        </tbody>
      </table>
    );
  }

  const primary = columns.find((col) => col.primary);
  const pairs = columns.filter((col) => col !== primary);
  return (
    <ul className="space-y-3" data-testid={cardListTestId(tableTestId)}>
      {rows.length === 0 && emptyMessage !== undefined ? (
        <li className={`text-body-sm text-slate-500 dark:text-slate-400 ${emptyTdClassName ?? ''}`} data-testid={cardListTestId(tableTestId) + '-empty'}>
          {emptyMessage}
        </li>
      ) : (
        rows.map((row) => (
          <li
            key={rowKey(row)}
            data-testid={rowTestId?.(row)}
            title={rowTitle?.(row)}
            onClick={onRowClick !== undefined ? () => onRowClick(row) : undefined}
            className={`rounded-xl border border-slate-200 p-3 dark:border-slate-700 ${cardClassName?.(row) ?? ''}`}
            {...(rowAttributes?.(row) ?? {})}
          >
            {primary !== undefined && (
              <div className="text-body-sm font-medium text-slate-900 dark:text-white">{primary.render(row)}</div>
            )}
            {pairs.length > 0 && (
              <dl className="mt-2 space-y-1.5">
                {pairs.map((col) => (
                  <div key={col.key} className="flex items-baseline justify-between gap-3">
                    <dt className="shrink-0 text-caption text-slate-500 dark:text-slate-400">{col.header}</dt>
                    <dd
                      className="min-w-0 text-right text-body-sm"
                      data-testid={col.cellTestId?.(row)}
                      {...(col.cellAttributes?.(row) ?? {})}
                    >
                      {col.render(row)}
                    </dd>
                  </div>
                ))}
              </dl>
            )}
            {actions !== undefined && (
              <div className="mt-3 flex justify-end border-t border-slate-100 pt-2 dark:border-slate-700/50">
                {actions.render(row)}
              </div>
            )}
          </li>
        ))
      )}
    </ul>
  );
}
