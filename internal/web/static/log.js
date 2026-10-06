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

// entry is one note.
function entry(doc, comment, now, onRemove) {
  const item = doc.createElement("li");
  item.className = "log-entry";

  const meta = doc.createElement("p");
  meta.className = "log-meta";
  const who = doc.createElement("span");
  who.className = "log-author";
  who.textContent = comment.author || t("log.someone");
  meta.append(who);
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

  const note = doc.createElement("p");
  note.className = "band-note";
  // The band says what it is for, because a log nobody knows the rules of
  // fills with the wrong thing: the game's own content belongs in a field
  // or a document, and this is what was thought about it.
  note.textContent = t("log.note");
  section.append(note);

  if (typeof spec.write === "function") {
    section.append(composer(doc, spec));
  }

  const list = doc.createElement("ol");
  list.className = "log-entries";
  section.append(list);

  const empty = negativeState(doc, {
    kind: STATE_EMPTY,
    heading: t("log.empty.heading"),
    sentence: whoWrites(spec.role, "log.empty.sentence"),
  });
  section.append(empty);

  const now = typeof spec.now === "number" ? spec.now : Date.now();
  const draw = (comments) => {
    const rows = Array.isArray(comments) ? comments : [];
    list.replaceChildren(...rows.map((comment) => entry(doc, comment, now, spec.remove)));
    list.hidden = rows.length === 0;
    empty.hidden = rows.length !== 0;
  };
  draw(spec.comments);
  section.draw = draw;
  return section;
}

// composer is the one write: a box, and a button under it.
function composer(doc, spec) {
  const form = doc.createElement("form");
  form.className = "log-composer";

  const box = doc.createElement("textarea");
  box.rows = 3;
  box.setAttribute("aria-label", t("log.write"));
  box.setAttribute("placeholder", t("log.placeholder"));
  form.append(box);

  const error = doc.createElement("p");
  error.className = "error";

  const save = doc.createElement("button");
  save.type = "submit";
  save.textContent = t("log.write");
  form.append(save, error);

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
  });
  return form;
}
