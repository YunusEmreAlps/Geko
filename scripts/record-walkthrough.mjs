// Record the walkthrough from Failures to the evidence span on the demo
// dataset, so it can be re-recorded for every release:
//
//   go build -o bin/geko ./cmd/geko
//   node scripts/record-walkthrough.mjs bin/geko docs/media
//
// It writes one screenshot per step and walkthrough.webm to the directory.
// PLAYWRIGHT_CHANNEL=msedge or chrome uses an installed browser instead of
// Playwright's own.
import assert from "node:assert/strict";
import { mkdir, readdir, rename, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { once } from "node:events";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { chromium } from "playwright";

const [binary, output = "docs/media"] = process.argv.slice(2);
if (!binary) throw new Error("Usage: node scripts/record-walkthrough.mjs <geko binary> [output directory]");
const socket = createServer().listen(0, "127.0.0.1");
await once(socket, "listening");
const port = socket.address().port;
await new Promise((done) => socket.close(done));
const base = `http://127.0.0.1:${port}`;
const out = resolve(output);
const videos = join(out, ".video");
await mkdir(videos, { recursive: true });

const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith("GEKO_")));
const server = spawn(resolve(binary), ["demo", "--no-open", "--listen", `127.0.0.1:${port}`], { env, stdio: "inherit" });
const stopped = once(server, "exit");
let browser;
try {
  let ready = false;
  for (let attempt = 0; attempt < 100 && !ready; attempt++) {
    ready = await fetch(base + "/healthz").then((response) => response.ok, () => false);
    if (!ready) await delay(100);
  }
  assert(ready, "geko demo did not become ready");
  browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || undefined });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1, colorScheme: "light",
    recordVideo: { dir: videos, size: { width: 1440, height: 900 } },
  });
  const page = await context.newPage();
  const pause = () => page.waitForTimeout(1200);
  const shot = (name) => page.screenshot({ path: join(out, `${name}.png`) });

  await page.goto(base + "/overview");
  await pause();
  await shot("1-overview");

  await page.goto(base + "/failures?view=groups&range=24h");
  await pause();
  await shot("2-error-groups");

  // The payment timeouts are the group the walkthrough follows.
  const group = page.locator("a", { hasText: "HttpTimeoutException" }).first();
  await group.scrollIntoViewIfNeeded();
  await group.click();
  await page.waitForLoadState("load"); // live pages keep a stream open
  await pause();
  await shot("3-group-traces");

  await page.locator('a[href^="/traces/"]').first().click();
  await page.waitForLoadState("load"); // live pages keep a stream open
  await pause();
  await shot("4-trace-evidence");

  await page.goto(base + "/regressions?baseline=5.12.0&candidate=5.13.0&range=24h");
  await pause();
  await shot("5-version-regressions");
  await context.close();

  const [video] = (await readdir(videos)).filter((name) => name.endsWith(".webm"));
  assert(video, "Playwright recorded no video");
  await rename(join(videos, video), join(out, "walkthrough.webm"));
  console.log(`Wrote the walkthrough to ${out}`);
} finally {
  await browser?.close();
  // A failed run can leave the video open for a moment; it is only scratch.
  await rm(videos, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }).catch(() => {});
  server.kill();
  await stopped;
}
