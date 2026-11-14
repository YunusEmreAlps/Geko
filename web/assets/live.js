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
  document.querySelector("[data-manual-refresh]")?.setAttribute("hidden", "");

  function setStatus(text, state) {
    status.textContent = text;
    status.dataset.state = state;
  }

  function readingResults() {
    return results.contains(document.activeElement) || window.getSelection()?.isCollapsed === false;
  }

  async function update(force = false) {
    if (inFlight || document.hidden || (paused && !force)) return;
    if (!force && readingResults()) {
      setStatus("New data available", "stale");
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
        setStatus("New data available", "stale");
        return;
      }
      results.replaceWith(replacement);
      results = replacement;
      refresh.hidden = true;
      setStatus("Live · updated " + new Date().toLocaleTimeString(), "live");
    } catch {
      pending = true;
      refresh.hidden = false;
      setStatus("Refresh unavailable · retry", "error");
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
    source.addEventListener("ready", () => { setStatus("Live", "live"); schedule(); });
    source.addEventListener("changed", schedule);
    source.onerror = () => { setStatus("Reconnecting…", "error"); };
  }
  toggle.addEventListener("click", () => {
    paused = !paused;
    toggle.textContent = paused ? "Resume" : "Pause";
    if (paused) { disconnect(); setStatus("Paused", "paused"); } else connect();
  });
  refresh.addEventListener("click", () => update(true));
  document.addEventListener("visibilitychange", () => document.hidden ? disconnect() : connect());
  window.addEventListener("pagehide", disconnect);
  window.addEventListener("pageshow", connect);
  connect();
})();
