"use strict";

// Run before the stylesheet to avoid a flash of the wrong theme on navigation.
(() => {
  const key = "geko.theme";
  const valid = (value) => ["dark", "light", "system"].includes(value);
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  let preference = "dark";
  try {
    const saved = localStorage.getItem(key);
    if (valid(saved)) preference = saved;
  } catch { /* Storage can be unavailable in restricted browsers. */ }
  function apply() {
    document.documentElement.dataset.theme = preference === "system"
      ? (media.matches ? "dark" : "light") : preference;
  }
  apply();
  media.addEventListener("change", apply);
  window.addEventListener("storage", (event) => {
    if (event.key !== key && event.key !== null) return;
    preference = valid(event.newValue) ? event.newValue : "dark";
    apply();
    const select = document.querySelector("[data-theme-select]");
    if (select) select.value = preference;
  });
  document.addEventListener("DOMContentLoaded", () => {
    const select = document.querySelector("[data-theme-select]");
    if (!select) return;
    select.disabled = false;
    select.value = preference;
    select.addEventListener("change", () => {
      if (!valid(select.value)) return;
      preference = select.value;
      apply();
      const status = document.getElementById("theme-status");
      try {
        localStorage.setItem(key, preference);
        status.textContent = "Theme saved in this browser.";
      } catch {
        status.textContent = "Theme applied. Browser storage is unavailable; this choice may reset on navigation.";
      }
    });
  });
})();
