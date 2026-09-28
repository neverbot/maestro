// An entity's address, in one function, for the whole front end.
export function addressOf(node) {
  return JSON.stringify([
    typeof node?.type === "string" ? node.type : "",
    typeof node?.key === "string" ? node.key : "",
  ]);
}
