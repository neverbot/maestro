// The frame every renderer sits in: title strip, parameter bar, banner
// stack, the drawing, footer strip. One component for all six drawings,
// so the negative states are designed once and cannot drift between
// them.
//
// **This file writes no sentences.** Every word a designer reads comes
// out of the model `render/scene.js` builds — which is also where the
// server's own sentences are carried across verbatim — and this file
// only decides where each string sits and what it is painted with. The
// rule is held mechanically by
// TestTheViewFrameSpeaksOnlyTheModelsWords (internal/web/static_frame_test.go):
// every text node in the templates below is either whitespace or an
// interpolation, so a word typed into a template here is a failing test
// rather than a second copy of a sentence.
//
// It fetches nothing. The one action the diagnostics panel offers is
// dispatched through scene.js's runAction against the client the page
// hands it, which keeps internal/web/static/client.js the only module in
// this front end that reaches the network.

import { LitElement, css, html, nothing } from "lit";

import { ACTION_RUN_BEST_EFFORT, KIND_DIAGNOSTICS, KIND_EMPTY, KIND_UNBOUND, runAction } from "../render/scene.js";
import "./mst-twin.js";

// The label of the one action, and the only human words in this file.
// It is here rather than in the model because it names a *control* and
// not a state of the answer: scene.js is a function from an envelope to
// what is true about it, and "what the button says" is not that.
const RUN_ANYWAY_LABEL = "Run anyway (best effort)";

export class MstViewFrame extends LitElement {
  static properties = {
    frame: { attribute: false },
    client: { attribute: false },
    params: { attribute: false },
  };

  static styles = css`
    :host {
      display: block;
      background: var(--paper);
      color: var(--ink);
      font-family: var(--sans);
    }
    .strip {
      display: flex;
      flex-wrap: wrap;
      gap: 0.75rem;
      align-items: baseline;
      padding: 0.5rem 0.75rem;
      border-bottom: 1px solid var(--line);
    }
    .name {
      font-family: var(--serif);
      font-size: 1.1rem;
    }
    .key,
    .pointer,
    .spelling {
      font-family: var(--mono);
      font-size: 0.85em;
      color: var(--muted);
    }
    .renamed {
      flex-basis: 100%;
      color: var(--muted);
      font-size: 0.9em;
    }
    .bar {
      display: flex;
      flex-wrap: wrap;
      gap: 0.75rem;
      padding: 0.5rem 0.75rem;
      border-bottom: 1px solid var(--line);
    }
    .control label {
      display: block;
      font-size: 0.8em;
      color: var(--muted);
    }
    .control.marked label {
      color: var(--danger);
    }
    .control.marked input,
    .control.marked select {
      border-color: var(--danger);
    }
    .banner {
      padding: 0.4rem 0.75rem;
      border-bottom: 1px solid var(--line);
      font-size: 0.9em;
    }
    .banner ul,
    .panel ul {
      margin: 0.25rem 0 0;
      padding-left: 1rem;
      list-style: none;
    }
    .swatch {
      display: inline-block;
      width: 0.6rem;
      height: 0.6rem;
      margin-right: 0.4rem;
      background: var(--dropped);
    }
    .panel {
      padding: 0.75rem;
      color: var(--ink);
      border-left: 3px solid var(--danger);
    }
    .panel .message {
      color: var(--danger);
    }
    .empty {
      padding: 2rem 0.75rem;
      text-align: center;
      color: var(--muted);
    }
    /* The drawing is hidden from assistive technology and the twin is
       not. Nothing about an SVG scene is navigable without sight, and a
       canvas decorated with ARIA roles would be a second, worse twin
       that nobody can read; the honest arrangement is one accessible
       representation and one that says it is decoration. */
    .canvas {
      display: block;
      /* The drawing's enclosure, and the reason it is here rather than
         on any page: mst-canvas is position:absolute inset:0, so
         it needs a positioned box with a real height to fill, and a
         frame that slotted a canvas into a zero-height block would
         render six hundred kilobytes of correct SVG that nobody can
         see. Found by mounting the first view (Task 15). The height is
         viewport-relative because a diagram is a thing you look *at*,
         and the floor is what keeps it usable in a short window. */
      position: relative;
      height: 70vh;
      min-height: 22rem;
    }
    .footer {
      padding: 0.4rem 0.75rem;
      border-top: 1px solid var(--line);
      color: var(--muted);
      font-size: 0.8em;
      font-family: var(--mono);
    }
  `;

  render() {
    const frame = this.frame;
    if (!frame) return nothing;
    return html`
      ${this.titleStrip(frame.title)} ${this.parameterBar(frame.bar)}
      ${(frame.banners || []).map((banner) => this.band(banner))} ${this.body(frame)}
      ${this.footerStrip(frame.footer)}
    `;
  }

  titleStrip(title) {
    if (!title) return nothing;
    return html`
      <div class="strip">
        <span class="name">${title.name}</span><span class="key">${title.key}</span
        ><span class="renderer">${title.renderer}</span>
        ${title.renamed
          ? html`<details class="renamed">
              <summary>${title.renamed.text}</summary>
              <ul>
                ${title.renamed.rows.map(
                  (row) =>
                    html`<li>
                      <span class="pointer">${row.pointer}</span
                      ><span class="spelling">${row.was}</span
                      ><span class="spelling">${row.now}</span>
                    </li>`,
                )}
              </ul>
            </details>`
          : nothing}
      </div>
    `;
  }

  parameterBar(bar) {
    if (!bar || !bar.present) return nothing;
    return html`
      <div class="bar">
        ${bar.controls.map(
          (control) => html`
            <div class=${control.marked ? "control marked" : "control"}>
              <label for=${"p-" + control.key}>${control.key}</label>
              ${control.options
                ? html`<select id=${"p-" + control.key} .value=${control.value ?? ""}>
                    ${control.options.map((option) => html`<option value=${option}>${option}</option>`)}
                  </select>`
                : html`<input
                    id=${"p-" + control.key}
                    type=${control.type === "number" ? "number" : "text"}
                    .value=${control.value ?? ""}
                  />`}
              ${control.marked ? html`<span class="message">${control.message}</span>` : nothing}
            </div>
          `,
        )}
      </div>
    `;
  }

  band(banner) {
    return html`
      <div class="banner" data-code=${banner.code}>
        <span>${banner.text}</span>
        ${banner.rows && banner.rows.length > 0
          ? html`<ul>
              ${banner.rows.map(
                (row) => html`<li>
                  <span class="swatch"></span><span class="pointer">${row.pointer}</span
                  ><span class="spelling">${row.was}</span><span>${row.message}</span>
                </li>`,
              )}
            </ul>`
          : nothing}
      </div>
    `;
  }

  // The picture, or what stands in its place. A stale refusal under the
  // failing policy gets **no drawing at all** — the slot is not rendered
  // — which is the whole point of that default: a diagram that silently
  // dropped its level filter looks exactly like a correct diagram.
  body(frame) {
    if (frame.kind === KIND_DIAGNOSTICS) return this.panel(frame);
    if (frame.kind === KIND_UNBOUND) return nothing;
    if (frame.kind === KIND_EMPTY) return html`${this.emptyAnswer(frame)}${this.twin(frame)}`;
    return html`<div class="canvas" aria-hidden="true"><slot></slot></div>${this.twin(frame)}`;
  }

  // The twin is rendered for every answer, in the same place, whichever
  // of the six renderers is in the slot above it — which is the whole of
  // "every view has a text twin" (spec §8.1). It is not rendered for a
  // refusal: there is no answer to describe, and a pair of empty tables
  // under a panel of diagnostics would read as an answer that matched
  // nothing, which is the one thing the frame is most careful never to
  // let a refusal look like.
  twin(frame) {
    if (!frame.twin) return nothing;
    return html`<mst-twin .twin=${frame.twin}></mst-twin>`;
  }

  panel(frame) {
    return html`
      <div class="panel">
        <ul>
          ${frame.diagnostics.rows.map(
            (row) => html`<li>
              <span class="message">${row.message}</span>
              <span class="pointer">${row.pointer}</span><span class="spelling">${row.was}</span
              ><span class="spelling">${row.now}</span>
            </li>`,
          )}
        </ul>
        ${(frame.actions || []).map((action) =>
          action.code === ACTION_RUN_BEST_EFFORT
            ? html`<button type="button" @click=${() => this.run(action)}>${RUN_ANYWAY_LABEL}</button>`
            : nothing,
        )}
      </div>
    `;
  }

  emptyAnswer(frame) {
    const ran = frame.ran || { renderer: "", sets: [], params: [] };
    return html`
      <div class="empty">
        <p>${frame.message}</p>
        <p class="key">
          ${ran.renderer} ${ran.sets.join(", ")}
          ${ran.params.map((param) => html`<span>${param.key}</span><span>${String(param.value)}</span>`)}
        </p>
      </div>
    `;
  }

  footerStrip(footer) {
    if (!footer) return nothing;
    return html`<div class="footer">${footer.text}</div>`;
  }

  run(action) {
    return runAction(action, this.client, {
      key: this.frame && this.frame.title ? this.frame.title.key : "",
      params: this.params || {},
    });
  }
}

customElements.define("mst-view-frame", MstViewFrame);
