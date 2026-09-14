// The four pickers this product keeps needing, each as the one function
// that turns a client into options.
//
// **Three screens wanted the same control and none of them had it**: the
// query builder needs an entity type, a relation type, a field of a type
// and an entity; the route step list needs the last of those; and the
// Draw clause needs the third. Built once, they are one control a
// designer learns once — and, more to the point, one place where "what
// may be chosen" is decided from the game's own vocabulary rather than
// from whatever each screen happened to fetch.
//
// Nothing here draws: `mst-picker` is the control and these are the
// sources it is fed from. They are ordinary async functions so a page
// can await one, a harness can call one with a stub client, and neither
// has to mount a component to find out what a picker would offer.

import { MstPicker, optionsFrom } from "./mst-picker.js";

// SEARCH_PAGE is how many entities one search of a picker asks for. It
// is smaller than the catalogue's fifty: a menu is not a listing, and a
// person who cannot see their entity in twenty rows narrows the word
// rather than scrolling a dropdown.
export const SEARCH_PAGE = 20;

// entityTypeOptions and relationTypeOptions are the two vocabularies a
// game declares. Both listings are small by construction — a game with
// four hundred entity types is a game whose modelling has gone wrong —
// so both are one call and no paging.
export async function entityTypeOptions(client, options = {}) {
  const answer = await client.listTypes();
  if (!answer.ok) return [];
  return optionsFrom(itemsOf(answer.result), options);
}

export async function relationTypeOptions(client, options = {}) {
  const answer = await client.listRelationTypes();
  if (!answer.ok) return [];
  return optionsFrom(itemsOf(answer.result), options);
}

// fieldOptions is the fields one type declares, in the order it declares
// them — which is the order the game means, and is why this does not
// sort.
//
// It reads the type rather than taking a schema, because every caller
// has a type key and only one of them has already fetched the type.
export async function fieldOptions(client, typeKey, options = {}) {
  if (typeof typeKey !== "string" || typeKey === "") return [];
  const answer = await client.getType(typeKey);
  if (!answer.ok) return [];
  const schema = Array.isArray(answer.result.field_schema) ? answer.result.field_schema : [];
  return optionsFrom(schema, options);
}

// entitySource is the one picker that cannot be a list.
//
// A game in this instance has a thousand entities of one type, so the
// options are an answer to a question rather than a vocabulary: it
// returns a **source** — the function `mst-picker` calls as somebody
// types — and the search it runs is the product's own, which indexes a
// row's key alongside its name.
//
// **It answers with the key the game wrote**, like every other picker,
// and labels with the entity's name. A picker that answered with an id
// would hand the query language a value it cannot address a row by.
export function entitySource(client, typeKey, options = {}) {
  return async (query) => {
    const asked = String(query ?? "").trim();
    // An empty box is not a search. The server refuses a query with no
    // letter or digit in it, and asking anyway would put a refusal in a
    // menu as the answer to having typed nothing yet.
    if (asked === "") return [];
    const answer = await client.searchEntities(asked, typeKey, {
      limit: options.limit ?? SEARCH_PAGE,
      kind: "entity",
    });
    if (!answer.ok) return [];
    const hits = Array.isArray(answer.result.items) ? answer.result.items : [];
    return hits
      .map((hit) => hit && hit.entity)
      .filter((entity) => entity && typeof entity.key === "string")
      .map((entity) => ({ key: entity.key, label: entity.name || entity.key }));
  };
}

// pickerFor builds the control already wired to one of the sources
// above, which is what a page actually wants: two lines rather than
// four, and no chance of a picker built with a source and drawn without
// one.
export function pickerFor(init) {
  return new MstPicker(init);
}

// itemsOf is the one shape difference between the two listings and the
// schema: both listings answer `{items: […]}`, and a schema is already a
// list.
function itemsOf(result) {
  if (Array.isArray(result)) return result;
  return result && Array.isArray(result.items) ? result.items : [];
}
