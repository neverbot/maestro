// The DOM stub the SVG emitter is driven in, and the mutation log the
// drag budget is measured with.

export const SVG_NS = "http://www.w3.org/2000/svg";
export const HTML_NS = "http://www.w3.org/1999/xhtml";

export function createDocument() {
  const log = [];
  let sequence = 0;

  class StubElement {
    constructor(tagName, namespaceURI) {
      if (typeof tagName !== "string" || tagName === "") {
        throw new Error("stub: an element needs a tag name");
      }
      if (typeof namespaceURI !== "string" || namespaceURI === "") {
        throw new Error("stub: an element needs a namespace; createElementNS is how an SVG element is made");
      }
      this.tagName = tagName;
      this.namespaceURI = namespaceURI;
      // Per element, never shared. A single map behind every element
      // would make one emitter's write look like every other's.
      this.attributes = new Map();
      this.childNodes = [];
      this.parentNode = null;
      this.ownText = "";
      this.listeners = {};
      // `dataset`, because a control that goes busy stashes its idle
      // label there while a write is in flight (app.js setFormBusy).
      this.dataset = {};
      // What innerHTML stored, if anything did.
      this.markup = "";
    }

    setAttribute(name, value) {
      if (typeof name !== "string" || name === "") throw new Error("stub: an attribute needs a name");
      if (typeof value !== "string") {
        throw new Error(
          `stub: setAttribute(${name}) was handed a ${typeof value}; a DOM attribute value is a string`,
        );
      }
      this.attributes.set(name, value);
      log.push({ element: this, name, value, at: sequence++ });
    }

    getAttribute(name) {
      return this.attributes.has(name) ? this.attributes.get(name) : null;
    }

    hasAttribute(name) {
      return this.attributes.has(name);
    }

    removeAttribute(name) {
      if (!this.attributes.has(name)) return;
      this.attributes.delete(name);
      log.push({ element: this, name, value: null, at: sequence++ });
    }

    // className and classList are the same attribute read two ways, and
    // both are here because this front end writes both: a fresh element
    // takes its whole class in one assignment and a conditional one is
    // added afterwards. A stub carrying only the first turns the second
    // into `Cannot read properties of undefined`, which is a harness
    // crash rather than the assertion the test came to make.
    get className() {
      return this.attributes.get("class") ?? "";
    }

    set className(value) {
      if (typeof value !== "string") throw new Error("stub: className is a string");
      this.setAttribute("class", value);
    }

    get classList() {
      const owner = this;
      const names = () => (owner.className === "" ? [] : owner.className.split(" "));
      return {
        add(...wanted) {
          const has = names();
          for (const name of wanted) if (!has.includes(name)) has.push(name);
          owner.className = has.join(" ");
        },
        remove(...unwanted) {
          owner.className = names().filter((name) => !unwanted.includes(name)).join(" ");
        },
        contains(name) {
          return names().includes(name);
        },
      };
    }

    // children is the element children, which is every child this stub
    // can hold: it has no text nodes, because textContent is a string on
    // the element itself rather than a node in the list.
    get children() {
      return this.childNodes.slice();
    }

    // `form.elements`, which `setFormBusy` walks to disable a form while
    // a write is in flight. A form in this front end is built from
    // elements rather than parsed, so this is what a real one would
    // hold: every control under it, however deep.
    get elements() {
      const found = [];
      const walk = (node) => {
        for (const child of node.childNodes) {
          if (["input", "select", "textarea", "button"].includes(child.tagName)) found.push(child);
          walk(child);
        }
      };
      walk(this);
      return found;
    }

    // append takes several children where appendChild takes one. Both
    // exist because both are used, and a stub with only one of them
    // pushes its own shape into the product's code.
    append(...kids) {
      for (const kid of kids) this.appendChild(kid);
    }

    appendChild(child) {
      if (!(child instanceof StubElement)) throw new Error("stub: only elements can be appended");
      if (child.parentNode) child.parentNode.removeChild(child);
      this.childNodes.push(child);
      child.parentNode = this;
      return child;
    }

    removeChild(child) {
      const at = this.childNodes.indexOf(child);
      if (at < 0) throw new Error("stub: removeChild was handed a node that is not a child");
      this.childNodes.splice(at, 1);
      child.parentNode = null;
      return child;
    }

    get textContent() {
      return this.ownText + this.childNodes.map((child) => child.textContent).join("");
    }

    set textContent(value) {
      if (typeof value !== "string") throw new Error("stub: textContent is character data, which is a string");
      for (const child of this.childNodes.slice()) this.removeChild(child);
      this.ownText = value;
    }

    // **A listener is recorded, never invoked by the stub.** A component
    // binding one is a fact a harness asserts (writes_test.mjs counts
    // them on a gesture); firing them here would make this a small event
    // loop and every test of one a test of it. `dispatch` below is how a
    // harness runs one deliberately.
    addEventListener(kind, handler) {
      if (typeof handler !== "function") return;
      (this.listeners[kind] ??= []).push(handler);
    }

    removeEventListener(kind, handler) {
      const list = this.listeners[kind];
      if (!list) return;
      const at = list.indexOf(handler);
      if (at >= 0) list.splice(at, 1);
    }

    // dispatch runs what a real click would run, and nothing else: no
    // bubbling, no default action, no ordering rules. A harness that
    // needs those needs a browser.
    // **It awaits.** A page's own handlers are async — a write goes out
    // and the screen is settled when it comes back — and a dispatch that
    // did not await them handed the harness a page mid-flight: the
    // assertion ran before the client had been called at all. Returning
    // the promise costs a synchronous caller nothing, because a
    // synchronous handler resolves before the caller's next line either
    // way.
    async dispatch(kind, event = {}) {
      for (const handler of this.listeners[kind] ?? []) {
        await handler({ target: this, ...event });
      }
    }

    click() {
      return this.dispatch("click");
    }

    replaceChildren(...kids) {
      for (const child of this.childNodes.slice()) this.removeChild(child);
      for (const kid of kids) this.appendChild(kid);
    }

    // querySelector understands one selector, `.class`, because that is
    // the only shape this front end asks a stub for. Anything else
    // throws rather than quietly answering null: a selector a stub
    // silently fails to match is a component that looks wired and is
    // not.
    querySelector(selector) {
      // The second shape this front end asks for, and the only one that
      // is not a class: `setFormBusy` finds a form's submit button by
      // `button[type=submit]`. It is spelled out rather than parsed,
      // because a stub with a selector engine is a second, worse browser
      // and every escaping question becomes a question about the stub.
      if (selector === "button[type=submit]") {
        const walk = (node) => {
          for (const child of node.childNodes) {
            if (child.tagName === "button" && child.type === "submit") return child;
            const deeper = walk(child);
            if (deeper) return deeper;
          }
          return null;
        };
        return walk(this);
      }
      if (typeof selector !== "string" || !selector.startsWith(".")) {
        throw new Error(`stub: querySelector understands ".class" and "button[type=submit]" only, not ${selector}`);
      }
      const wanted = selector.slice(1);
      const walk = (node) => {
        for (const child of node.childNodes) {
          if ((child.attributes.get("class") ?? "").split(" ").includes(wanted)) return child;
          const deeper = walk(child);
          if (deeper) return deeper;
        }
        return null;
      };
      return walk(this);
    }

    // querySelectorAll, in the same deliberately tiny dialect
    // querySelector speaks: `.class`, and nothing else. A picker's
    // harness asks for every option in a menu, which is one class.
    querySelectorAll(selector) {
      if (typeof selector !== "string" || !selector.startsWith(".")) {
        throw new Error(`stub: querySelectorAll understands ".class" only, not ${selector}`);
      }
      const wanted = selector.slice(1);
      const found = [];
      const walk = (node) => {
        for (const child of node.childNodes) {
          if ((child.attributes.get("class") ?? "").split(" ").includes(wanted)) found.push(child);
          walk(child);
        }
      };
      walk(this);
      return found;
    }

    // **Stored, not parsed.** Two modules in this front end insert markup
    // and both insert what internal/markdown rendered: doc.js for a
    // document body and pages/entity.js for a longtext field. A harness
    // that threw here could not drive either, and a harness that parsed
    // would be a second HTML parser nobody tested. Which modules may
    // reach this at all is held by a guard over the sources
    // (TestOnlyTheRenderedProseIsInsertedAsMarkup) rather than by this
    // stub, because a stub only sees the modules a harness happens to
    // load.
    get innerHTML() {
      return this.markup;
    }

    set innerHTML(value) {
      this.markup = typeof value === "string" ? value : "";
      this.childNodes.length = 0;
    }
  }

  const document = {
    createElementNS(namespaceURI, tagName) {
      return new StubElement(tagName, namespaceURI);
    },
    createElement(tagName) {
      return new StubElement(tagName, HTML_NS);
    },
  };

  return {
    document,
    StubElement,
    log,
    // reset is how a measurement starts from nothing. It empties the
    // log in place, so a caller holding a reference keeps seeing the
    // same array.
    reset() {
      log.length = 0;
    },
    // touched is the set of elements written to since the last reset,
    // which is the drag budget expressed as a number.
    touched() {
      return [...new Set(log.map((entry) => entry.element))];
    },
  };
}

// install puts the globals a custom element's constructor needs onto the
// host, and answers with the document it installed. Nothing here parses
// HTML, deliberately: a stub that could turn a string into elements
// would be a second, worse browser, and every escaping question would
// become a question about the stub.
export function install() {
  const dom = createDocument();
  const defined = new Map();
  globalThis.window = globalThis;
  globalThis.document = dom.document;
  globalThis.HTMLElement = class HTMLElement {
    constructor() {
      this.shadowRoot = null;
      // Every event this element sent, in order: a harness that only
      // wants to know *that* something was announced reads this.
      this.dispatched = [];
      this.listeners = {};
    }
    attachShadow() {
      this.shadowRoot = dom.document.createElement("shadow-root");
      return this.shadowRoot;
    }
    // **A listener added here is called.** It was a no-op, and the
    // element still recorded what it dispatched — so a component that
    // announced a choice and a harness that listened for one both
    // "worked" and never met. That is the shape of defect this whole
    // directory exists to catch, in the stub itself.
    dispatchEvent(event) {
      this.dispatched.push(event);
      for (const handler of this.listeners[event?.type] ?? []) handler(event);
      return true;
    }
    addEventListener(kind, handler) {
      if (typeof handler !== "function") return;
      (this.listeners[kind] ??= []).push(handler);
    }
    removeEventListener(kind, handler) {
      const list = this.listeners[kind];
      if (!list) return;
      const at = list.indexOf(handler);
      if (at >= 0) list.splice(at, 1);
    }
  };
  // A component dispatches a CustomEvent; the stub needs one that
  // carries a type and a detail and nothing else.
  if (typeof globalThis.CustomEvent !== "function") {
    globalThis.CustomEvent = class CustomEvent {
      constructor(type, init = {}) {
        this.type = type;
        this.detail = init.detail ?? null;
        this.bubbles = init.bubbles === true;
        this.composed = init.composed === true;
      }
    };
  }
  globalThis.customElements = {
    define(name, ctor) {
      defined.set(name, ctor);
    },
    get(name) {
      return defined.get(name);
    },
  };
  return { ...dom, defined };
}

// walk yields every element under `root`, in document order — which in
// SVG is paint order, and is what makes "labels after their nodes" a
// thing a test can read.
export function walk(root) {
  const found = [];
  const visit = (element) => {
    found.push(element);
    for (const child of element.childNodes) visit(child);
  };
  visit(root);
  return found;
}
