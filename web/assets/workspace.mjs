// Browser-local navigation preferences. Never persist full request URLs or payloads.
const maxViews = 12;
const maxRecent = 20;
const maxAge = 30 * 86400000;
const controls = /[\u0000-\u001f\u007f-\u009f]/;
const traceID = /^[a-f0-9]{32}$/;

function viewName(value) {
  if (typeof value !== "string" || controls.test(value) || !value.trim() || value.trim().length > 64) return null;
  return value.trim();
}

export function listURL(raw, origin) {
  if (typeof raw !== "string" || raw.length > 16384 || controls.test(raw)) return null;
  let url;
  try { url = new URL(raw, origin); } catch { return null; }
  if (url.origin !== origin || url.username || url.password || !["/failures", "/traces"].includes(url.pathname)) return null;
  const query = new URLSearchParams();
  const allowed = {
    application: value => value === "ungrouped" || value.startsWith("group:") && new TextEncoder().encode(value).length <= 262 && value.length > 6,
    service: value => new TextEncoder().encode(value).length <= 256,
    evidence_service: value => new TextEncoder().encode(value).length <= 256,
    range: value => ["1h", "24h", "7d"].includes(value),
    status: value => /^[1-5][0-9]{2}$/.test(value),
    view: value => url.pathname === "/failures" && value === "groups",
    state: value => url.searchParams.get("view") === "groups" && ["open", "resolved", "muted"].includes(value),
    pinned: value => value === "true",
  };
  for (const [key, valid] of Object.entries(allowed)) {
    if (url.searchParams.getAll(key).length > 1) return null;
    const value = url.searchParams.get(key);
    if (!value) continue;
    if (controls.test(value) || !valid(value)) return null;
    query.set(key, value);
  }
  const result = url.pathname + (query.size ? "?" + query : "");
  return result.length <= 3072 ? result : null;
}

export function emptyWorkspace() {
  return {version: 1, views: [], lastView: null, remember: false, recent: []};
}

export function readWorkspace(raw, origin, now = Date.now()) {
  if (raw === null) return emptyWorkspace();
  if (typeof raw !== "string" || raw.length > 65536) throw new Error("Invalid local workspace");
  const input = JSON.parse(raw);
  if (!input || input.version !== 1 || !Array.isArray(input.views)) throw new Error("Invalid local workspace");
  const state = emptyWorkspace();
  const seen = new Set();
  for (const entry of input.views.slice(0, maxViews)) {
    const name = viewName(entry?.name), url = listURL(entry?.url, origin);
    if (!name || !url || seen.has(name.toLowerCase())) continue;
    state.views.push({name, url});
    seen.add(name.toLowerCase());
  }
  state.lastView = listURL(input.lastView, origin);
  state.remember = input.remember === true;
  if (state.remember && Array.isArray(input.recent)) {
    const seenIDs = new Set();
    for (const item of input.recent.slice(0, maxRecent)) {
      if (typeof item?.id !== "string" || !traceID.test(item.id) || /^0+$/.test(item.id) || seenIDs.has(item.id) || !Number.isSafeInteger(item.at) || item.at > now || item.at < now - maxAge) continue;
      state.recent.push({id: item.id, at: item.at});
      seenIDs.add(item.id);
    }
  }
  return state;
}

export function saveView(state, name, rawURL, origin) {
  name = viewName(name);
  const url = listURL(rawURL, origin);
  if (!name || !url) throw new Error("Use a name of 1–64 characters and valid project filters.");
  const views = state.views.filter(view => view.name.toLowerCase() !== name.toLowerCase());
  if (views.length >= maxViews) throw new Error("Up to 12 views can be saved. Remove one before adding another.");
  return {...state, views: [...views, {name, url}], lastView: url};
}

export function removeView(state, name) {
  return {...state, views: state.views.filter(view => view.name !== name)};
}

export function visitTrace(state, id, now = Date.now()) {
  if (!state.remember) return state;
  if (typeof id !== "string" || !traceID.test(id) || /^0+$/.test(id)) throw new Error("Invalid trace ID");
  return {...state, recent: [{id, at: now}, ...state.recent.filter(item => item.id !== id && item.at >= now - maxAge)].slice(0, maxRecent)};
}

if (typeof document !== "undefined") initializeWorkspace();

function initializeWorkspace() {
  const panel = document.querySelector("[data-workspace]");
  const resume = document.querySelector("[data-workspace-resume]");
  const key = "geko.workspace.v1:" + encodeURIComponent(document.body.dataset.workspaceScope || "local");
  const origin = location.origin;
  let state = emptyWorkspace();
  let storageError = false;
  let needsCleanup = false;
  const message = text => {
    const target = panel?.querySelector("[data-workspace-status]");
    if (target) target.textContent = text;
  };
  const load = () => readWorkspace(localStorage.getItem(key), origin);
  try {
    const raw = localStorage.getItem(key);
    state = readWorkspace(raw, origin);
    needsCleanup = raw !== null && JSON.stringify(state) !== raw;
  } catch { storageError = true; }
  const persist = next => {
    try {
      localStorage.setItem(key, JSON.stringify(next));
      state = next;
      storageError = false;
      return true;
    } catch {
      message("Browser storage is unavailable. Allow site storage to save views or recent investigations.");
      return false;
    }
  };
  const update = change => {
    try {
      const next = change(load());
      if (!persist(next)) return false;
      render();
      return true;
    } catch (error) {
      if (error.name === "SecurityError" || error.name === "QuotaExceededError") {
        message("Browser storage is unavailable. Allow site storage to save views or recent investigations.");
      } else {
        message(error.message === "Invalid local workspace" || error instanceof SyntaxError ? "Saved data could not be read. Use Clear local data to reset it." : error.message);
      }
      return false;
    }
  };
  const link = (label, url) => {
    const element = document.createElement("a");
    element.className = "text-link";
    element.textContent = label;
    element.href = url;
    return element;
  };
  function render() {
    if (resume) {
      resume.hidden = !state.lastView;
      if (state.lastView) resume.href = state.lastView;
      else resume.removeAttribute("href");
    }
    if (!panel) return;
    const list = panel.querySelector("[data-workspace-views]");
    list.replaceChildren();
    for (const view of state.views) {
      const row = document.createElement("li");
      const anchor = link(view.name, view.url);
      if (view.url === listURL(location.href, origin)) anchor.setAttribute("aria-current", "page");
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "button small ghost";
      remove.textContent = "Remove";
      remove.setAttribute("aria-label", "Remove view " + view.name);
      remove.addEventListener("click", () => {
        if (update(current => removeView(current, view.name))) {
          message("View removed.");
          panel.querySelector("input[name=view_name]").focus();
        }
      });
      row.append(anchor, remove);
      list.append(row);
    }
    panel.querySelector("[data-workspace-empty]").hidden = state.views.length > 0;
    panel.querySelector("[data-workspace-count]").textContent = state.views.length ? " (" + state.views.length + ")" : "";
    const last = panel.querySelector("[data-last-view]");
    last.hidden = !state.lastView;
    if (state.lastView) last.href = state.lastView;
    panel.querySelector("[data-remember-traces]").checked = state.remember;
    const recent = panel.querySelector("[data-workspace-recent]");
    recent.replaceChildren();
    for (const entry of state.recent) {
      const row = document.createElement("li");
      const anchor = link("Trace " + entry.id.slice(0, 8) + "…" + entry.id.slice(-4), "/traces/" + entry.id);
      anchor.title = entry.id;
      const at = document.createElement("time");
      at.dateTime = new Date(entry.at).toISOString();
      at.textContent = new Date(entry.at).toLocaleString();
      row.append(anchor, at);
      recent.append(row);
    }
    panel.querySelector("[data-recent-empty]").hidden = state.recent.length > 0;
    panel.querySelector("[data-clear-recent]").disabled = state.recent.length === 0;
  }
  // No automatic redirection or filter application. Remember only sanitized
  // list context after the user has saved a view or opted into local history.
  if (!storageError && needsCleanup) persist(state);
  const currentList = listURL(location.href, origin);
  if (!storageError && currentList && (state.views.length || state.remember) && currentList !== state.lastView) persist({...state, lastView: currentList});
  const id = document.body.dataset.workspaceTrace;
  if (!storageError && state.remember && id) persist(visitTrace(state, id));
  render();
  if (panel) {
    panel.hidden = false;
    if (storageError) message("Saved data could not be read. Allow site storage, or use Clear local data to reset it.");
    panel.querySelector("form").addEventListener("submit", event => {
      event.preventDefault();
      const input = panel.querySelector("input[name=view_name]");
      if (update(current => saveView(current, input.value, location.href, origin))) message("View saved in this browser. Search text and run-specific filters were omitted.");
    });
    panel.querySelector("[data-remember-traces]").addEventListener("change", event => {
      const remember = event.target.checked;
      if (update(current => ({...current, remember, recent: remember ? current.recent : []}))) message(remember ? "Recent investigations enabled in this browser." : "Recent investigations disabled and cleared.");
      else event.target.checked = state.remember;
    });
    panel.querySelector("[data-clear-recent]").addEventListener("click", () => {
      if (update(current => ({...current, recent: []}))) message("Recent investigations cleared.");
    });
    panel.querySelector("[data-clear-workspace]").addEventListener("click", () => {
      try {
        localStorage.removeItem(key);
        state = emptyWorkspace();
        storageError = false;
        render();
        message("Local views, last view and recent investigations cleared.");
      } catch { message("Browser storage is unavailable. Clear site data in your browser to reset it."); }
    });
  }
  window.addEventListener("storage", event => {
    if (event.key !== key && event.key !== null) return;
    try { state = load(); render(); } catch { message("Saved data changed but could not be read. Use Clear local data to reset it."); }
  });
}
