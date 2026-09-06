// The ground: choosing a background image, placing it, and clearing it.
//
// The second of Task 14's two writes, and the only place in this front
// end where a designer hands the server bytes.
//
// **The picker states the refusal before a file is chosen.** Eight
// megabytes, PNG/JPEG/WebP, and *SVG is refused because an SVG served to
// a browser can carry script*. A picker that waited for a designer to
// find a 30 MB PNG, upload it over a hotel connection and then read a
// sentence about it has spent their time to say something it knew all
// along. The three facts are the server's own — the byte count is
// internal/views' MaxAssetBytes and the formats are its three sniffed
// mimes — and internal/web/static_ground_test.go pins them against those
// values, so a bound that changes on the server cannot leave a stale
// promise on the picker.
//
// What this file does **not** do is compose a refusal. A rejected upload
// shows the server's own message, character for character, exactly as
// client.js carried it. The picker's sentences are preconditions a
// designer reads before acting; a refusal's sentence belongs to whoever
// refused.
//
// **Placement is a mode.** While *adjust ground* is active the image
// moves and scales and the nodes hold still, because a designer aligning
// an image to a graph is moving one of the two and an interface that
// moved both would be asking them to do it by feel. Committing writes
// one `set_background {asset_id, scale, offset}`; cancelling writes
// nothing at all, which is what makes the mode safe to enter. Clearing
// sends `asset_id: null` — the only spelling for it — behind a
// confirmation, because internal/views' RemoveAsset resets the scale and
// the offset with it and there is nothing to put back.
//
// **This component fetches nothing.** Every call goes through the data
// client it is handed, which is what internal/web/static_client_test.go's
// perimeter checks and what keeps this file a function from state to a
// small tree.
//
// It is not a LitElement, for mst-canvas.js's reason carried one step
// along: it lives inside that component's floating panel slot, its
// sentences must arrive as character data rather than as markup, and
// building with `createElement` and `textContent` means nothing on this
// path ever parses a string. A designer's own filename is a game string
// like any other.

import { CLASS_PENDING, worldDelta } from "./mst-canvas.js";

// The bounds, in the server's own numbers.
//
// MAX_ASSET_BYTES is internal/views.MaxAssetBytes; the three mimes are
// its MimePNG, MimeJPEG and MimeWebP. They are spelled here because a
// picker cannot ask the server what it will refuse without uploading
// something first, and they are pinned to those values by a Go guard
// rather than left to drift.
export const MAX_ASSET_BYTES = 8 << 20;
export const MIME_PNG = "image/png";
export const MIME_JPEG = "image/jpeg";
export const MIME_WEBP = "image/webp";
export const ACCEPTED_MIMES = [MIME_PNG, MIME_JPEG, MIME_WEBP];

// The three facts, in the order a designer meets them: how big, which
// formats, and the one format they will try that is refused for a reason
// they cannot guess.
export const FACT_SIZE =
  `A background image may weigh at most 8 MB (${MAX_ASSET_BYTES} bytes). ` +
  "Scale it down or save it at a lower quality if it is larger.";
export const FACT_FORMATS =
  `The accepted formats are ${MIME_PNG}, ${MIME_JPEG} and ${MIME_WEBP}. ` +
  "The format is read from the bytes themselves, never from the file's " +
  "name or the type it is sent with.";
export const FACT_SVG =
  "SVG is refused: an SVG served to a browser can carry script, and a map " +
  "is a raster image anyway. Export it as a PNG, a JPEG or a WebP.";
export const FACTS = [FACT_SIZE, FACT_FORMATS, FACT_SVG];

// The words on the controls, and the one confirmation.
export const LABEL_UPLOAD = "Upload this image";
export const LABEL_ADJUST = "Adjust ground";
export const LABEL_COMMIT = "Save this placement";
export const LABEL_CANCEL = "Cancel";
export const LABEL_CLEAR = "Remove the background";
export const CONFIRM_CLEAR =
  "Removing the background clears its scale and its offset too, and there " +
  "is nothing to put back. Remove it?";
export const LABEL_CONFIRM_CLEAR = "Yes, remove it";

export const CLASS_GROUND = "ground";
export const CLASS_FACT = "fact";
export const CLASS_BAND = "band";
export const CLASS_CONFIRM = "confirm";

// The action identifiers, so a caller asks by identity rather than by
// matching a label it also renders.
export const ACTION_UPLOAD = "upload";
export const ACTION_ADJUST = "adjust";
export const ACTION_COMMIT = "commit";
export const ACTION_CANCEL = "cancel";
export const ACTION_CLEAR = "clear";
export const ACTION_CONFIRM_CLEAR = "confirm-clear";

// The scale and the offset a view with no placement of its own gets.
// internal/views defaults to exactly these, and naming them here is what
// keeps "the designer moved nothing" and "the designer moved it back to
// the origin" one answer rather than two.
export const DEFAULT_SCALE = 1;

export class MstGround extends HTMLElement {
  constructor(options = {}) {
    super();
    this.doc = options.document || this.ownerDocument || globalThis.document;
    this.client = options.client || null;
    this.canvas = options.canvas || null;
    this.viewKey = options.viewKey || "";
    // The chosen file, before it is anything on the server.
    this.file = null;
    // The uploaded asset this view would point at: `{id, url, width,
    // height}` as api_view_assets.go answers it.
    this.asset = options.asset || null;
    // What the view already says about its ground: the scale and the
    // offset it is drawn at now. Adjusting the *same* image starts from
    // these; adjusting a new one starts from the defaults, because
    // internal/views' own contract is that a caller naming a new image
    // and saying nothing about the arithmetic must not inherit the
    // previous image's.
    this.background = normaliseBackground(options.background);
    // The placement being adjusted. Null outside the mode, which is what
    // makes "cancelling writes nothing" a thing there is a state for.
    this.placing = null;
    this.pending = false;
    this.band = null;
    this.confirming = false;
    this.root = this.doc.createElement("div");
    const shadow = this.attachShadow({ mode: "open" });
    if (shadow && typeof shadow.appendChild === "function") shadow.appendChild(this.root);
    this.render();
  }

  // --- The picker ----------------------------------------------------

  // choose records the file a designer picked and writes nothing. The
  // three facts are already on screen; this is where a designer finds
  // out whether *their* file meets them, and it still costs no upload.
  choose(file) {
    this.file = file || null;
    this.band = null;
    this.render();
    return this.file;
  }

  // upload sends the bytes. A refusal is the server's sentence, verbatim.
  async upload() {
    if (!this.file) return null;
    const answer = await this.settle(() => this.client.uploadAsset(this.file, this.file.name));
    if (answer && answer.ok) {
      this.asset = answer.result;
      this.file = null;
      // Uploading is not placing: the bytes are on the server and the
      // view still names whatever it named. Entering the mode is the
      // designer's next act, and the write is theirs after that.
      this.adjust(this.asset);
    }
    return answer;
  }

  // --- The placement mode --------------------------------------------

  // adjust enters the mode. It writes nothing: the numbers accumulate
  // here and reach the server only on commit.
  adjust(asset) {
    const target = asset || this.asset;
    if (!target) return null;
    const sameImage = this.asset !== null && this.asset.id === target.id && this.background.assetId === target.id;
    const start = sameImage
      ? { scale: this.background.scale, offset: { ...this.background.offset } }
      : { scale: DEFAULT_SCALE, offset: { x: 0, y: 0 } };
    this.asset = target;
    this.placing = { assetId: target.id, start, scale: start.scale, offset: { ...start.offset } };
    this.band = null;
    this.render();
    return this.placing;
  }

  // moveBy takes a **pointer** delta and stores a **game** one. The
  // division by the zoom is mst-canvas.js's worldDelta, asked rather than
  // repeated: an offset written in screen pixels would place the image
  // differently for every designer's window.
  moveBy(dx, dy) {
    if (!this.placing) return null;
    const zoom = this.canvas && this.canvas.view ? this.canvas.view.k : 1;
    const moved = worldDelta(dx, dy, zoom);
    this.placing.offset = {
      x: this.placing.offset.x + moved.dx,
      y: this.placing.offset.y + moved.dy,
    };
    if (this.canvas) this.canvas.adjustGround({ dx: moved.dx, dy: moved.dy });
    return this.placing.offset;
  }

  // scaleBy multiplies the scale. One number for both axes, because
  // `background_scale` is one column: the image keeps its own aspect
  // ratio and this interface does not offer to change it.
  scaleBy(factor) {
    if (!this.placing || !Number.isFinite(factor) || factor <= 0) return null;
    this.placing.scale *= factor;
    if (this.canvas) this.canvas.adjustGround({ factor });
    return this.placing.scale;
  }

  // commit is the write: one call, on leaving the mode.
  async commit() {
    if (!this.placing) return null;
    const placing = this.placing;
    const answer = await this.settle(() =>
      this.client.writeBackground(this.viewKey, {
        assetId: placing.assetId,
        scale: placing.scale,
        offset: placing.offset,
      }),
    );
    if (answer && answer.ok) {
      this.background = normaliseBackground({
        asset_id: placing.assetId,
        scale: placing.scale,
        offset: placing.offset,
      });
      this.placing = null;
    }
    this.render();
    return answer;
  }

  // cancel leaves the mode having written nothing, and puts the image
  // back where the mode found it.
  cancel() {
    if (!this.placing) return null;
    const { offset, scale, start } = this.placing;
    if (this.canvas) {
      this.canvas.adjustGround({ factor: start.scale / scale });
      this.canvas.adjustGround({ dx: start.offset.x - offset.x, dy: start.offset.y - offset.y });
    }
    this.placing = null;
    this.render();
    return null;
  }

  // --- Clearing ------------------------------------------------------

  // clear removes the background, behind a confirmation. The first call
  // arms the confirmation and writes nothing; only a call that says it
  // is confirmed sends `asset_id: null`.
  async clear(options = {}) {
    if (!options.confirmed) {
      this.confirming = true;
      this.render();
      return null;
    }
    this.confirming = false;
    const answer = await this.settle(() => this.client.writeBackground(this.viewKey, { assetId: null }));
    if (answer && answer.ok) {
      this.asset = null;
      this.placing = null;
      this.background = normaliseBackground(null);
    }
    this.render();
    return answer;
  }

  // --- The plumbing --------------------------------------------------

  // settle wears the in-flight treatment around one call and takes the
  // server's own sentence out of it. It composes no words.
  async settle(call) {
    this.band = null;
    this.pending = true;
    this.render();
    let answer;
    try {
      answer = await call();
    } finally {
      this.pending = false;
    }
    if (answer && !answer.ok) this.band = answer.error.message;
    this.render();
    return answer;
  }

  // actions is the panel as data, so what is offered can be asserted
  // without walking a tree that might merely have failed to render.
  actions() {
    if (this.placing) {
      return [
        { id: ACTION_COMMIT, label: LABEL_COMMIT },
        { id: ACTION_CANCEL, label: LABEL_CANCEL },
      ];
    }
    const out = [];
    if (this.file) out.push({ id: ACTION_UPLOAD, label: LABEL_UPLOAD });
    if (this.asset) {
      out.push({ id: ACTION_ADJUST, label: LABEL_ADJUST });
      out.push({ id: this.confirming ? ACTION_CONFIRM_CLEAR : ACTION_CLEAR, label: this.confirming ? LABEL_CONFIRM_CLEAR : LABEL_CLEAR });
    }
    return out;
  }

  render() {
    const root = this.root;
    while (root.childNodes.length > 0) root.removeChild(root.childNodes[root.childNodes.length - 1]);
    root.setAttribute("class", this.pending ? CLASS_GROUND + " " + CLASS_PENDING : CLASS_GROUND);

    const input = this.doc.createElement("input");
    input.setAttribute("type", "file");
    input.setAttribute("accept", ACCEPTED_MIMES.join(","));
    root.appendChild(input);
    this.input = input;

    // The facts, always, and before anything has been chosen: that is
    // the whole point of them.
    for (const fact of FACTS) {
      const paragraph = this.doc.createElement("p");
      paragraph.setAttribute("class", CLASS_FACT);
      paragraph.textContent = fact;
      root.appendChild(paragraph);
    }

    if (this.confirming) {
      const confirm = this.doc.createElement("p");
      confirm.setAttribute("class", CLASS_CONFIRM);
      confirm.textContent = CONFIRM_CLEAR;
      root.appendChild(confirm);
    }

    for (const action of this.actions()) {
      const button = this.doc.createElement("button");
      button.setAttribute("type", "button");
      button.setAttribute("data-action", action.id);
      button.textContent = action.label;
      root.appendChild(button);
    }

    if (this.band) {
      const band = this.doc.createElement("p");
      band.setAttribute("class", CLASS_BAND);
      band.textContent = this.band;
      root.appendChild(band);
    }
    return root;
  }
}

// normaliseBackground reads a view row's three background columns, with
// internal/views' own defaults where it says nothing. A row with no
// asset is a scale of 1 at the origin, which is what RemoveAsset leaves
// behind: the knobs are reset with the id precisely so nothing is left
// placing an image that is gone.
function normaliseBackground(row) {
  const from = row && typeof row === "object" ? row : {};
  const offset = from.offset && typeof from.offset === "object" ? from.offset : {};
  return {
    assetId: typeof from.asset_id === "string" ? from.asset_id : null,
    scale: Number.isFinite(from.scale) && from.scale > 0 ? from.scale : DEFAULT_SCALE,
    offset: {
      x: Number.isFinite(offset.x) ? offset.x : 0,
      y: Number.isFinite(offset.y) ? offset.y : 0,
    },
  };
}

customElements.define("mst-ground", MstGround);
