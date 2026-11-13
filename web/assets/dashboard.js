"use strict";

const copy = document.querySelector("[data-copy]");
if (copy && navigator.clipboard) {
  copy.hidden = false;
  copy.addEventListener("click", async () => {
    const text = document.getElementById(copy.dataset.copy);
    const status = document.getElementById("copy-status");
    try {
      await navigator.clipboard.writeText(text.textContent);
      status.textContent = "Copied";
    } catch {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(text);
      selection.removeAllRanges();
      selection.addRange(range);
      status.textContent = "Selected for manual copy";
    }
  });
}

const traceSearch = document.querySelector("[data-trace-search]");
document.addEventListener("keydown", (event) => {
  if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey || !traceSearch) return;
  if (event.target.closest("input, select, textarea, [contenteditable]")) return;
  if (getComputedStyle(traceSearch).visibility === "hidden" || !traceSearch.offsetParent) return;
  event.preventDefault();
  traceSearch.focus();
});

const loading = document.querySelector(".loading");
function showLoading() {
  loading.hidden = false;
}
document.querySelectorAll("form[data-loading]").forEach((form) => {
  form.addEventListener("submit", showLoading);
});
document.querySelectorAll("a[data-loading]").forEach((link) => {
  link.addEventListener("click", (event) => {
    if (event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {
      showLoading();
    }
  });
});
window.addEventListener("pageshow", () => {
  loading.hidden = true;
});

const graph = document.querySelector(".service-graph");
if (graph) {
  const controls = document.querySelector(".map-controls");
  const canvas = document.querySelector(".map-canvas");
  const width = Number(graph.getAttribute("width"));
  const height = Number(graph.getAttribute("height"));
  let zoom = 1;
  const output = controls.querySelector("[data-map-scale]");
  function renderZoom() {
    graph.setAttribute("width", String(width * zoom));
    graph.setAttribute("height", String(height * zoom));
    if (output) output.textContent = Math.round(zoom * 100) + "%";
  }
  controls.hidden = false;
  // Shrink slightly wider graphs to fit; larger ones scroll so labels stay readable.
  const widthFit = canvas.clientWidth / width;
  if (widthFit < 1 && widthFit >= 0.7) {
    zoom = widthFit;
    renderZoom();
  }
  controls.addEventListener("click", (event) => {
    const button = event.target.closest("[data-map-zoom]");
    if (!button) return;
    const action = button.dataset.mapZoom;
    if (action === "reset") {
      zoom = Math.min(1, canvas.clientWidth / width, canvas.clientHeight / height);
      canvas.scrollTo(0, 0);
    } else if (action === "actual") {
      zoom = 1;
    } else {
      zoom = Math.max(0.25, Math.min(2, zoom * (action === "in" ? 1.25 : 0.8)));
    }
    renderZoom();
  });
}

// Span links end in #span-details so narrow layouts jump to the details below the
// map. On wide layouts the details sit beside the map, so the jump would only hide
// the trace summary: open a trace at the top, and keep the reader's place when a
// span is selected from within the same trace.
const spanDetails = document.getElementById("span-details");
if (spanDetails) {
  const key = "geko.scroll:" + location.pathname;
  const wide = window.matchMedia("(min-width: 1250px)");
  document.addEventListener("click", (event) => {
    const link = event.target.closest("a[href]");
    if (!link) return;
    const url = new URL(link.getAttribute("href"), location.href);
    if (url.pathname !== location.pathname || url.hash !== "#span-details") return;
    try { sessionStorage.setItem(key, String(window.scrollY)); } catch {}
  });
  let saved = null;
  try {
    saved = sessionStorage.getItem(key);
    sessionStorage.removeItem(key);
  } catch {}
  const keepPlace = () => {
    if (location.hash !== "#span-details" || !wide.matches) return;
    if (document.activeElement === spanDetails) spanDetails.blur();
    window.scrollTo(0, saved === null ? 0 : Number(saved));
  };
  keepPlace();
  window.addEventListener("load", keepPlace, { once: true });
}

const waterfall = document.querySelector(".waterfall-disclosure");
if (waterfall) {
  const key = "geko.waterfall:" + location.pathname;
  try {
    const saved = sessionStorage.getItem(key);
    if (saved !== null) waterfall.open = saved === "open";
  } catch {}
  waterfall.addEventListener("toggle", () => {
    try { sessionStorage.setItem(key, waterfall.open ? "open" : "closed"); } catch {}
  });
}
