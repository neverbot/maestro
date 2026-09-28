// The four pickers this product keeps needing, each as the one function
// that turns a client into options.

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
export async function fieldOptions(client, typeKey, options = {}) {
  if (typeof typeKey !== "string" || typeKey === "") return [];
  const answer = await client.getType(typeKey);
  if (!answer.ok) return [];
  const schema = Array.isArray(answer.result.field_schema) ? answer.result.field_schema : [];
  return optionsFrom(schema, options);
}

// entitySource is the one picker that cannot be a list.
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
