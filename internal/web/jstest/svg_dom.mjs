// The DOM stub the SVG emitter is driven in, and the mutation log the
// drag budget is measured with.
//
// The four harnesses that came before this one each carry their own
// inline stub, because each needed three or four members of `document`
// and no more. This one is a module rather than another copy for a
// reason that is not tidiness: the assertions it serves are assertions
// *about the DOM* — which attribute carries which value, in what order
// elements were appended, and how many elements a drag touched — so the
// stub is no longer scaffolding around the thing under test. It is part
// of the instrument, and an instrument gets its own guard.
//
// **The failure mode this file is written against.** A stub that starts
// a flag where the assertion wants it makes a test pass on its own; this
// repository has shipped that shape twice. So nothing here has a
// helpful default:
//
//   - `getAttribute` answers **null** for an attribute nobody set, never
//     the empty string, so "the emitter wrote this" and "the emitter
//     wrote nothing" are two different answers.
//   - `setAttribute` refuses a non-string value. A DOM attribute is a
//     string; accepting a number would let an emitter that never called
//     String() pass a comparison the browser would fail.
//   - `createElementNS` refuses a missing namespace and records the one
//     it was given, so an assertion that a mark landed in the SVG
//     namespace cannot be satisfied by a default.
//   - `textContent` starts empty and is only ever a string.
//   - `innerHTML` throws on read *and* write, in both directions, so the
//     one sink this whole component is built to avoid cannot be reached
//     quietly.
//   - the mutation log starts empty, records only writes, and records
//     nothing for a read.
//
// theDOMStubStartsWhereTheAssertionsBegin, in canvas_test.mjs, asserts
// every one of those against the stub itself.

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

    get innerHTML() {
      throw new Error("stub: innerHTML parses markup, and nothing in this front end may read or write it");
    }

    set innerHTML(_value) {
      throw new Error("stub: innerHTML parses markup, and nothing in this front end may read or write it");
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
      this.dispatched = [];
    }
    attachShadow() {
      this.shadowRoot = dom.document.createElement("shadow-root");
      return this.shadowRoot;
    }
    dispatchEvent(event) {
      this.dispatched.push(event);
      return true;
    }
    addEventListener() {}
    removeEventListener() {}
  };
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
