// The chosen theme, applied before the first paint.
//
// **A classic script with no `defer`, and that is the whole point.** A
// module is deferred by definition: the page would paint in the system's
// theme and then jump to the chosen one, which is a flash on every
// navigation in a product with no client-side router. This file is
// therefore loaded synchronously from the `<head>` of every shell, runs
// before the body exists, and writes one attribute.
//
// It is also the reason the logic is repeated rather than imported: the
// account screen reads and writes the same value through the three
// functions below, hung off `window` because a classic script has no
// exports. The alternative — a module for the screen and a copy here —
// is two implementations of one rule, which this project reports as a
// defect.
//
// The choice lives in this browser, not in the account: there is no
// preferences table and a theme did not seem worth a migration. A person
// with two machines chooses twice, and the account screen says so rather
// than letting them find out.
(function () {
  var KEY = "maestro:theme";
  // "system" is the absence of a choice, and it is stored as the absence
  // of a value: the attribute is removed and the media query decides.
  var CHOICES = ["system", "light", "dark"];

  function read() {
    try {
      var stored = window.localStorage.getItem(KEY);
      return CHOICES.indexOf(stored) > 0 ? stored : "system";
    } catch (e) {
      // A browser with storage denied is a browser that follows its
      // system, which is the default anyone gets before choosing.
      return "system";
    }
  }

  function apply(choice) {
    var root = document.documentElement;
    if (choice === "light" || choice === "dark") root.setAttribute("data-theme", choice);
    else root.removeAttribute("data-theme");
  }

  function write(choice) {
    var next = CHOICES.indexOf(choice) > 0 ? choice : "system";
    try {
      if (next === "system") window.localStorage.removeItem(KEY);
      else window.localStorage.setItem(KEY, next);
    } catch (e) {
      // The attribute below still lands, so the choice holds for this
      // page even where it cannot be remembered for the next one.
    }
    apply(next);
    return next;
  }

  window.maestroTheme = { read: read, write: write, apply: apply, choices: CHOICES };
  apply(read());
})();
