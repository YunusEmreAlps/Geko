"use strict";

// The Clipboard API needs HTTPS or localhost; staging servers reached over
// plain HTTP copy through a selected, off-screen textarea instead.
async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const buffer = document.createElement("textarea");
  buffer.value = text;
  buffer.readOnly = true;
  buffer.className = "sr-only";
  document.body.append(buffer);
  buffer.select();
  const copied = document.execCommand("copy");
  buffer.remove();
  if (!copied) throw new Error("copy failed");
}
document.querySelectorAll("[data-copy]").forEach((button) => {
  const source = document.getElementById(button.dataset.copy);
  const status = document.getElementById(button.dataset.copyStatus || "copy-status");
  if (!source || !status) return;
  button.hidden = false;
  button.addEventListener("click", async () => {
    try {
      await copyText(source.textContent);
      status.textContent = "Copied";
    } catch {
      if (source.hidden) {
        status.textContent = "Copy failed";
        return;
      }
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(source);
      selection.removeAllRanges();
      selection.addRange(range);
      status.textContent = "Selected for manual copy";
    }
    setTimeout(() => { status.textContent = ""; }, 3000);
  });
});

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
  } else if (widthFit < 0.7) {
    const focus = graph.querySelector(".map-node.is-selected .node-body") || graph.querySelector(".map-node.has-errors .node-body");
    if (focus) {
      const node = focus.getBoundingClientRect();
      const view = canvas.getBoundingClientRect();
      canvas.scrollLeft += node.left - view.left - (view.width - node.width) / 2;
      canvas.scrollTop += node.top - view.top - (view.height - node.height) / 2;
    }
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

// Mark error groups first received since this browser last listed open groups.
const groupRows = document.querySelectorAll("tr[data-first-received]");
if (groupRows.length && !new URLSearchParams(location.search).has("state") && !new URLSearchParams(location.search).has("group_cursor")) {
  const key = "geko.groups.seen";
  let seen = 0;
  try { seen = Number(localStorage.getItem(key)) || 0; } catch {}
  if (seen) {
    groupRows.forEach((row) => {
      if (Number(row.dataset.firstReceived) <= seen) return;
      const badge = document.createElement("span");
      badge.className = "pill new";
      badge.title = "First received since your last visit";
      badge.textContent = "New";
      row.querySelector(".operation").after(" ", badge);
    });
  }
  try { localStorage.setItem(key, String(Date.now())); } catch {}
}

// Mirrors formatAge in web/trace.go.
const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
function age(then, now) {
  const seconds = (now - then) / 1000;
  if (seconds < 45) return "just now";
  if (seconds < 3600) return Math.max(1, Math.floor(seconds / 60)) + " min ago";
  if (seconds < 86400) return Math.floor(seconds / 3600) + " h ago";
  if (seconds < 604800) return Math.floor(seconds / 86400) + " d ago";
  const date = new Date(then);
  const day = String(date.getUTCDate()).padStart(2, "0") + " " + months[date.getUTCMonth()];
  return date.getUTCFullYear() === new Date(now).getUTCFullYear() ? day : day + " " + date.getUTCFullYear();
}
setInterval(() => {
  const now = Date.now();
  document.querySelectorAll("time[data-age]").forEach((time) => {
    const then = Date.parse(time.dateTime);
    if (!Number.isNaN(then)) time.textContent = age(then, now);
  });
}, 30000);

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
