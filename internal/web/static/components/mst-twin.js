// The text twin: two tables that are the accessible content of every
// view, the five graphical ones included.
//
// The canvas beside it is `aria-hidden` — an SVG hairball with ARIA
// roles sprinkled over it is navigable by nobody, and pretending
// otherwise is worse than not pretending (interface design spec §8.1) —
// so this is not a compromise version of the view. It *is* the view, for
// a screen reader, for a keyboard, and for a designer on a screen
// narrower than a tablet (§9). It is always in the DOM: never built on
// demand, never behind a toggle that could be off when it is needed.
//
// It is also the keyboard path into the canvas. Every node row is
// focusable, focusing one selects its node, and the selection travels as
// a DOM event so the canvas can highlight what the reader is standing
// on without this component knowing a canvas exists.
//
// **This file writes no sentences.** Every word — each caption, each
// column heading, the em dash of an absent slot, the note on an endpoint
// the query did not draw — comes out of render/twin.js, which is a pure
// function a Node harness reads and a mutation turns red. The rule is
// held mechanically by TestEveryComponentSpeaksOnlyItsModelsWords
// (internal/web/static_frame_test.go), exactly as it is for the frame.
//
// **Every game string arrives as text.** The names, keys, types and
// projected values below are the game's words and a game's words are
// hostile input: this repository has already shipped one stored
// cross-site scripting defect. They reach the DOM as *interpolated
// values in child position* of a Lit template, which Lit renders into
// Text nodes, and they never appear in a template's static HTML. This
// file reaches for no markup sink of any kind — internal/web's
// HTML-sink perimeter (static_sinks_test.go) reads it, and
// TestNoOwnModuleReachesForARawHTMLDirective refuses the Lit-shaped
// spellings the perimeter's DOM-shaped list would not catch. The
// harness asserts the placement itself:
// internal/web/jstest/twin_test.mjs walks the emitted template, finds
// every binding's position in the static markup, and fails if a game
// string is interpolated anywhere but into a text node.

import { LitElement, css, html, nothing } from "lit";

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

  static styles = css`
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
    th,
    td {
      text-align: left;
      padding: 0.25rem 0.75rem;
      border-bottom: 1px solid var(--line);
      vertical-align: top;
    }
    th {
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
  `;

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
