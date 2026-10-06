// The one place in this front end that inserts markup, beside doc.js.
//
// **The argument is doc.js's own**: the string comes from
// internal/markdown, which is goldmark with no html.WithUnsafe, so a tag
// a designer or an agent typed arrives as text and a link destination
// whose scheme is not http, https or mailto arrives as "#". Nothing a
// game holds can reach the parser through here.
//
// It is a module of its own so the two callers that draw rendered prose
// — a longtext field and a comment — share one inserter rather than one
// importing the other's page.

// proseBlock is a rendered body. `tool` sets it in the sans: a comment
// is what somebody thought about the game and would not survive the game
// shipping, so by the Two Voices Rule it is not the game's serif.
export function proseBlock(doc, html, options) {
  const block = doc.createElement("div");
  block.className = options && options.tool === true ? "prose tool" : "prose";
  block.innerHTML = html;
  return block;
}
