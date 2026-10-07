// The log beside a thing's content: what a designer or an agent was
// thinking when they changed it.
//
// **One builder, three pages.** An entity, an entity type and a relation
// type each carry one, and a band rebuilt per page is three bands that
// drift. The target is the only thing that differs.
//
// The shape is a margin note, not a chat: no avatar, no bubble, no
// thread. A line of who and when, the note under it, and a hairline
// between entries.
import { t } from "./i18n.js";
import { hueFor } from "./palette.js";
import { STATE_EMPTY, negativeState, whoWrites } from "./pages/page.js";
import { proseBlock } from "./prose.js";
import { setFormBusy } from "./app.js";

// LOG_ROWS is how many entries a page asks for. The server caps the
// listing anyway; this is the page saying what it will draw.
export const LOG_ROWS = 50;

// when turns a timestamp into the one phrase a reader wants from a log,
// which is how long ago rather than a date nobody is going to subtract.
export function when(now, iso) {
  const then = Date.parse(String(iso || ""));
  if (!Number.isFinite(then)) return "";
  const seconds = Math.max(0, Math.round((now - then) / 1000));
  if (seconds < 60) return t("log.justNow");
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return t("log.minutes").replace("{n}", String(minutes));
  const hours = Math.round(minutes / 60);
  if (hours < 24) return t("log.hours").replace("{n}", String(hours));
  const days = Math.round(hours / 24);
  return t("log.days").replace("{n}", String(days));
}

// MONOGRAM_LETTERS is how much of a name a chip carries. Two, because a
// chip is for telling three or four authors apart down a column and not
// for reading.
const MONOGRAM_LETTERS = 2;

// monogram is the initials of a name, in the one shape that works for
// "Iván Alonso", "rl-aeternum" and "seed".
export function monogram(name) {
  const words = String(name || "").split(/[\s._-]+/).filter((word) => word !== "");
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, MONOGRAM_LETTERS).toUpperCase();
  return (words[0][0] + words[words.length - 1][0]).toUpperCase();
}

// chip is the author's mark. **Not a gravatar**: the content security
// policy is `default-src 'self'` and a third-party avatar would be
// blocked silently, and asking gravatar.com for one tells it the hash of
// a designer's email on every page view, from a product whose whole
// identity story is local accounts and no external anything. This is the
// local answer: two letters on one of the eight data hues, which
// palette.js already assigns by hashing text and which were chosen to
// stay apart under deuteranopia and protanopia.
//
// **The hue is the owner's, for a person and for their agent alike**, so
// a column of notes shows at a glance which of them trace back to the
// same person. What kind of author it was is carried by the word beside
// it, never by the colour.
function chip(doc, comment) {
  const of = comment.author_of || comment.author || "";
  const mark = doc.createElement("span");
  mark.className = "log-chip hue-" + (hueFor(of) + 1);
  mark.textContent = monogram(comment.author || t("log.someone"));
  // **Whose agent it is lives here**, not on the line. The line already
  // says who wrote it and that a machine did; which person's token that
  // was is the next question down and does not have to cost the line the
  // width of a second name. It is the chip's accessible name as well as
  // its tooltip, so it is not a hover-only fact.
  const says = comment.by_agent === true && comment.author_of
    ? t("log.agentOf").replace("{who}", comment.author_of)
    : comment.author || t("log.someone");
  mark.setAttribute("title", says);
  mark.setAttribute("aria-label", says);
  return mark;
}

// entry is one note.
function entry(doc, comment, now, onRemove) {
  const item = doc.createElement("li");
  item.className = "log-entry";

  const meta = doc.createElement("p");
  meta.className = "log-meta";
  meta.append(chip(doc, comment));
  const who = doc.createElement("span");
  who.className = "log-author";
  who.textContent = comment.author || t("log.someone");
  meta.append(who);
  // **"agente" is a word and not a badge.** A reader should not have to
  // learn a mark to find out that a machine wrote this, and the token's
  // label is a word somebody chose — "rl-aeternum" reads as a name. Whose
  // agent it is comes with it, because in a game with two designers that
  // is the question under "who wrote this".
  if (comment.by_agent === true) {
    const kind = doc.createElement("span");
    kind.className = "log-agent";
    kind.textContent = t("log.agent");
    meta.append(kind);
  }
  const stamp = doc.createElement("time");
  stamp.className = "log-when";
  stamp.setAttribute("datetime", String(comment.created_at || ""));
  stamp.textContent = when(now, comment.created_at);
  meta.append(stamp);

  if (typeof onRemove === "function") {
    const remove = doc.createElement("button");
    remove.type = "button";
    remove.className = "quiet log-remove";
    remove.textContent = t("log.remove");
    // The name says which note, because "Remove" said six times on one
    // page is six controls a screen reader cannot tell apart.
    remove.setAttribute("aria-label", t("log.remove") + " " + (comment.author || ""));
    remove.addEventListener("click", () => onRemove(comment));
    meta.append(remove);
  }
  item.append(meta);

  const body = doc.createElement("div");
  body.className = "log-body";
  // The rendering the browser's own route sent. A note is the tool's
  // voice and not the game's, so it is the prose vocabulary in the sans:
  // what was thought about a thing would not survive the game shipping.
  body.append(proseBlock(doc, String(comment.body_html || ""), { tool: true }));
  item.append(body);
  return item;
}

// logBand builds the whole band: the box that writes one, and the log.
// `spec.write` is absent for a reader who may not write, and the band
// then has no form at all rather than a disabled one.
export function logBand(doc, spec) {
  const section = doc.createElement("section");
  section.className = "log";
  const heading = doc.createElement("h2");
  heading.textContent = t("log.heading");
  section.append(heading);

  const list = doc.createElement("ol");
  list.className = "log-entries";
  section.append(list);

  const empty = negativeState(doc, {
    kind: STATE_EMPTY,
    heading: t("log.empty.heading"),
    sentence: whoWrites(spec.role, "log.empty.sentence"),
  });
  section.append(empty);

  // **The box after the log, not before it.** A log is read down to the
  // newest and written at the end, so the one write sits where your eye
  // already is. It was the first thing in the band, which put an empty
  // control above the content it is about.
  if (typeof spec.write === "function") {
    section.append(composer(doc, spec));
  }

  const now = typeof spec.now === "number" ? spec.now : Date.now();
  const draw = (comments) => {
    // **Oldest first, and the server answers newest first.** The server
    // is the one with the limit, and a limit taken from the oldest end
    // truncates the wrong end of a log; the order a person reads it in is
    // the order it happened. There is no cap on what is drawn: a log that
    // shows the last few hides exactly what a reader does not know is
    // there.
    const rows = Array.isArray(comments) ? [...comments].reverse() : [];
    list.replaceChildren(...rows.map((comment) => entry(doc, comment, now, spec.remove)));
    list.hidden = rows.length === 0;
    empty.hidden = rows.length !== 0;
  };
  draw(spec.comments);
  section.draw = draw;
  return section;
}

// composer is the one write: a quiet word, and the box it opens.
//
// **Closed until somebody wants it**, which is the shape this page
// already uses for a write that is not the point of the screen: the
// field editor is a word, and the control appears when it is reached
// for. A box and a filled button standing open were the loudest thing
// on a reading page for its most secondary act — measured, the "Comment"
// button wore the primary's ink fill and shadow while "Rename", which is
// what this page is actually for, wore the ghost's. Almost every note
// here is written by an agent over MCP; the person's way in should cost
// the page a word.
function composer(doc, spec) {
  const holder = doc.createElement("div");
  holder.className = "log-composer";

  const open = doc.createElement("button");
  open.type = "button";
  open.className = "quiet log-open";
  open.textContent = t("log.write");

  const form = doc.createElement("form");
  form.className = "log-form";
  form.hidden = true;

  const box = doc.createElement("textarea");
  box.rows = 3;
  box.setAttribute("aria-label", t("log.write"));
  box.setAttribute("placeholder", t("log.placeholder"));
  form.append(box);

  const error = doc.createElement("p");
  error.className = "error";

  // **Inside the form the save is the primary, legitimately**: while the
  // box is open, writing the note is what the screen is for.
  const save = doc.createElement("button");
  save.type = "submit";
  save.textContent = t("entity.save");
  const cancel = doc.createElement("button");
  cancel.type = "button";
  cancel.className = "ghost";
  cancel.textContent = t("entity.cancel");
  form.append(save, cancel, error);
  holder.append(open, form);

  const close = () => {
    form.hidden = true;
    open.hidden = false;
    error.textContent = "";
  };
  open.addEventListener("click", () => {
    form.hidden = false;
    open.hidden = true;
    if (typeof box.focus === "function") box.focus();
  });
  cancel.addEventListener("click", () => {
    box.value = "";
    close();
  });

  form.addEventListener("submit", async (event) => {
    if (event && typeof event.preventDefault === "function") event.preventDefault();
    const body = String(box.value ?? "").trim();
    if (body === "") {
      error.textContent = t("log.needsWords");
      return;
    }
    error.textContent = "";
    setFormBusy(form, true, t("action.saving"));
    const answer = await spec.write(body);
    setFormBusy(form, false);
    if (!answer || !answer.ok) {
      error.textContent = answer && answer.error ? answer.error.message : "";
      return;
    }
    box.value = "";
    close();
  });
  return holder;
}
