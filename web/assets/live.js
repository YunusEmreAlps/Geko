"use strict";

(() => {
  let results = document.getElementById("failure-results");
  if (!results?.dataset.liveUrl || !window.EventSource) return;
  const toggle = document.querySelector("[data-live-toggle]");
  const status = document.querySelector("[data-live-status]");
  const refresh = document.querySelector("[data-live-refresh]");
  const url = results.dataset.liveUrl;
  let source, timer, inFlight, pending = false, paused = false;
  toggle.hidden = false;

  function readingResults() {
    return results.contains(document.activeElement) || window.getSelection()?.isCollapsed === false;
  }

  async function update(force = false) {
    if (inFlight || document.hidden || (paused && !force)) return;
    if (!force && readingResults()) {
      status.textContent = "New data available";
      refresh.hidden = false;
      return;
    }
    pending = false;
    inFlight = new AbortController();
    const timeout = setTimeout(() => inFlight?.abort(), 5000);
    try {
      const response = await fetch(url, { signal: inFlight.signal, cache: "no-store" });
      if (!response.ok) throw new Error("Refresh failed");
      const documentCopy = new DOMParser().parseFromString(await response.text(), "text/html");
      const replacement = documentCopy.getElementById("failure-results");
      if (!replacement) throw new Error("Missing results");
      // Check again after the network await so a newly focused link stays put.
      if (!force && readingResults()) {
        pending = true;
        refresh.hidden = false;
        status.textContent = "New data available";
        return;
      }
      results.replaceWith(replacement);
      results = replacement;
      refresh.hidden = true;
      status.textContent = "Live · updated " + new Date().toLocaleTimeString();
    } catch {
      pending = true;
      refresh.hidden = false;
      status.textContent = "Refresh unavailable · retry";
    } finally {
      clearTimeout(timeout);
      inFlight = undefined;
      if (pending && !paused && !document.hidden) schedule();
    }
  }

  function schedule() {
    pending = true;
    if (!timer) timer = setTimeout(() => { timer = undefined; update(); }, 3000);
  }
  function disconnect() {
    source?.close(); source = undefined;
    clearTimeout(timer); timer = undefined;
    inFlight?.abort();
  }
  function connect() {
    if (source || paused || document.hidden) return;
    source = new EventSource("/events/failures");
    source.addEventListener("ready", () => { status.textContent = "Live"; schedule(); });
    source.addEventListener("changed", schedule);
    source.onerror = () => { status.textContent = "Reconnecting…"; };
  }
  toggle.addEventListener("click", () => {
    paused = !paused;
    toggle.textContent = paused ? "Resume live updates" : "Pause live updates";
    if (paused) { disconnect(); status.textContent = "Paused"; } else connect();
  });
  refresh.addEventListener("click", () => update(true));
  document.addEventListener("visibilitychange", () => document.hidden ? disconnect() : connect());
  window.addEventListener("pagehide", disconnect);
  window.addEventListener("pageshow", connect);
  connect();
})();
