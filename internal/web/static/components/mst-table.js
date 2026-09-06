// The painter for the `table` renderer.
//
// **It is a file Task 15 did not name, and its absence was a mechanism
// nothing read.** Five of the six renderers answer with marks and the
// canvas emits them; `table` answers with columns, rows, groups, a sort
// and a pager — a model with no painter anywhere in the plan. Until this
// file existed the most-used renderer in the catalogue could be run,
// sorted, grouped and paged, and could not be put on a screen.
//
// It is the twin's shape carried one step along: `render/table.js` is
// the pure function from an envelope to a table and **this file writes
// none of its words**. Every string below is an interpolation of the
// model — a column's label, a cell's text, a group's caption, the
// pager's sentence — which is what
// TestEveryComponentSpeaksOnlyItsModelsWords (internal/web/
// static_frame_test.go) reads the templates for. The two exceptions are
// the two *controls*: a header is a button and needs a name for what
// pressing it does, which is not a fact about the answer.
//
// **Sorting is client-side and issues no call.** The server returned
// everything the answer has — views.run has no cursor, because a page of
// a graph is not a graph — so re-sorting is a re-render of rows already
// in hand, and a table that fetched to sort would be asking a question
// it already had the answer to.

import { LitElement, css, html, nothing } from "lit";

import { ASCENDING, tableScene } from "../render/table.js";

// What a header's button says it does. It names a **control** and not a
// state of the answer, which is `mst-view-frame`'s own exception for
// "Run anyway" and is here for the same reason.
export const SORT_HINT = "Sort by this column";

export class MstTable extends LitElement {
  static properties = {
    table: { attribute: false },
    // The envelope and parameters, held so a header click can re-run the
    // pure model with a new sort rather than reordering the DOM: the
    // model owns "stable across equal keys", and a component that sorted
    // its own rows would be a second implementation of that rule.
    envelope: { attribute: false },
    params: { attribute: false },
    sort: { attribute: false },
    page: { attribute: false },
  };

  static styles = css`
    /* **The scroll container the sticky headers stick to**, and the
       reason it has a height. An overflow:auto box with no height
       is a scroll container that never scrolls: it grows to its content,
       the header's position:sticky sticks it to a box taller than the
       document's visible area, and the whole thing scrolls out of the
       window with the page. That is how a thousand rows shipped with a
       column header and six group sub-headers that were sticky in the
       stylesheet and stuck to nothing on the screen — found by opening
       one (Task 15). The height matches the drawing's enclosure in
       mst-canvas, because a table and a diagram are the same frame's two
       bodies and a designer switching a view between them should not
       watch the page resize. */
    :host {
      display: block;
      background: var(--paper);
      color: var(--ink);
      font-family: var(--sans);
      overflow: auto;
      max-height: 70vh;
      min-height: 22rem;
    }
    table {
      border-collapse: collapse;
      width: 100%;
    }
    /* Hairline rules and no zebra striping (spec §4.7): a rule is a
       boundary and a stripe is a decoration that competes with the
       game's own colour, which is the one channel this interface has
       already given away. */
    th,
    td {
      border-bottom: 1px solid var(--line);
      padding: 0.3rem 0.6rem;
      text-align: left;
      font-weight: 400;
    }
    thead th {
      position: sticky;
      top: 0;
      background: var(--paper);
      color: var(--muted);
      border-bottom: 1px solid var(--line-strong);
    }
    /* A group's sub-header is sticky too, under the column header, so a
       reader scrolling fast can always see which group they are in. */
    tbody th.group {
      position: sticky;
      top: 1.7rem;
      background: var(--ground);
      color: var(--ink);
    }
    th button {
      all: unset;
      cursor: pointer;
    }
    .serif {
      font-family: var(--serif);
    }
    .mono {
      font-family: var(--mono);
    }
    .muted {
      color: var(--muted);
    }
    /* Absent is marked twice: the em dash the model wrote, and this
       class — because a game may legitimately hold an em dash, and a
       value carried by one signal the content can forge is a value that
       can be forged. */
    .absent {
      color: var(--muted);
    }
    .pager {
      padding: 0.4rem 0.6rem;
      color: var(--muted);
      font-family: var(--mono);
      font-size: 0.8em;
    }
  `;

  render() {
    const table = this.table;
    if (!table) return nothing;
    return html`
      <table>
        <thead>
          <tr>
            ${table.groups ? html`<th></th>` : nothing}
            ${table.columns.map((column) => this.header(column))}
          </tr>
        </thead>
        ${this.body(table)}
      </table>
      ${table.page.text === "" ? nothing : html`<p class="pager">${table.page.text}</p>`}
    `;
  }

  header(column) {
    return html`<th class=${column.style}>
      <button type="button" title=${SORT_HINT} @click=${() => this.sortBy(column)}>${column.label}</button>
    </th>`;
  }

  // The body, with a group's caption and count on a row of its own when
  // the view groups. The caption and the count are the model's, both of
  // them: a component that counted the rows it happened to be given
  // would disagree with the model the moment a page cut a group in two.
  body(table) {
    const starts = new Map();
    for (const group of table.groups || []) starts.set(group.from, group);
    return html`<tbody>
      ${table.page.rows.map((row, index) => {
        const group = starts.get(table.page.from - 1 + index);
        return html`${group
          ? html`<tr>
              <th class="group" colspan=${table.columns.length + 1}>${group.caption} ${group.count}</th>
            </tr>`
          : nothing}
        <tr>
          ${table.groups ? html`<td></td>` : nothing}
          ${row.cells.map(
            (cell, at) =>
              html`<td class=${cellClass(table.columns[at], cell)}>${cell.text}</td>`,
          )}
        </tr>`;
      })}
    </tbody>`;
  }

  // sortBy re-runs the pure model with the reader's column, which is the
  // one place a sort is decided. Pressing the sorted column again
  // reverses it; pressing another starts it ascending, because "sort by
  // this" is not "reverse that".
  sortBy(column) {
    if (!this.envelope) return null;
    const current = this.table && this.table.sort;
    const direction =
      current && current.column === column.key && current.direction === ASCENDING ? "desc" : ASCENDING;
    this.sort = { column: column.key, direction };
    this.table = tableScene(this.envelope, this.params || {}, { sort: this.sort, page: this.page });
    return this.sort;
  }
}

function cellClass(column, cell) {
  const style = column ? column.style : "";
  return cell.absent ? style + " absent" : style;
}

customElements.define("mst-table", MstTable);
