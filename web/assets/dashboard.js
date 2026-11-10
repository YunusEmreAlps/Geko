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
  controls.hidden = false;
  controls.addEventListener("click", (event) => {
    const button = event.target.closest("[data-map-zoom]");
    if (!button) return;
    const action = button.dataset.mapZoom;
    if (action === "reset") {
      zoom = Math.max(0.25, Math.min(1, canvas.clientWidth / width, canvas.clientHeight / height));
    } else {
      zoom = Math.max(0.25, Math.min(2, zoom * (action === "in" ? 1.25 : 0.8)));
    }
    graph.setAttribute("width", String(width * zoom));
    graph.setAttribute("height", String(height * zoom));
  });
}
