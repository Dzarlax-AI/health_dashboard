(() => {
  let storedTheme = null;
  try {
    storedTheme = globalThis.localStorage.getItem("theme");
  } catch {
    // A blocked storage API should not prevent the app from rendering.
  }

  const prefersDark = globalThis.matchMedia("(prefers-color-scheme: dark)").matches;
  const preference =
    storedTheme === "light" || storedTheme === "dark" ? storedTheme : "system";
  if (preference === "dark" || (preference === "system" && prefersDark)) {
    globalThis.document.documentElement.setAttribute("dark-mode", "");
  }
})();
