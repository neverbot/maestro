// A Node module-resolution hook that resolves bare specifiers through
// **the import map this server ships**, so a harness can load a real
// component the way a browser loads it.
//
// The components import `lit` by bare specifier, because that is what
// the shells' import map maps and what a browser needs; Node has no
// import map and no `node_modules` here, and this repository ships no
// build step to rewrite either. So this hook reads the map out of a
// shipped shell — the same one the browser is served — and answers with
// the file it names.
//
// Reading the map rather than hard-coding "lit" is the point: a
// specifier renamed in the shells and not here would leave the harness
// importing something the browser never would, and the harness would go
// on passing. internal/web/jstest/vendor_modules_test.mjs reads the same
// map for the same reason.
//
// It is registered by internal/web/jstest/twin_test.mjs through
// node:module's `register`, which runs it on the loader thread.

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
