import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { once } from "node:events";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { chromium } from "playwright";

const binary = process.argv[2];
if (!binary) throw new Error("Usage: node scripts/browser-checks.mjs <geko binary>");
const socket = createServer().listen(0, "127.0.0.1");
await once(socket, "listening");
const port = socket.address().port;
await new Promise((done) => socket.close(done));
const base = `http://127.0.0.1:${port}`;
const directory = await mkdtemp(join(tmpdir(), "geko-browser-"));
await writeFile(join(directory,"geko.yaml"),"telemetry_budget:\n  enabled: true\n  history_days: 7\n  attributes: []\n");
const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith("GEKO_")));
const server = spawn(resolve(binary), ["serve"], {
  cwd: directory,
  env: {
    ...env, GEKO_LISTEN_ADDR: `127.0.0.1:${port}`, GEKO_DB_PATH: join(directory, "geko.db"),
    GEKO_SEARCH_ATTRIBUTES: "order.id", GEKO_ATTRIBUTE_SEARCH_KEY: "ab".repeat(32),
  },
  stdio: "inherit",
});
const stopped = once(server, "exit");
let browser;
try {
  let ready = false;
  for (let attempt = 0; attempt < 50 && !ready; attempt++) {
    ready = await fetch(base + "/healthz").then((response) => response.ok, () => false);
    if (!ready) await delay(100);
  }
  assert(ready, "Geko did not become ready");
  const fixture = await readFile(new URL("../examples/otlp-json/trace.json", import.meta.url), "utf8");
  const id = "01000000000000000000000000000000";
  const payload = JSON.parse(fixture.replaceAll("@TRACE_ID@", id).replaceAll("@SERVICE@", "inventory")
    .replaceAll("@START@", "1000000").replaceAll("@END@", "3000000"));
  payload.resourceSpans[0].scopeSpans[0].spans[0].attributes.push({ key: "order.id", value: { stringValue: "test-order-382" } });
  // Keep run-comparison and Connect samples independent of this two-evidence trace.
  const tracePayload = structuredClone(payload);
  const child = structuredClone(tracePayload.resourceSpans[0].scopeSpans[0].spans[0]);
  child.spanId = "0200000000000000";
  child.parentSpanId = "0100000000000000";
  child.name = "POST /inventory/reserve";
  child.kind = 3;
  child.events = [{
    name: "exception", timeUnixNano: "2000000",
    attributes: [
      { key: "exception.type", value: { stringValue: "InventoryTimeout" } },
      { key: "exception.message", value: { stringValue: "Synthetic inventory reservation timed out" } },
    ],
  }];
  tracePayload.resourceSpans[0].scopeSpans[0].spans.push(child);
  const response = await fetch(base + "/v1/traces", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(tracePayload),
  });
  assert(response.ok, await response.text());
  const baselineRun = 'a'.repeat(32), candidateRun = 'b'.repeat(32);
  for (const [run, traceID, failing] of [[baselineRun, '02' + '00'.repeat(15), true], [candidateRun, '03' + '00'.repeat(15), false]]) {
    const sample = structuredClone(payload);
    sample.resourceSpans[0].resource.attributes.push({ key: 'geko.run.id', value: { stringValue: run } });
    const span = sample.resourceSpans[0].scopeSpans[0].spans[0];
    span.traceId = traceID;
    if (!failing) {
      span.events = [];
      span.status = { code: 1 };
      span.attributes = span.attributes.filter(attribute => !['http.response.status_code', 'http.status_code'].includes(attribute.key));
      span.attributes.push({ key: 'http.response.status_code', value: { intValue: '200' } });
    }
    const received = await fetch(base + '/v1/traces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(sample) });
    assert(received.ok, await received.text());
  }
  browser = await chromium.launch({ executablePath: process.env.GEKO_BROWSER_EXECUTABLE });
  await mkdir("dist/browser-checks", { recursive: true });
  for (const theme of ["light", "dark"]) {
    for (const width of [320, 360, 440, 760, 768, 1280, 1440]) {
      const page = await browser.newPage({ viewport: { width, height: 900 } });
      await page.addInitScript((value) => localStorage.setItem("geko.theme", value), theme);
      for (const [name, path, contents] of [
        ["traces", "/traces", "GET /inventory"],
        ["groups", "/failures?view=groups", "inventory"],
        ["trace", "/traces/" + id, "Span waterfall"],
        ["connect", "/connect?service=inventory", "Connect an application"],
        ["runs", `/runs/compare?baseline=${baselineRun}&candidate=${candidateRun}`, "Not observed in candidate"],
        ["budget", "/budget", "Retaining up to 7 days"],
        ["settings", "/settings", "Exception redaction"],
      ]) {
        assert((await page.goto(base + path)).ok(), path);
        assert.equal(await page.locator("html").getAttribute("data-theme"), theme);
        assert.equal(await page.locator("html").evaluate((html) => getComputedStyle(html).colorScheme), theme);
        // Outer-page overflow alone misses a horizontally scrolling menu.
        // Every mobile navigation item must be reachable without sideways scroll.
        if (width <= 760) {
          const navigation = await page.locator('.sidebar').evaluate((sidebar) => ({
            fits: sidebar.scrollWidth <= sidebar.clientWidth,
            linksFit: [...sidebar.querySelectorAll('nav a')].every((link) => {
              const bounds = link.getBoundingClientRect();
              return bounds.left >= 0 && bounds.right <= innerWidth;
            }),
          }));
          assert(navigation.fits && navigation.linksFit, `mobile navigation wraps without horizontal scrolling at ${width}px`);
        }
        const traceSearch = page.getByRole("search").getByRole("textbox", { name: "Go to trace ID" });
        assert(await traceSearch.isVisible(), `global trace lookup is visible at ${width}px`);
        // A fresh page's first Tab must expose the skip link, then Enter moves
        // focus to main. Test the shortcut from there, not from a text field.
        await page.evaluate(() => document.activeElement?.blur());
        await page.keyboard.press("Tab");
        const skip = page.getByRole("link", { name: "Skip to content" });
        assert(await skip.evaluate((element) => element === document.activeElement), "skip link is first in keyboard order");
        await page.keyboard.press("Enter");
        assert(await page.locator("main").evaluate((element) => element === document.activeElement), "skip link focuses main");
        await page.keyboard.press("/");
        assert(await traceSearch.evaluate((element) => element === document.activeElement), `trace shortcut works at ${width}px`);
        await traceSearch.evaluate((element) => element.blur());
        if (name === "budget") {
          const details = page.locator("details").filter({ has: page.locator("summary", { hasText: "Measurement details" }) });
          await details.locator("summary").click();
          assert(await details.locator("p").first().isVisible(), "budget measurement disclosures remain available");
          assert.equal(await page.getByRole("link", { name: "Current hour", exact: true }).getAttribute("aria-current"), "page");
        }
        assert(await page.locator("main").innerText().then((text) => text.includes(contents)), path);
        if (name === "trace") {
          const featured = page.locator('#evidence > .evidence-list a.operation');
          assert(await featured.isVisible(), "primary evidence is immediately visible");
          await featured.click();
          assert(await page.locator("#evidence > .evidence-list a[aria-current]").isVisible(), "explicitly selected evidence stays visible");
          const others = page.locator("#evidence details").filter({ has: page.locator("summary", { hasText: "Other evidence" }) });
          assert.equal(await others.getAttribute("open"), null, "other evidence starts collapsed");
          const otherEvidence = others.locator("a.operation").first();
          assert(!(await otherEvidence.isVisible()), "secondary evidence is disclosed on demand");
          await others.locator("summary").focus();
          await page.keyboard.press("Enter");
          assert(await otherEvidence.isVisible(), "secondary evidence can be opened with the keyboard");
          await others.locator("summary").click();
          const more = page.locator("details").filter({ has: page.locator("summary", { hasText: /^More actions$/ }) });
          const download = more.getByRole("link", { name: "Download OTLP JSON", exact: true });
          assert(!(await download.isVisible()), "secondary trace actions start collapsed");
          await more.locator("summary").focus();
          await page.keyboard.press("Enter");
          assert(await download.isVisible(), "trace download is keyboard-accessible in More actions");
          assert.match(await download.getAttribute("href"), /\/otlp\.json$/);
          await more.locator("summary").click();
          for (const target of ["#waterfall", "#span-details"]) {
            assert(await page.locator(`a[href="${target}"]`).isVisible(), `${target} jump link is available`);
          }
        }
        if (name === "runs" && width >= 1000) {
          const baseline = await page.locator(".run-compare-form input[name=baseline]").boundingBox();
          const candidate = await page.locator(".run-compare-form input[name=candidate]").boundingBox();
          assert(baseline && candidate && Math.abs(baseline.width - candidate.width) <= 1, "run IDs have equal field widths");
        }
        if (name === "settings") {
          assert.equal(await page.getByLabel("Theme", { exact: true }).inputValue(), theme);
          const sections = page.getByRole("navigation", { name: "Settings sections", exact: true });
          assert(await sections.isVisible(), "Settings section navigation is visible");
          for (const href of await sections.getByRole("link").evaluateAll((links) => links.map((link) => link.getAttribute("href")))) {
            assert(href?.startsWith("#"), "Settings section links stay within the page");
            assert.equal(await page.locator(href).count(), 1, `Settings section ${href} exists`);
          }
        }
        if (name === "traces") {
          await page.getByText("Search a protected attribute", { exact: true }).click();
          await page.getByLabel("Exact value").fill("test-order-382");
          await page.getByRole("button", { name: "Find traces" }).click();
          await page.getByText("Filtered by a protected attribute", { exact: true }).waitFor();
          assert(!page.url().includes("test-order-382"));
          assert(await page.locator(`a[href^="/traces/${id}"]`).count() > 0);
          await page.getByText("Restore a trace", { exact: true }).click();
          const file = page.getByLabel("OTLP JSON file");
          const submit = page.getByRole("button", { name: "Restore trace", exact: true });
          const [field, button] = await Promise.all([file.boundingBox(), submit.boundingBox()]);
          assert(field && button, "restore controls are visible");
          assert(width < 400 ? button.y > field.y : button.y === field.y, "restore controls wrap on mobile");
        }
        if (name === "connect") {
          await page.getByRole("link", { name: "Start a 60-second check", exact: true }).click();
          assert.match(await page.getByRole("status").innerText(), /Waiting for telemetry/);
          const received = await fetch(base + "/v1/traces", {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload),
          });
          assert(received.ok, await received.text());
          await page.getByRole("link", { name: "Check again", exact: true }).click();
          assert.match(await page.getByRole("status").innerText(), /Telemetry observed/);
          assert(await page.getByRole("link", { name: "Open trace", exact: true }).count() > 0);
          await page.getByRole("status").scrollIntoViewIfNeeded();
        }
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${path} overflows at ${width}px`);
        await page.screenshot({ path: `dist/browser-checks/${name}-${theme}-${width}.png`, animations: "disabled" });
      }
      await page.close();
    }
  }
  // Evidence and utility actions use native disclosures, including without JS.
  const noScript = await browser.newPage({ javaScriptEnabled: false, viewport: { width: 390, height: 900 } });
  await noScript.goto(base + '/traces/' + id);
  const evidenceDisclosure = noScript.locator('#evidence .other-evidence');
  await evidenceDisclosure.locator('summary').click();
  assert(await evidenceDisclosure.locator('a.operation').first().isVisible(), 'all evidence remains accessible without JavaScript');
  await noScript.locator('.trace-tools > summary').click();
  assert(await noScript.getByRole('link', { name: 'Download OTLP JSON', exact: true }).isVisible(), 'download remains accessible without JavaScript');
  await noScript.close();
} finally {
  await browser?.close();
  server.kill();
  await stopped;
  await rm(directory, { recursive: true, force: true });
}
