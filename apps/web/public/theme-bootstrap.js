(() => {
  let storedTheme = null;
  try {
    storedTheme = globalThis.localStorage.getItem("theme");
  } catch {
    // A blocked storage API should not prevent the app from rendering.
  }

  const prefersDark = globalThis.matchMedia("(prefers-color-scheme: dark)").matches;
  if (storedTheme === "dark" || (!storedTheme && prefersDark)) {
    globalThis.document.documentElement.setAttribute("dark-mode", "");
  }
})();
