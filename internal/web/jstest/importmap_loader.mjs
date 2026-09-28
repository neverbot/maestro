// A Node module-resolution hook that resolves bare specifiers through
// **the import map this server ships**, so a harness can load a real
// component the way a browser loads it.

import { readFileSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

let imports = {};
let staticDir = "";

export async function initialize(data) {
  staticDir = data.staticDir;
  const src = readFileSync(path.join(staticDir, data.shell), "utf8");
  const match = /<script\s+type="importmap"\s*>([\s\S]*?)<\/script>/.exec(src);
  if (!match) throw new Error(`${data.shell} carries no import map`);
  imports = JSON.parse(match[1]).imports || {};
  if (Object.keys(imports).length === 0) throw new Error(`${data.shell} maps no specifiers`);
}

export async function resolve(specifier, context, next) {
  const target = imports[specifier];
  if (target === undefined) return next(specifier, context);
  if (!target.startsWith("/static/")) {
    throw new Error(`the import map sends ${specifier} to ${target}, which is not a local static path`);
  }
  const full = path.join(staticDir, target.slice("/static/".length));
  if (!full.startsWith(staticDir + path.sep)) {
    throw new Error(`the import map escapes the static tree: ${target}`);
  }
  return { url: pathToFileURL(full).href, shortCircuit: true };
}
