// The load harness for the vendored runtime: it reads the *real* import
// map out of a shipped shell, resolves every specifier the way a browser
// would, imports the file that specifier names, and asserts the module
// exports the names this front end is about to import from it.
//
// What this covers that internal/web/static_vendor_test.go cannot. The
// Go guards are arithmetic over bytes — the hash matches, the size fits,
// the map is the same in every shell, the server serves the path. None
// of that says the bytes *parse as an ES module*, that the module
// evaluates without a build step or a shim, or that it exports the
// identifiers the import map exists to hand out. A hash pins which file
// was vendored; only an import says the file works.
//
// The one behavioural assertion here is deliberate rather than
// incidental. @dagrejs/dagre's ESM build is self-contained: it bundles
// its own copy of @dagrejs/graphlib and re-exports it. So the `Graph`
// this repository vendors separately as graphlib.mjs is a *different
// constructor* from the one inside dagre.mjs, and dagre.layout() only
// accepts it because dagre reads a graph structurally rather than by
// instanceof. That is an upstream implementation detail the layout code
// (Task 6) will depend on completely, and it is exactly the sort of
// thing a minor-version bump changes in silence — so it is asserted
// here, against the real vendored pair, rather than assumed.
//
// Run directly: `node internal/web/jstest/vendor_modules_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { readFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const staticDir = path.join(here, "..", "static");

let failures = 0;

function check(name, fn) {
  try {
    fn();
    console.log("ok   " + name);
  } catch (err) {
    failures++;
    console.error("FAIL " + name + ": " + (err && err.message ? err.message : err));
  }
}

async function checkAsync(name, fn) {
  try {
    await fn();
    console.log("ok   " + name);
  } catch (err) {
    failures++;
    console.error("FAIL " + name + ": " + (err && err.message ? err.message : err));
  }
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

// readImportMap parses the map out of a shell instead of restating it,
// so this harness tests the mapping the browser will actually be given.
// A specifier renamed in the shells and not here would otherwise leave
// this file happily importing a path nothing maps to.
function readImportMap(shell) {
  const src = readFileSync(path.join(staticDir, shell), "utf8");
  const match = /<script\s+type="importmap"\s*>([\s\S]*?)<\/script>/.exec(src);
  if (!match) throw new Error(`${shell} carries no import map`);
  const parsed = JSON.parse(match[1]);
  if (!parsed.imports || Object.keys(parsed.imports).length === 0) {
    throw new Error(`${shell} maps no specifiers`);
  }
  return parsed.imports;
}

// resolve turns "/static/vendor/dagre.mjs" into a file URL, refusing
// anything that is not a path under this repository's own static tree:
// the whole point of vendoring is that no specifier resolves off-box.
function resolve(target) {
  if (!target.startsWith("/static/")) {
    throw new Error(`the import map sends a specifier to ${target}, which is not a local static path`);
  }
  const full = path.join(staticDir, target.slice("/static/".length));
  if (!full.startsWith(staticDir + path.sep)) {
    throw new Error(`${target} escapes the static tree`);
  }
  return pathToFileURL(full).href;
}

// Lit's element base class is written against a DOM and evaluates
// module-scope code that needs one. This is the smallest stub that lets
// it *load* — it is not a DOM and this harness renders nothing with it;
// what a rendered Lit component does is Task 4's harness and Task 18's
// browser pass. Installed before any import so the module graph sees it.
globalThis.HTMLElement = class HTMLElement {};
globalThis.customElements = { define() {}, get: () => undefined };
globalThis.document = {
  createElement: () => ({ style: {}, content: {}, setAttribute() {}, appendChild() {} }),
  createTextNode: () => ({}),
  createComment: () => ({}),
  createTreeWalker: () => ({ currentNode: null, nextNode: () => null }),
  head: { appendChild() {} },
  adoptedStyleSheets: [],
};
globalThis.window = globalThis;

const imports = readImportMap("game.html");

// The exports each specifier owes its callers. These are the names the
// components, the renderers and the layout worker import by hand, so a
// vendored upgrade that renames or drops one fails here rather than in a
// browser console three tasks later.
const expected = {
  lit: ["LitElement", "css", "html", "noChange", "nothing", "render", "svg"],
  dagre: ["Graph", "graphlib", "layout"],
  graphlib: ["Graph", "alg", "json"],
};

const loaded = {};

for (const specifier of Object.keys(expected)) {
  await checkAsync(`${specifier} resolves through the import map and imports`, async () => {
    assert(specifier in imports, `game.html maps no "${specifier}" specifier`);
    const target = imports[specifier];
    assert(
      target.startsWith("/static/vendor/"),
      `"${specifier}" maps to ${target}, which is not a vendored file`,
    );
    loaded[specifier] = await import(resolve(target));
  });
}

for (const [specifier, names] of Object.entries(expected)) {
  check(`${specifier} exports the names this front end imports`, () => {
    const module = loaded[specifier];
    assert(module, `"${specifier}" never loaded`);
    for (const name of names) {
      assert(name in module, `"${specifier}" exports no ${name}`);
      assert(module[name] !== undefined, `"${specifier}".${name} is undefined`);
    }
  });
}

check("no specifier maps outside this instance", () => {
  for (const [specifier, target] of Object.entries(imports)) {
    assert(
      !/^[a-z]+:\/\//i.test(target),
      `"${specifier}" maps to the absolute URL ${target}; a self-hosted instance has no outbound route`,
    );
  }
});

// A small acyclic graph laid out with the vendored pair. Two ranks, so a
// layout that ran produces a b strictly below a, and a layout that
// silently did nothing leaves both at undefined.
function layoutFixture() {
  const { Graph } = loaded.graphlib;
  const g = new Graph({});
  g.setGraph({ rankdir: "TB" });
  g.setDefaultEdgeLabel(() => ({}));
  g.setNode("a", { width: 60, height: 24 });
  g.setNode("b", { width: 60, height: 24 });
  g.setNode("c", { width: 60, height: 24 });
  g.setEdge("a", "b");
  g.setEdge("a", "c");
  return g;
}

check("dagre lays out a graph built from the separately vendored graphlib", () => {
  const g = layoutFixture();
  loaded.dagre.layout(g);
  for (const key of ["a", "b", "c"]) {
    const node = g.node(key);
    assert(Number.isFinite(node.x), `node ${key} has no finite x after layout: ${node.x}`);
    assert(Number.isFinite(node.y), `node ${key} has no finite y after layout: ${node.y}`);
  }
  assert(
    g.node("b").y > g.node("a").y,
    `b should be ranked below a: a.y=${g.node("a").y}, b.y=${g.node("b").y}`,
  );
});

check("dagre's own bundled graphlib is a different constructor from the vendored one", () => {
  // If this ever stops being true — dagre importing the vendored module
  // rather than bundling a copy — the assertion above stops testing
  // cross-module tolerance and starts testing nothing, and the two
  // copies stop costing two entries in the payload budget. Either way a
  // human should look, so the day it changes is a failure, not a
  // silence.
  assert(
    loaded.dagre.graphlib.Graph !== loaded.graphlib.Graph,
    "dagre now re-exports the vendored graphlib module itself: graphlib.mjs may be redundant, " +
      "and the cross-module tolerance asserted above is no longer being exercised",
  );
});

check("the same graph laid out twice gives byte-identical coordinates", () => {
  // The one property a saved view exists for is that it looks the same
  // tomorrow. Layout that is stable only within a process would satisfy
  // every renderer test and still move every node on the next deploy.
  const positions = (g) =>
    JSON.stringify(g.nodes().sort().map((k) => [k, g.node(k).x, g.node(k).y]));
  const first = layoutFixture();
  loaded.dagre.layout(first);
  const second = layoutFixture();
  loaded.dagre.layout(second);
  assertEqual(positions(second), positions(first), "two layouts of the same graph agree");
});

if (failures > 0) {
  console.error(`${failures} vendored-runtime check(s) failed`);
  process.exit(1);
}
console.log("vendor: all checks passed");
