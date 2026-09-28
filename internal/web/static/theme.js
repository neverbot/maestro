// The chosen theme, applied before the first paint.
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
