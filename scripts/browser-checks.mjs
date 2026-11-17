import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
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
  const response = await fetch(base + "/v1/traces", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  assert(response.ok, await response.text());
  browser = await chromium.launch({ executablePath: process.env.GEKO_BROWSER_EXECUTABLE });
  await mkdir("dist/browser-checks", { recursive: true });
  for (const theme of ["light", "dark"]) {
    for (const width of [360, 1280]) {
      const page = await browser.newPage({ viewport: { width, height: 900 } });
      await page.addInitScript((value) => localStorage.setItem("geko.theme", value), theme);
      for (const [name, path, contents] of [
        ["traces", "/traces", "GET /inventory"],
        ["groups", "/failures?view=groups", "inventory"],
        ["trace", "/traces/" + id, "Span waterfall"],
      ]) {
        assert((await page.goto(base + path)).ok(), path);
        assert.equal(await page.locator("html").getAttribute("data-theme"), theme);
        assert.equal(await page.locator("html").evaluate((html) => getComputedStyle(html).colorScheme), theme);
        assert(await page.locator("main").innerText().then((text) => text.includes(contents)), path);
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
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${path} overflows at ${width}px`);
        await page.screenshot({ path: `dist/browser-checks/${name}-${theme}-${width}.png`, animations: "disabled" });
      }
      await page.close();
    }
  }
} finally {
  await browser?.close();
  server.kill();
  await stopped;
  await rm(directory, { recursive: true, force: true });
}
