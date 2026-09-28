// The text twin: two tables that are the accessible content of every
// view, the five graphical ones included.

import { LitElement, css, html, nothing, unsafeCSS } from "lit";

import { CONTROL_CSS, adoptControlStyles } from "./control-styles.js";

// The event a focused row fires. It is a bare event with the node's two
// keys on it — never the entity id, which execute.go says is not an
// address — and it bubbles out of the shadow root so the page can wire
// the twin to a canvas without either of them importing the other.
export const SELECT_EVENT = "mst-select";

export class MstTwin extends LitElement {
  static properties = {
    twin: { attribute: false },
    selected: { state: true },
  };

  static styles = [
    unsafeCSS(CONTROL_CSS),
    css`
    :host {
      display: block;
      font-family: var(--sans);
      color: var(--ink);
    }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.85rem;
    }
    caption {
      text-align: left;
      padding: 0.4rem 0.75rem;
      color: var(--muted);
      font-family: var(--mono);
      font-size: 0.8rem;
    }
    /* The product's row height, as the catalogue and the table renderer
       have it: these were 29.4px against a 36px token, which made one
       shape three heights across three files. */
    th,
    td {
      text-align: left;
      box-sizing: border-box;
      height: var(--row-h);
      padding: 0 11px;
      line-height: 20px;
      border-bottom: 1px solid var(--line);
      vertical-align: middle;
    }
    th {
      height: var(--row-compact-h);
      color: var(--muted);
      font-weight: normal;
      font-family: var(--mono);
      font-size: 0.8em;
    }
    /* An absent slot is told apart from an empty one twice: by the mark
       the model puts in the cell, and by this. A game may hold an em
       dash as a value, so the mark alone is not enough. */
    td.absent {
      color: var(--muted);
    }
    .note {
      margin-left: 0.4rem;
      color: var(--muted);
      font-size: 0.9em;
    }
    tr[tabindex]:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: -2px;
    }
    tr[aria-current="true"] {
      background: var(--ground);
    }
  `,
  ];

  // The shared control vocabulary is a fetched sheet rather than a
  // string, so it cannot be spread into `static styles`; `CONTROL_CSS`
  // there is only the two rules a page stylesheet cannot express.
  firstUpdated() {
    adoptControlStyles(this.renderRoot);
  }


  render() {
    const twin = this.twin;
    if (!twin) return nothing;
    return html`${this.nodeTable(twin.nodes)}${this.edgeTable(twin.edges)}`;
  }

  nodeTable(table) {
    if (!table) return nothing;
    return html`
      <table>
        <caption>
          ${table.caption}
        </caption>
        ${this.head(table.columns)}
        <tbody>
          ${table.rows.map(
            (row) => html`<tr
              tabindex="0"
              aria-current=${this.selected === row.key ? "true" : "false"}
              @focus=${() => this.select(row)}
            >
              ${row.cells.map((cell) => this.cell(cell))}
            </tr>`,
          )}
        </tbody>
      </table>
    `;
  }

  // The edge rows are not focusable. A focused row selects the node it
  // describes and an edge describes two, so there is no one answer for
  // it to send; a table is navigable by a screen reader without a tab
  // stop on every row, and adding one that selects nothing would be a
  // stop that does nothing.
  edgeTable(table) {
    if (!table) return nothing;
    return html`
      <table>
        <caption>
          ${table.caption}
        </caption>
        ${this.head(table.columns)}
        <tbody>
          ${table.rows.map((row) => html`<tr>
            ${row.cells.map((cell) => this.cell(cell))}
          </tr>`)}
        </tbody>
      </table>
    `;
  }

  head(columns) {
    return html`<thead>
      <tr>
        ${columns.map((column) => html`<th scope="col">${column.label}</th>`)}
      </tr>
    </thead>`;
  }

  cell(cell) {
    return html`<td class=${cell.absent ? "absent" : ""}>
      ${cell.text}${cell.note ? html`<span class="note">${cell.note}</span>` : nothing}
    </td>`;
  }

  // select is what focusing a node row does: it marks the row here, so
  // the reader can see where they are standing, and it announces the
  // node so a canvas can highlight it. Both halves matter — the mark is
  // the half that works today, when no canvas exists yet.
  select(row) {
    this.selected = row.key;
    this.dispatchEvent(
      new CustomEvent(SELECT_EVENT, { detail: row.node, bubbles: true, composed: true }),
    );
  }
}

customElements.define("mst-twin", MstTwin);
