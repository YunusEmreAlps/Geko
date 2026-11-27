import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { once } from "node:events";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { chromium } from "playwright";

async function assertControlsFit(controls, container, description) {
  assert(await controls.count(), `${description}: controls exist`);
  const failures = await controls.evaluateAll((elements, containerSelector) => {
    const tolerance = 1;
    const viewportWidth = document.documentElement.clientWidth;
    return elements.flatMap((element) => {
      const label = element.innerText.trim() || element.getAttribute("aria-label") || element.name || element.tagName;
      const box = element.getBoundingClientRect();
      if (!element.checkVisibility() || !box.width || !box.height) return [`${label} is hidden`];

      let left = 0;
      let right = viewportWidth;
      for (let parent = element.parentElement; parent; parent = parent.parentElement) {
        if (!parent.matches(containerSelector) && getComputedStyle(parent).overflowX === "visible") continue;
        const parentLeft = parent.getBoundingClientRect().left + parent.clientLeft;
        left = Math.max(left, parentLeft);
        right = Math.min(right, parentLeft + parent.clientWidth);
      }
      if (box.left < left - tolerance || box.right > right + tolerance) {
        return [`${label} extends from ${box.left.toFixed(1)} to ${box.right.toFixed(1)}px outside ${left.toFixed(1)}–${right.toFixed(1)}px`];
      }

      if (element.matches("a, button, summary, strong")) {
        const range = document.createRange();
        range.selectNodeContents(element);
        for (const fragment of range.getClientRects()) {
          if (fragment.left < box.left - tolerance || fragment.right > box.right + tolerance ||
            fragment.top < box.top - tolerance || fragment.bottom > box.bottom + tolerance) {
            return [`${label} has content outside its own control`];
          }
        }
      }
      return [];
    });
  }, container);
  assert.deepEqual(failures, [], description);
}

async function isInViewportVertically(control) {
  return control.evaluate((element) => {
    const box = element.getBoundingClientRect();
    let top = 0;
    let bottom = document.documentElement.clientHeight;
    for (let parent = element.parentElement; parent; parent = parent.parentElement) {
      if (getComputedStyle(parent).overflowY === "visible") continue;
      const parentTop = parent.getBoundingClientRect().top + parent.clientTop;
      top = Math.max(top, parentTop);
      bottom = Math.min(bottom, parentTop + parent.clientHeight);
    }
    // clientHeight rounds to CSS pixels; rectangles retain fractional pixels.
    return box.height > 0 && box.top >= top - 1 && box.bottom <= bottom + 1;
  });
}

const binary = process.argv[2];
if (!binary) throw new Error("Usage: node scripts/browser-checks.mjs <geko binary>");
const socket = createServer().listen(0, "127.0.0.1");
await once(socket, "listening");
const port = socket.address().port;
await new Promise((done) => socket.close(done));
const base = `http://127.0.0.1:${port}`;
const directory = await mkdtemp(join(tmpdir(), "geko-browser-"));
await writeFile(join(directory,"geko.yaml"),"telemetry_budget:\n  enabled: true\n  history_days: 7\n  default:\n    spans_per_hour: 1\n    bytes_per_hour: 1\n  cardinality_limit: 1\n  attributes: [http.route]\n");
await writeFile(join(directory,'collector.yaml'),'receivers:\n  otlp: {}\nexporters:\n  debug: {}\nservice:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [debug]\n');
await writeFile(join(directory,'geko.yaml'),(await readFile(join(directory,'geko.yaml'),'utf8'))+'collector_doctor:\n  config_file: collector.yaml\n');
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
  payload.resourceSpans[0].scopeSpans[0].spans[0].attributes.push({ key: "http.route", value: { stringValue: "/inventory" } });
  payload.resourceSpans[0].resource.attributes.push({ key: "service.version", value: { stringValue: "1.0.0" } });
  // Keep run-comparison and Connect samples independent of this two-evidence trace.
  const tracePayload = structuredClone(payload);
  const child = structuredClone(tracePayload.resourceSpans[0].scopeSpans[0].spans[0]);
  child.spanId = "0200000000000000";
  child.parentSpanId = "0100000000000000";
  child.name = "POST /inventory/reserve";
  child.kind = 3;
  child.attributes.find(({ key }) => key === "http.route").value.stringValue = "/inventory/reserve";
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
    sample.resourceSpans[0].resource.attributes.find(({key}) => key === 'service.version').value.stringValue = failing ? '1.0.0' : '1.1.0';
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

  const chosenSuccess='03'+'00'.repeat(15),newestSuccess='04'+'00'.repeat(15);
  const newerSuccess=structuredClone(payload);
  const newerSpan=newerSuccess.resourceSpans[0].scopeSpans[0].spans[0];
  newerSpan.traceId=newestSuccess;newerSpan.events=[];newerSpan.status={code:1};
  newerSpan.attributes=newerSpan.attributes.filter(attribute=>!['http.response.status_code','http.status_code'].includes(attribute.key));
  newerSpan.attributes.push({key:'http.response.status_code',value:{intValue:'200'}});
  assert((await fetch(base+'/v1/traces',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(newerSuccess)})).ok);
  // A repeated check must issue a fresh request even if its URL is unchanged.
  const connectionPage = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await connectionPage.goto(base + "/connect?service=inventory&application=otlp-json-demo");
  await connectionPage.getByRole("link", { name: "Start a 60-second check", exact: true }).click();
  let observationSince;
  for (let attempt = 0; attempt < 2; attempt++) {
    await Promise.all([
      connectionPage.waitForResponse(response => new URL(response.url()).pathname === "/connect", { timeout: 3000 }),
      connectionPage.getByRole("button", { name: "Check again", exact: true }).click(),
    ]);
    const since = new URL(connectionPage.url()).searchParams.get("since");
    if (attempt === 0) {
      observationSince = since;
      const exported = await fetch(base + '/v1/traces', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(tracePayload)});
      assert(exported.ok);
    } else {
      assert.equal(since, observationSince, "repeated checks preserve the observation window");
      assert(await connectionPage.locator('#connection-check').innerText().then(text=>text.includes('Telemetry observed for inventory.')), "a repeated check reveals newly accepted telemetry");
    }
  }
  for (let attempt = 0; attempt < 2; attempt++) {
    await Promise.all([
      connectionPage.waitForResponse(response => new URL(response.url()).pathname === '/connect'),
      connectionPage.getByRole('link',{name:'Start a new 60-second check',exact:true}).click(),
    ]);
  }

  const delivery=connectionPage.locator('#delivery-diagnostics');
  assert.equal(await delivery.locator('.delivery-stages > li').count(),4,'delivery diagnostics expose four scoped stages');
  assert(await delivery.innerText().then(text=>text.includes('All applications, cumulative')&&text.includes('not an integrity check')),'diagnostics disclose evidence scope');
  await delivery.getByRole('link',{name:'Inspect Collector',exact:true}).click();
  assert(new URL(await connectionPage.getByRole('link',{name:'Refresh',exact:true}).getAttribute('href'),base).searchParams.has('return'),'Collector refresh retains Connect context');
  await connectionPage.getByRole('link',{name:'Back to connection check',exact:true}).click();
  assert.equal(new URL(connectionPage.url()).searchParams.get('service'),'inventory');
  assert.equal(new URL(connectionPage.url()).searchParams.get('application'),'otlp-json-demo');
  assert(await delivery.innerText().then(text=>text.includes('Last check available')),'explicit Collector check publishes summary');
  for (const theme of ['light','dark']) {
    await connectionPage.evaluate(value=>document.documentElement.dataset.theme=value,theme);
    for (const width of [320,360,390,440,760,761,768,1280,1440]) {
      await connectionPage.setViewportSize({width,height:900});
      await assertControlsFit(delivery.locator('a'),'.delivery-stages',`delivery links fit at ${width}px in ${theme}`);
      assert(await connectionPage.evaluate(()=>document.documentElement.scrollWidth<=document.documentElement.clientWidth+1),'delivery diagnostics do not overflow');
    }
  }
  await connectionPage.setViewportSize({width:390,height:900});
  await delivery.scrollIntoViewIfNeeded();
  await connectionPage.screenshot({path:'dist/browser-checks/delivery-diagnostics-mobile.png'});
  await connectionPage.setViewportSize({width:1280,height:900});

  const filteredURL = '/failures?application=group%3Aotlp-json-demo&range=1h';
  await connectionPage.goto(base + filteredURL);
  await connectionPage.locator(`a.operation[href^="/traces/${id}?"]`).first().click();
  assert.equal(await connectionPage.getByRole('link',{name:'Back to results',exact:true}).getAttribute('href'),filteredURL,'trace returns to its filtered list');
  const selectedSpan = new URL(connectionPage.url()).searchParams.get('span');
  await connectionPage.getByRole('link',{name:'Expand span details',exact:true}).click();
  assert(await connectionPage.locator('.reading-wide').count(),'wide reader uses the full trace workspace');
  assert.equal(new URL(connectionPage.url()).searchParams.get('span'), selectedSpan,'wide reader preserves selected span');
  assert((await connectionPage.locator('#span-details').boundingBox()).width > 700,'desktop stack reader is wider than the inspector');
  await connectionPage.screenshot({path:'dist/browser-checks/wide-stack-reader.png'});
  await connectionPage.locator('.span-properties > summary').click();
  await connectionPage.locator('.span-properties dt').filter({hasText:/^Parent span$/}).locator('xpath=..').getByRole('link').click();
  assert(await connectionPage.locator('.reading-wide').count(),'parent navigation preserves the wide reader');
  await connectionPage.locator('.waterfall a.operation').filter({hasText:'POST /inventory/reserve'}).click();
  assert(await connectionPage.locator('.reading-wide').count(),'waterfall navigation preserves the wide reader');
  await connectionPage.getByRole('link',{name:'Use split view',exact:true}).click();
  await connectionPage.getByRole('link',{name:'Compare with success',exact:true}).click();
  assert(await connectionPage.getByRole('region',{name:'Span operation differences',exact:true}).isVisible(),'comparison includes per-operation span observations');
  const sourceBack = await connectionPage.locator('.crumb').getAttribute('href');
  assert.equal(new URL(sourceBack,base).searchParams.get('return'), filteredURL,'comparison preserves the return context');
  assert(await connectionPage.getByRole('link',{name:newestSuccess,exact:true}).isVisible(),'automatic comparison selects latest matching success');
  await connectionPage.getByLabel('Successful trace ID',{exact:true}).fill(chosenSuccess);
  await connectionPage.getByRole('button',{name:'Compare selected trace',exact:true}).click();
  assert.equal(new URL(connectionPage.url()).searchParams.get('baseline'),chosenSuccess);
  assert(await connectionPage.getByRole('link',{name:chosenSuccess,exact:true}).isVisible(),'manual comparison can select an older success');
  assert(!(await connectionPage.getByRole('link',{name:newestSuccess,exact:true}).count()),'manual choice is not replaced by automatic reference');
  assert.equal(new URL(await connectionPage.locator('.crumb').getAttribute('href'),base).searchParams.get('return'),filteredURL,'selection preserves results context');
  for (const theme of ['light','dark']) {
    await connectionPage.evaluate(value=>document.documentElement.dataset.theme=value,theme);
    for (const width of [320,360,390,440,760,761,768,1280,1440]) {
      await connectionPage.setViewportSize({width,height:844});
      await assertControlsFit(connectionPage.locator('.baseline-form :is(input:not([type=hidden]),button,a)'),'.baseline-selector',`baseline selector fits at ${width}px in ${theme}`);
    }
  }
  await connectionPage.getByLabel('Successful trace ID',{exact:true}).fill(id);
  await connectionPage.getByRole('button',{name:'Compare selected trace',exact:true}).click();
  assert(await connectionPage.getByRole('alert').innerText().then(text=>text.includes('No other reference was substituted')),'ineligible selection is recoverable and explicit');
  assert.equal(await connectionPage.getByLabel('Successful trace ID',{exact:true}).inputValue(),id,'invalid choice remains editable');
  await connectionPage.getByLabel('Successful trace ID',{exact:true}).fill('fe'.repeat(16));
  await connectionPage.getByRole('button',{name:'Compare selected trace',exact:true}).click();
  assert(await connectionPage.getByRole('alert').innerText().then(text=>text.includes('not retained')),'missing baseline does not claim source trace is missing');
  await connectionPage.getByRole('link',{name:'Use automatic match',exact:true}).click();
  assert(!new URL(connectionPage.url()).searchParams.has('baseline'));
  assert(await connectionPage.getByRole('link',{name:newestSuccess,exact:true}).isVisible());
  await connectionPage.setViewportSize({width:390,height:844});
  for(const name of ['Trace comparison','Span operation differences']) {
    const region=connectionPage.getByRole('region',{name,exact:true});
    assert(await region.evaluate(element=>element.scrollWidth<=element.clientWidth+1),'all comparison columns fit on a phone');
  }
  await connectionPage.screenshot({path:'dist/browser-checks/span-comparison-mobile.png',fullPage:true});
  await connectionPage.goto(base + '/regressions');
  assert(await connectionPage.locator('#version-options option[value="1.0.0"]').count());
  assert(await connectionPage.locator('#version-options option[value="1.1.0"]').count());
  await connectionPage.goto(base + '/runs/compare');
  assert(await connectionPage.locator(`#run-options option[value="${baselineRun}"]`).count());
  assert(await connectionPage.locator(`#run-options option[value="${candidateRun}"]`).count());
  await connectionPage.goto(base + '/traces/' + 'f'.repeat(32));
  for (const name of ['Receiver diagnostics','Connect an application','Storage and retention']) {
    assert(await connectionPage.getByRole('link',{name,exact:true}).isVisible(),'missing traces offer concrete next checks');
  }
  await connectionPage.close();

  const settingsProbe = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await settingsProbe.goto(base + "/settings#receiver");
  const sectionPicker = settingsProbe.getByLabel("Settings section", { exact: true });
  assert(await sectionPicker.isVisible(), "mobile Settings has one labelled section selector");
  assert.equal(await sectionPicker.inputValue(), "receiver", "direct section links reveal their panel");
  assert(!(await settingsProbe.locator("#appearance").isVisible()), "other mobile sections are disclosed on demand");
  await sectionPicker.selectOption("appearance");
  assert(await settingsProbe.getByLabel("Theme", { exact: true }).isVisible());
  await settingsProbe.goBack();
  assert.equal(await sectionPicker.inputValue(), "receiver", "history restores the selected section");
  await settingsProbe.setViewportSize({ width: 1280, height: 900 });
  await settingsProbe.locator("#appearance").waitFor({ state: "visible" });
  assert(await settingsProbe.locator("#appearance").isVisible(), "desktop shows every section");
  await settingsProbe.setViewportSize({ width: 390, height: 844 });
  await settingsProbe.goto(base + "/settings#runtime-trace-url");
  assert(await settingsProbe.getByLabel("Trace link URL", { exact: true }).isVisible(), "field deep links reveal the owning section");
  await settingsProbe.goto(base + "/settings#unknown");
  assert(await settingsProbe.locator("#runtime-settings").isVisible(), "unknown fragments have a useful fallback");
  await settingsProbe.close();

  const budgetProbe = await browser.newPage({viewport:{width:390,height:844}});
  const budgetBefore = await fetch(base+'/api/v1/budget').then(response=>response.json());
  await budgetProbe.goto(base+'/settings#budget-limits');
  await budgetProbe.getByLabel('Reporting limits (YAML)',{exact:true}).fill('default: {spans_per_hour: 100000, bytes_per_hour: 10000000}\ncardinality_limit: 10\n');
  await budgetProbe.getByRole('button',{name:'Save Budget limits',exact:true}).click();
  await budgetProbe.getByRole('status').filter({hasText:'Budget limits active since'}).waitFor();
  const budgetAfter = await fetch(base+'/api/v1/budget').then(response=>response.json());
  assert.deepEqual(budgetAfter.total,budgetBefore.total,'live limits preserve volume');
  assert.equal(budgetAfter.cardinality_limit,10,'existing tracker uses live threshold');
  assert(budgetAfter.services.every(service=>service.span_overage===0&&service.byte_overage===0),'new limits apply without restart');
  await budgetProbe.getByLabel('Reporting limits (YAML)',{exact:true}).fill('enabled: false\n');
  await budgetProbe.getByRole('button',{name:'Save Budget limits',exact:true}).click();
  await budgetProbe.getByRole('heading',{name:'Budget limits not applied',exact:true}).waitFor();
  assert.equal((await fetch(base+'/api/v1/budget').then(response=>response.json())).cardinality_limit,10,'invalid edit retains policy');
  await budgetProbe.getByRole('link',{name:'Retry',exact:true}).click();
  await assertControlsFit(budgetProbe.locator('#budget-limits').locator('textarea, button'),'.properties','mobile Budget editor fits');
  // Restore the fixture limits before the existing budget matrix assertions.
  await budgetProbe.getByLabel('Reporting limits (YAML)',{exact:true}).fill('default: {spans_per_hour: 1, bytes_per_hour: 1}\ncardinality_limit: 1\n');
  await budgetProbe.getByRole('button',{name:'Save Budget limits',exact:true}).click();
  await budgetProbe.getByRole('status').filter({hasText:'Budget limits active since'}).waitFor();
  await budgetProbe.screenshot({path:'dist/browser-checks/budget-limits-mobile.png'});
  await budgetProbe.close();
  const ownerProbe = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await ownerProbe.goto(base + '/settings#ownership');
  await ownerProbe.getByLabel('Owner rules (YAML)', { exact: true }).fill('services:\n  inventory: runtime-review-team\napplications:\n  legacy: legacy-team\n');
  await ownerProbe.getByRole('button', { name: 'Save ownership', exact: true }).click();
  await ownerProbe.getByRole('status').filter({hasText:'Owner rules active since'}).waitFor();
  await ownerProbe.goto(base + '/failures?view=groups');
  assert(await ownerProbe.locator('.group-table').innerText().then(text=>text.includes('Owner runtime-review-team')), 'existing server uses saved owners without restart');
  const ownerState = await fetch(base + '/api/v1/settings/owners').then(response=>response.json());
  assert.equal(ownerState.owners.services.inventory,'runtime-review-team');
  assert.equal(ownerState.source.kind,'yaml');
  await ownerProbe.goto(base + '/settings#ownership');
  await ownerProbe.getByLabel('Owner rules (YAML)', {exact:true}).fill('services: &shared {inventory: wrong-team}\napplications: *shared\n');
  await ownerProbe.getByRole('button',{name:'Save ownership',exact:true}).click();
  await ownerProbe.getByRole('heading',{name:'Owners not applied',exact:true}).waitFor();
  assert.equal((await fetch(base+'/api/v1/settings/owners').then(response=>response.json())).owners.services.inventory,'runtime-review-team','invalid YAML keeps the active rules');
  await ownerProbe.getByRole('link',{name:'Retry',exact:true}).click();
  assert(await ownerProbe.getByLabel('Owner rules (YAML)',{exact:true}).isVisible(),'owner errors return to the correct mobile section');
  await ownerProbe.screenshot({path:'dist/browser-checks/ownership-mobile.png'});
  await ownerProbe.close();

  const workspaceContext = await browser.newContext({viewport:{width:390,height:844}});
  const workspace = await workspaceContext.newPage();
  const savedPanel = workspace.locator('[data-workspace]');
  const savedName = 'Storefront ' + 'x'.repeat(53);
  const filteredPath = '/failures?service=inventory&range=24h&q=private-workspace-query';
  await workspace.goto(base+filteredPath);
  await savedPanel.locator('summary').focus();
  await workspace.keyboard.press('Enter');
  await workspace.getByLabel('View name',{exact:true}).fill(savedName);
  await workspace.getByLabel('View name',{exact:true}).press('Enter');
  await savedPanel.getByRole('status').filter({hasText:'View saved in this browser'}).waitFor();
  let saved = await workspace.evaluate(()=>{
    const key=Object.keys(localStorage).find(key=>key.startsWith('geko.workspace.v1:'));
    return {key,raw:localStorage.getItem(key)};
  });
  assert(!saved.raw.includes('private-workspace-query'),'saved views omit search text');
  assert.equal(JSON.parse(saved.raw).views.length,1);
  assert.equal(JSON.parse(saved.raw).remember,false,'history is opt-in');
  for (const theme of ['light','dark']) {
    await workspace.evaluate(value=>document.documentElement.dataset.theme=value,theme);
    for (const width of [320,360,390,440,760,761,768,1280,1440]) {
      await workspace.setViewportSize({width,height:844});
      await assertControlsFit(savedPanel.locator('input, button, a:not([hidden])'),'.workspace-content',`local workspace controls fit at ${width}px in ${theme}`);
      assert(await workspace.evaluate(()=>document.documentElement.scrollWidth<=document.documentElement.clientWidth+1),'workspace has no page overflow');
    }
  }
  await workspace.setViewportSize({width:390,height:844});
  await workspace.screenshot({path:'dist/browser-checks/saved-views-mobile.png'});
  await workspace.goto(base+'/overview');
  const resume = workspace.getByRole('link',{name:'Resume last view',exact:true});
  await resume.waitFor();
  assert.equal(await resume.getAttribute('href'),'/failures?service=inventory&range=24h');
  await resume.click();
  assert.equal(new URL(workspace.url()).searchParams.get('q'),null,'resume opens saved filters only');
  await savedPanel.locator('summary').click();
  await savedPanel.getByRole('link',{name:savedName,exact:true}).click();
  await savedPanel.locator('summary').click();
  await workspace.getByLabel('View name',{exact:true}).fill(savedName.toUpperCase());
  await workspace.getByRole('button',{name:'Save current view',exact:true}).click();
  await savedPanel.getByRole('status').filter({hasText:'View saved in this browser'}).waitFor();
  assert.equal(await savedPanel.locator('[data-workspace-views] li').count(),1,'reusing a name replaces the view');
  await workspace.getByLabel('Remember recent investigations',{exact:true}).check();
  await workspace.goto(base+'/traces/'+id);
  await workspace.goto(base+'/traces/'+id+'?span=0100000000000000');
  await workspace.goto(base+'/traces/'+'fe'.repeat(16));
  await workspace.getByRole('heading',{name:'Trace not found',exact:true}).waitFor();
  await workspace.goto(base+'/traces');
  await savedPanel.locator('summary').click();
  const recent = savedPanel.locator('[data-workspace-recent] a');
  assert.equal(await recent.count(),1,'span selections deduplicate; missing traces are not recorded');
  assert.equal(await recent.getAttribute('href'),'/traces/'+id);
  await workspace.getByLabel('Remember recent investigations',{exact:true}).uncheck();
  assert.equal(await recent.count(),0,'opting out clears history');
  await savedPanel.getByRole('button',{name:'Remove view '+savedName.toUpperCase(),exact:true}).click();
  assert.equal(await savedPanel.locator('[data-workspace-views] li').count(),0);
  const workspacePeer=await workspaceContext.newPage();
  await workspacePeer.goto(base+'/traces');
  await workspacePeer.locator('[data-workspace] > summary').click();
  await workspace.getByLabel('View name',{exact:true}).fill('<img src=x onerror=alert(1)>');
  await workspace.getByRole('button',{name:'Save current view',exact:true}).click();
  await savedPanel.getByRole('status').filter({hasText:'View saved in this browser'}).waitFor();
  await workspacePeer.getByRole('link',{name:'<img src=x onerror=alert(1)>',exact:true}).waitFor();
  assert.equal(await savedPanel.locator('img').count(),0,'view names render as text, never HTML');
  await savedPanel.getByRole('button',{name:'Remove view <img src=x onerror=alert(1)>',exact:true}).click();
  await workspacePeer.locator('[data-workspace-views] li').waitFor({state:'detached'});
  await workspacePeer.close();
  await workspace.evaluate(key=>localStorage.setItem(key,'{'),saved.key);
  await workspace.reload();
  await savedPanel.locator('summary').click();
  assert(await savedPanel.getByRole('status').innerText().then(text=>text.includes('Saved data could not be read')),'corrupt storage offers recovery');
  await savedPanel.getByRole('button',{name:'Clear local data',exact:true}).click();
  assert.equal(await workspace.evaluate(key=>localStorage.getItem(key),saved.key),null,'clear removes local preferences');
  await workspace.close();
  const blockedStorage=await workspaceContext.newPage();
  await blockedStorage.addInitScript(()=>{Storage.prototype.setItem=function(){throw new DOMException('Denied','SecurityError');};});
  await blockedStorage.goto(base+'/traces?service=inventory');
  await blockedStorage.locator('[data-workspace] > summary').click();
  await blockedStorage.getByLabel('View name',{exact:true}).fill('Unavailable storage');
  await blockedStorage.getByRole('button',{name:'Save current view',exact:true}).click();
  assert(await blockedStorage.locator('[data-workspace-status]').innerText().then(text=>text.includes('Browser storage is unavailable')),'storage failure is visible');
  await blockedStorage.locator('input[name=q]').fill('inventory');
  await blockedStorage.getByRole('button',{name:'Apply',exact:true}).click();
  assert.equal(new URL(blockedStorage.url()).searchParams.get('q'),'inventory','normal filters work when persistence is unavailable');
  const deniedReads=await workspaceContext.newPage();
  await deniedReads.addInitScript(()=>{Storage.prototype.getItem=function(){throw new DOMException('Denied','SecurityError');};});
  await deniedReads.goto(base+'/traces');
  await deniedReads.locator('[data-workspace] > summary').click();
  await deniedReads.getByLabel('View name',{exact:true}).fill('Denied reads');
  await deniedReads.getByRole('button',{name:'Save current view',exact:true}).click();
  assert(await deniedReads.locator('[data-workspace-status]').innerText().then(text=>text.includes('Browser storage is unavailable')),'read-denied storage has an actionable message');
  await workspaceContext.close();

  // Disclosure boxes can spill left without changing the document's scrollWidth.
  const layout = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await layout.goto(base + "/traces/" + id);
  await layout.getByText("More actions", { exact: true }).click();
  await assertControlsFit(layout.getByRole("link", { name: "Download OTLP JSON", exact: true }),
    ".trace-tools-content, .page-head", "trace download fits its open panel at 390px");
  await layout.setViewportSize({ width: 768, height: 428 });
  await layout.goto(base + "/failures");
  await layout.getByText("More filters", { exact: true }).click();
  await assertControlsFit(layout.getByLabel("Request service", { exact: true }),
    ".popover, .filters", "open filters stay in the content area at 768px");

  const shortTrace = structuredClone(tracePayload);
  const shortTraceID = "fc".padEnd(32, "0");
  const shortTraceResource = shortTrace.resourceSpans[0];
  shortTraceResource.resource.attributes.find(({ key }) => key === "service.name").value.stringValue = "layout-short-window";
  const longOperation = "W".repeat(256);
  for (const span of shortTraceResource.scopeSpans[0].spans) span.traceId = shortTraceID;
  shortTraceResource.scopeSpans[0].spans[0].name = longOperation;
  const shortTraceReceived = await fetch(base + "/v1/traces", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(shortTrace),
  });
  assert(shortTraceReceived.ok, await shortTraceReceived.text());

  const shortViewportFailures = [];
  await layout.setViewportSize({ width: 390, height: 844 });
  await layout.goto(base + "/traces");
  const sidebar = layout.locator(".sidebar");
  const traceLookup = sidebar.getByRole("textbox", { name: "Go to trace ID" });
  await sidebar.getByRole("button", { name: "Search", exact: true }).click();
  await layout.keyboard.press("Tab");
  assert(await sidebar.getByRole("button", { name: "Go", exact: true }).evaluate((element) => element === document.activeElement));
  await layout.setViewportSize({ width: 761, height: 844 });
  await layout.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
  if (!(await traceLookup.evaluate((element) => element === document.activeElement))) {
    shortViewportFailures.push("Resizing to desktop from the mobile Go button moves focus to the trace lookup");
  }

  await layout.setViewportSize({ width: 390, height: 844 });
  await layout.goto(base + "/traces");
  await sidebar.getByRole("button", { name: "Menu", exact: true }).click();
  const resultSearch = layout.locator('input[name="q"]');
  await resultSearch.focus();
  await layout.keyboard.press("Escape");
  if (!(await resultSearch.evaluate((element) => element === document.activeElement))) {
    shortViewportFailures.push("Escape outside the mobile Menu keeps focus in the results search field");
  }

  for (const [width, height, traceID, title] of [
    [761, 260, shortTraceID, longOperation],
    [768, 200, id, "GET /inventory"],
  ]) {
    await layout.setViewportSize({ width, height });
    await layout.goto(base + "/traces/" + traceID);
    await layout.getByRole("heading", { name: title, exact: true }).waitFor();
    const moreActions = layout.getByText("More actions", { exact: true });
    await moreActions.focus();
    await layout.keyboard.press("Enter");
    const download = layout.getByRole("link", { name: "Download OTLP JSON", exact: true });
    await download.focus();
    await download.scrollIntoViewIfNeeded();
    if (!(await isInViewportVertically(download))) {
      shortViewportFailures.push(`The trace download can scroll into a ${width}×${height}px viewport`);
    }
  }

  await layout.setViewportSize({ width: 761, height: 260 });
  await layout.goto(base + "/traces");
  const sidebarSettings = sidebar.getByRole("link", { name: "Settings", exact: true });
  await sidebarSettings.focus();
  await sidebarSettings.scrollIntoViewIfNeeded();
  if (!(await isInViewportVertically(sidebarSettings))) {
    shortViewportFailures.push("The sidebar Settings link scrolls into view when focused in a 260px-tall viewport");
  }
  assert.deepEqual(shortViewportFailures, [], "viewport changes and short windows preserve access to controls");

  await layout.setViewportSize({ width: 320, height: 844 });
  const longService = "W".repeat(64);
  await layout.goto(base + `/connect?service=${longService}`);
  await layout.getByRole("link", { name: "Start a 60-second check", exact: true }).click();
  const serviceTraces = layout.getByRole("link", { name: "View service traces", exact: true });
  assert.equal(await serviceTraces.count(), 1, "Connect provides a short service traces link at 320px");
  assert.equal(new URL(await serviceTraces.getAttribute("href"), base).searchParams.get("service"), longService);
  assert.equal(await layout.getByLabel("Service name", { exact: true }).inputValue(), longService);
  await assertControlsFit(serviceTraces, "#connection-check", "Connect's service traces link fits at 320px");
  const waitingService = layout.getByRole("status").locator("strong");
  assert.equal(await waitingService.innerText(), longService);
  await assertControlsFit(waitingService, ".notice",
    "Connect's waiting status contains the full service name at 320px");
  await layout.close();

  for (const theme of ["light", "dark"]) {
    for (const width of [320, 360, 390, 440, 760, 768, 1024, 1280, 1440]) {
      const page = await browser.newPage({ viewport: { width, height: width === 760 ? 428 : 900 } });
      await page.addInitScript((value) => localStorage.setItem("geko.theme", value), theme);
      for (const [name, path, contents] of [
        ["traces", "/traces", "GET /inventory"],
        ["groups", "/failures?view=groups", "inventory"],
        ["trace", "/traces/" + id, "Span waterfall"],
        ["connect", "/connect?service=inventory", "Connect an application"],
        ["applications", "/applications", "Service mappings"],
        ["runs", `/runs/compare?baseline=${baselineRun}&candidate=${candidateRun}`, "Not observed in candidate"],
        ["budget", "/budget", "Retaining up to 7 days"],
        ["settings", "/settings", "Live settings"],
      ]) {
        assert((await page.goto(base + path)).ok(), path);
        assert.equal(await page.locator("html").getAttribute("data-theme"), theme);
        assert.equal(await page.locator("html").evaluate((html) => getComputedStyle(html).colorScheme), theme);
        const mobile = width <= 760;
        const sidebar = page.locator(".sidebar");
        const menu = sidebar.getByRole("button", { name: "Menu", exact: true });
        const search = sidebar.getByRole("button", { name: "Search", exact: true });
        const traceSearch = sidebar.getByRole("textbox", { name: "Go to trace ID", includeHidden: true });
        assert.equal(await traceSearch.isVisible(), !mobile, `trace lookup initial visibility at ${width}px`);
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
        if (mobile) {
          assert.equal(await search.getAttribute("aria-expanded"), "true", "trace shortcut opens Search");
          await page.keyboard.press("Escape");
          assert.equal(await search.getAttribute("aria-expanded"), "false", "Escape closes Search");
          assert(await search.evaluate((element) => element === document.activeElement), "search focus returns to its trigger");
        } else {
          await traceSearch.evaluate((element) => element.blur());
        }
        if (name === "traces" && mobile) {
          const navigation = sidebar.getByRole("navigation", { name: "Main navigation", includeHidden: true });
          assert.equal(await menu.getAttribute("aria-expanded"), "false", "Menu starts closed");
          assert(!(await navigation.isVisible()), "closed Menu hides its navigation links");
          for (const trigger of [menu, search]) {
            const box = await trigger.boundingBox();
            assert(box && box.width >= 44 && box.height >= 44, "mobile disclosure has a 44px touch target");
            await assertControlsFit(trigger, ".sidebar", `mobile disclosure fits at ${width}px`);
          }
          await search.click();
          assert.equal(await search.getAttribute("aria-expanded"), "true", "Search button opens the trace lookup");
          assert(await traceSearch.evaluate((element) => element === document.activeElement), "Search button focuses the lookup");
          await assertControlsFit(traceSearch, ".sidebar", `open trace lookup fits at ${width}px`);
          await page.keyboard.press("Escape");
          await menu.focus();
          await page.keyboard.press("Enter");
          assert.equal(await menu.getAttribute("aria-expanded"), "true", "keyboard opens Menu");
          await assertControlsFit(sidebar.getByRole("link"), ".sidebar", `open menu links fit at ${width}px`);
          await navigation.getByRole("link", { name: "Overview", exact: true }).focus();
          await page.keyboard.press("Escape");
          assert.equal(await menu.getAttribute("aria-expanded"), "false", "Escape closes Menu");
          assert(await menu.evaluate((element) => element === document.activeElement), "menu focus returns to its trigger");
          assert(!(await navigation.isVisible()), "menu navigation is hidden after Escape");
        }
        if (name === "budget") {
          const usage = page.getByRole("region", { name: "Hourly usage by service", exact: true });
          assert(await usage.locator("tbody td").first().innerText().then((text) => text.includes("over")), "populated budget has a real configured overage");
          assert(await usage.innerText().then((text) => text.includes("http.route") && text.includes("2 values")), "populated cardinality remains readable with usage and limits");
          const geometry = await usage.evaluate((element) => ({ width: element.clientWidth, scroll: element.scrollWidth }));
          if (geometry.width <= 610) assert(geometry.scroll <= geometry.width + 1, "every usage column fits without horizontal scrolling");
          await assertControlsFit(usage.locator("tbody :is(td, th)"), ".table-wrap", `all budget values fit at ${width}px`);
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
          const resourceSummary = page.locator("#span-details summary").filter({ hasText: /^Resource attributes/ });
          assert(await resourceSummary.isVisible(), "resource attributes can be reached without opening properties first");
          assert.match(await resourceSummary.innerText(), /\(\d+\)/, "attribute groups show captured counts");
          await resourceSummary.click();
          assert(await page.locator("#span-details .attributes").filter({ hasText: "service.name" }).isVisible(), "captured resource data is accessible in one step");
          await resourceSummary.click();
          await page.locator(".span-properties > summary").click();
          const propertyRows = page.locator(".span-properties .properties > div");
          assert(!(await page.locator(".span-properties dt").allTextContents()).includes("Operation"), "heading context is not repeated in properties");
          const panelShape = await page.locator("#span-details").evaluate((element) => ({
            overflow: getComputedStyle(element).overflowY,
            nestedFrames: [...element.querySelectorAll(".inspector-surface .properties, .inspector-surface .expanders")]
              .filter((child) => parseFloat(getComputedStyle(child).borderLeftWidth) > 0).length,
          }));
          assert.equal(panelShape.overflow, "visible", "inspector shares page scrolling");
          assert.equal(panelShape.nestedFrames, 0, "inspector has no nested bordered surfaces");
          const alignment = await propertyRows.first().evaluate((row) => {
            const label = row.querySelector("dt").getBoundingClientRect();
            const value = row.querySelector("dd").getBoundingClientRect();
            return { columns: Math.abs(label.top - value.top) <= 1, width: row.clientWidth };
          });
          assert.equal(alignment.columns, alignment.width > 280, "property alignment follows available inspector width");
          await assertControlsFit(propertyRows, "#span-details", `properties fit at ${width}px`);
          if ([320, 390, 1440].includes(width)) {
            await page.locator("#span-details").screenshot({ path: `dist/browser-checks/span-inspector-${theme}-${width}.png`, animations: "disabled" });
          }
          await page.locator(".span-properties > summary").click();
          assert(await page.locator(".span-events details[open]").count() > 0, "selected exception is expanded");
          const inspector = await page.locator("#span-details").boundingBox();
          const waterfall = await page.locator("#waterfall").boundingBox();
          if (width < 1250) assert(inspector.y < waterfall.y, "mobile reading order puts evidence before the waterfall");
          const waterfallRegion = page.getByRole("region", { name: "Span waterfall", exact: true });
          assert(await waterfallRegion.evaluate((element) => element.scrollWidth <= element.clientWidth + 1), "waterfall duration fits without sideways scrolling");
          await assertControlsFit(page.locator(".waterfall td.time"), ".table-wrap", `waterfall durations fit at ${width}px`);
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
          await assertControlsFit(more.locator(".trace-tools-content :is(a, button)"),
            ".trace-tools-content, .page-head", `open trace actions fit at ${width}px`);
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
          assert.equal(await sections.isVisible(), width > 760, "desktop uses anchor navigation; mobile uses a selector");
          if (width > 760) await assertControlsFit(sections.getByRole("link"), ".section-nav", `Settings section links fit at ${width}px`);
          for (const href of await sections.locator('a[href^="#"]').evaluateAll((links) => links.map((link) => link.getAttribute("href")))) {
            assert(href?.startsWith("#"), "Settings section links stay within the page");
            assert.equal(await page.locator(href).count(), 1, `Settings section ${href} exists`);
            if (width <= 760) {
              await page.getByLabel("Settings section", { exact: true }).selectOption(href.slice(1));
              assert(await page.locator(href).isVisible(), "each selected settings section is available");
              if (href === '#operations') await assertControlsFit(page.locator('#operations :is(input:not([type=hidden]),button)'),'.operational-settings-form',`operational fields fit at ${width}px`);
              if (href === '#budget-limits') await assertControlsFit(page.locator('#budget-limits :is(textarea,button)'),'.owner-settings-form',`Budget editor fits at ${width}px`);
              if (href === '#ownership') await assertControlsFit(page.locator('#ownership .owner-settings-form :is(textarea, button)'), '.owner-settings-form', `ownership editor fits at ${width}px`);
            }
          }
          if (width <= 760) await page.getByLabel("Settings section", { exact: true }).selectOption("runtime-settings");
          const savedURL = `https://tempo.example/trace/{trace_id}`;
          const viewer = width === 390 ? "W".repeat(40) : "Tempo test";
          await assertControlsFit(page.locator(".runtime-settings-form :is(input:not([type=hidden]), button)"),
            ".runtime-settings-form", `Live settings fields and Save and apply fit at ${width}px`);
          await page.getByLabel('Trace link URL', { exact: true }).fill(savedURL);
          await page.getByLabel('Link name', { exact: true }).fill(viewer);
          await page.getByRole('button', { name: 'Save and apply', exact: true }).click();
          await page.getByRole('status').filter({ hasText: 'Saved and applied' }).waitFor();
          assert.equal(await page.getByLabel('Trace link URL', { exact: true }).inputValue(), savedURL);
          await page.goto(base + '/traces/' + id);
          await page.locator('.trace-tools > summary').click();
          const external = page.getByRole('link', { name: `Open in ${viewer}`, exact: true });
          assert.equal(await external.getAttribute('href'), `https://tempo.example/trace/${id}`);
          await assertControlsFit(external, ".trace-tools-content, .page-head", `external viewer label fits at ${width}px`);
          await page.goto(base + '/settings');
          if (theme === 'light' && width === 360) {
            await page.getByLabel('Trace link URL', { exact: true }).fill('javascript:invalid');
            await page.getByRole('button', { name: 'Save and apply', exact: true }).click();
            await page.getByRole('heading', { name: 'Settings not applied', exact: true }).waitFor();
            const retry = page.getByRole('link', { name: 'Retry', exact: true });
            assert.equal(await retry.getAttribute('href'), '/settings#runtime-settings');
            await retry.click();
            assert.equal(await page.getByLabel('Trace link URL', { exact: true }).inputValue(), savedURL, 'failed saves retain the active setting');
          }
        }
        if (name === "traces") {
          const moreFilters = page.getByText("More filters", { exact: true });
          await moreFilters.click();
          await assertControlsFit(page.locator(".popover :is(input, select, button)"),
            ".popover, .filters", `open filters fit at ${width}px`);
          await moreFilters.click();
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
          assert(button.y >= field.y + field.height || button.x >= field.x + field.width, "restore controls do not overlap");
          if (width <= 360) assert(button.y > field.y, "restore controls wrap on narrow phones");
          await assertControlsFit(file.or(submit),
            ".restore-controls", `file picker and restore button fit at ${width}px`);
          if (theme === "light" && width === 320) {
            await page.getByText("Import a failure bundle", { exact: true }).click();
            const bundle = page.getByLabel("Failure bundle").or(page.getByRole("button", { name: "Import bundle", exact: true }));
            await assertControlsFit(bundle, ".restore-controls", "failure bundle import controls fit at 320px");
          }
        }
        if (name === "connect") {
          await page.getByRole("link", { name: "Start a 60-second check", exact: true }).click();
          assert.match(await page.getByRole("status").innerText(), /Waiting for telemetry/);
          const received = await fetch(base + "/v1/traces", {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload),
          });
          assert(received.ok, await received.text());
          await page.getByRole("button", { name: "Check again", exact: true }).click();
          assert.match(await page.getByRole("status").innerText(), /Telemetry observed/);
          assert(await page.getByRole("link", { name: "Open trace", exact: true }).count() > 0);
          await page.getByRole("status").scrollIntoViewIfNeeded();
        }
        if (name === "applications") {
          await assertControlsFit(page.locator(".application-form :is(input, button)"),
            ".application-form, .actions", `application form controls fit at ${width}px`);
        }
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${path} overflows at ${width}px`);
        await page.screenshot({ path: `dist/browser-checks/${name}-${theme}-${width}.png`, animations: "disabled" });
      }
      await page.close();
    }
  }

  // Three pages keep both the First page and Next page actions present together.
  const paginationFixture = structuredClone(payload);
  paginationFixture.resourceSpans[0].resource.attributes.find(({ key }) => key === "service.name").value.stringValue = "layout-pagination";
  const scope = paginationFixture.resourceSpans[0].scopeSpans[0];
  const sample = scope.spans[0];
  scope.spans = Array.from({ length: 51 }, (_, index) => ({
    ...sample, traceId: `ff${index.toString(16).padStart(30, "0")}`,
  }));
  const received = await fetch(base + "/v1/traces", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(paginationFixture),
  });
  assert(received.ok, await received.text());
  const paging = await browser.newPage({ viewport: { width: 320, height: 844 } });
  await paging.goto(base + "/traces?service=layout-pagination");
  const resultPages = paging.getByRole("navigation", { name: "Result pages", exact: true });
  await resultPages.getByRole("link", { name: "Next page", exact: true }).click();
  assert(await resultPages.getByRole("link", { name: "First page", exact: true }).isVisible());
  assert(await resultPages.getByRole("link", { name: "Next page", exact: true }).isVisible());
  await assertControlsFit(resultPages.getByRole("link"), ".pagination", "both result pagination actions fit at 320px");
  await paging.close();

  // A large stack and long identifier must remain in the page's scroll flow.
  const longStackFixture = structuredClone(tracePayload);
  const longStackID = "de".padEnd(32, "0");
  const longStackSpans = longStackFixture.resourceSpans[0].scopeSpans[0].spans;
  for (const span of longStackSpans) span.traceId = longStackID;
  longStackSpans[1].status = { code: 2, message: "payment-authorization-".repeat(20) };
  longStackSpans[1].events[0].attributes.push({ key: "exception.stacktrace", value: {
    stringValue: Array.from({ length: 60 }, (_, index) => `at payment.authorize /app/payments.go:${index + 1}`).join("\n"),
  } });
  const longStackExport = await fetch(base + "/v1/traces", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(longStackFixture),
  });
  assert(longStackExport.ok, await longStackExport.text());
  const stackPage = await browser.newPage({ viewport: { width: 1440, height: 700 } });
  for (const width of [320, 1440]) {
    await stackPage.setViewportSize({ width, height: 700 });
    await stackPage.goto(base + `/traces/${longStackID}?span=0200000000000000#span-details`);
    const inspector = stackPage.locator("#span-details");
    assert(await inspector.evaluate((element) => element.scrollHeight <= element.clientHeight + 1), "a long stack does not create a second inspector scrollbar");
    if (!(await inspector.locator(".span-properties").evaluate((element) => element.open))) {
      await inspector.locator(".span-properties > summary").click();
    }
    await assertControlsFit(inspector.locator(".properties > div"), "#span-details", "long status values stay within the inspector");
    const scopeCount = inspector.getByText("Scope attributes (0)", { exact: false });
    assert(await scopeCount.isVisible(), "empty attributes show a noninteractive zero count");
    assert.equal(await inspector.locator("summary").filter({ hasText: /^Scope attributes/ }).count(), 0);
    await scopeCount.scrollIntoViewIfNeeded();
    assert(await isInViewportVertically(scopeCount), "properties after a large stack remain reachable by page scrolling");
  }
  await stackPage.close();

  // Evidence and utility actions use native disclosures, including without JS.
  const noScript = await browser.newPage({ javaScriptEnabled: false, viewport: { width: 390, height: 900 } });
  await noScript.goto(base + '/traces/' + id);
  const lookupWithoutScript = noScript.getByRole("textbox", { name: "Go to trace ID" });
  assert(await lookupWithoutScript.isVisible(), "trace lookup is visible without JavaScript");
  assert(await noScript.getByRole("navigation", { name: "Main navigation" }).isVisible(), "navigation is visible without JavaScript");
  await lookupWithoutScript.fill(id);
  await noScript.locator(".sidebar").getByRole("button", { name: "Go", exact: true }).click();
  assert(new URL(noScript.url()).pathname === `/traces/${id}`, "trace lookup submits without JavaScript");
  const evidenceDisclosure = noScript.locator('#evidence .other-evidence');
  await evidenceDisclosure.locator('summary').click();
  assert(await evidenceDisclosure.locator('a.operation').first().isVisible(), 'all evidence remains accessible without JavaScript');
  await noScript.locator('.trace-tools > summary').click();
  assert(await noScript.getByRole('link', { name: 'Download OTLP JSON', exact: true }).isVisible(), 'download remains accessible without JavaScript');
  await noScript.goto(base+'/traces');
  assert.equal(await noScript.locator('[data-workspace]').isVisible(),false,'local views stay out of the way without JavaScript');
  await noScript.goto(base+'/traces/'+id+'/compare');
  await noScript.getByLabel('Successful trace ID',{exact:true}).fill(chosenSuccess);
  await noScript.getByRole('button',{name:'Compare selected trace',exact:true}).click();
  assert.equal(new URL(noScript.url()).searchParams.get('baseline'),chosenSuccess,'reference selection works without JavaScript');
  assert(await noScript.getByRole('link',{name:chosenSuccess,exact:true}).isVisible());
  await noScript.getByRole('link',{name:'Use automatic match',exact:true}).click();
  assert(!new URL(noScript.url()).searchParams.has('baseline'),'automatic reset works without JavaScript');
  await noScript.goto(base + "/settings");
  assert(await noScript.getByRole("navigation", { name: "Settings sections" }).isVisible(), "settings anchors survive without JavaScript");
  for (const target of ["#runtime-settings", "#appearance", "#receiver", "#configuration"]) {
    assert(await noScript.locator(target).isVisible(), "settings content survives without JavaScript");
  }
  await noScript.getByLabel('Owner rules (YAML)',{exact:true}).fill('services:\n  inventory: no-script-team\n');
  await noScript.getByRole('button',{name:'Save ownership',exact:true}).click();
  assert(await noScript.getByRole('status').filter({hasText:'Owner rules active since'}).isVisible(),'ownership saves without JavaScript');
  assert.equal((await fetch(base+'/api/v1/settings/owners').then(response=>response.json())).owners.services.inventory,'no-script-team');
  await noScript.goto(base+'/settings#budget-limits');
  await noScript.getByLabel('Reporting limits (YAML)',{exact:true}).fill('default: {spans_per_hour: 5}\ncardinality_limit: 2\n');
  await noScript.getByRole('button',{name:'Save Budget limits',exact:true}).click();
  assert(await noScript.getByRole('status').filter({hasText:'Budget limits active since'}).isVisible(),'Budget limits save without JavaScript');
  assert.equal((await fetch(base+'/api/v1/budget').then(response=>response.json())).cardinality_limit,2);
  await noScript.goto(base + '/connect?service=inventory');
  await noScript.getByRole('link',{name:'Start a 60-second check',exact:true}).click();
  for(let attempt=0;attempt<2;attempt++) {
    await Promise.all([
      noScript.waitForResponse(response=>new URL(response.url()).pathname==='/connect'),
      noScript.getByRole('button',{name:'Check again',exact:true}).click(),
    ]);
  }
  assert(await noScript.locator('#delivery-diagnostics').isVisible(),'delivery diagnostics work without JavaScript');
  await noScript.getByRole('link',{name:'Inspect Collector',exact:true}).click();
  await noScript.getByRole('link',{name:'Back to connection check',exact:true}).click();
  assert.equal(new URL(noScript.url()).searchParams.get('service'),'inventory','Collector return works without JavaScript');
  await noScript.close();

  const operationsOriginal=await fetch(base+'/api/v1/settings/operations').then(r=>r.json());
  const opsPage=await browser.newPage({viewport:{width:390,height:844}});
  await opsPage.goto(base+'/settings#operations');
  await opsPage.getByLabel('Retention days',{exact:true}).fill('1');
  await opsPage.getByLabel('Main database limit (MiB)',{exact:true}).fill('64');
  await opsPage.getByLabel('Concurrent exports',{exact:true}).fill('2');
  await opsPage.getByLabel('Exception redaction',{exact:true}).fill('strict');
  await opsPage.getByLabel('Budget history days',{exact:true}).fill('0');
  await opsPage.getByRole('button',{name:'Preview operational changes',exact:true}).click();
  await opsPage.getByRole('heading',{name:'Review operational changes',exact:true}).waitFor();
  assert.equal((await fetch(base+'/api/v1/settings/operations').then(r=>r.json())).policy.retention_days,operationsOriginal.policy.retention_days,'preview does not apply');
  for(const width of [320,390,760,768,1440]) {
    await opsPage.setViewportSize({width,height:844});
    await assertControlsFit(opsPage.locator('.operations-apply-form :is(input:not([type=hidden]),button,a)'),'.operations-apply-form',`operational apply fits at ${width}px`);
  }
  await opsPage.setViewportSize({width:390,height:844});
  await opsPage.getByRole('checkbox').check();
  await opsPage.getByRole('button',{name:'Apply operational changes',exact:true}).click();
  await opsPage.getByRole('status').filter({hasText:'Operational settings active since'}).waitFor();
  const appliedOps=await fetch(base+'/api/v1/settings/operations').then(r=>r.json());
  assert.equal(appliedOps.policy.ingest_concurrency,2);assert.equal(appliedOps.policy.redaction,'strict');
  assert.equal((await fetch(base+'/api/v1/budget').then(r=>r.json())).history_days,0,'live history disable changes reports');
  const policyTrace=structuredClone(tracePayload);
  for(const resource of policyTrace.resourceSpans) for(const scope of resource.scopeSpans) for(const span of scope.spans) {
    span.traceId='fa'.repeat(16);
    for(const event of span.events||[]) for(const attr of event.attributes||[]) if(attr.key==='exception.message') attr.value={stringValue:'operation-private-message-marker'};
  }
  assert((await fetch(base+'/v1/traces',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(policyTrace)})).ok);
  const strictTrace=await fetch(base+'/api/v1/traces/'+'fa'.repeat(16)).then(r=>r.text());
  assert(!strictTrace.includes('operation-private-message-marker')&&strictTrace.includes('exception.message_hash'),'new exports use live strict normalization');
  const latestOps=await fetch(base+'/api/v1/settings/operations').then(r=>r.json());
  assert((await fetch(base+'/api/v1/settings/operations',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({revision:latestOps.revision,policy:operationsOriginal.policy,confirmed:true})})).ok);
  await opsPage.goto(base+'/settings#operations');
  await opsPage.screenshot({path:'dist/browser-checks/operational-settings-mobile.png'});
  await opsPage.close();
  const plainOps=await browser.newPage({javaScriptEnabled:false,viewport:{width:390,height:844}});
  await plainOps.goto(base+'/settings#operations');
  await plainOps.getByLabel('Concurrent exports',{exact:true}).fill('3');
  await plainOps.getByRole('button',{name:'Preview operational changes',exact:true}).click();
  await plainOps.getByRole('button',{name:'Apply operational changes',exact:true}).click();
  assert.equal((await fetch(base+'/api/v1/settings/operations').then(r=>r.json())).policy.ingest_concurrency,3,'operational edits work without JavaScript');
  await plainOps.close();

  const missingDashboardScript = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await missingDashboardScript.route("**/assets/dashboard.js", (route) => route.abort());
  await missingDashboardScript.goto(base + "/traces");
  assert(await missingDashboardScript.getByRole("textbox", { name: "Go to trace ID" }).isVisible(),
    "trace lookup stays visible when the dashboard script fails to load");
  assert(await missingDashboardScript.getByRole("navigation", { name: "Main navigation" }).isVisible(),
    "navigation stays visible when the dashboard script fails to load");
  await missingDashboardScript.close();
} finally {
  await browser?.close();
  server.kill();
  await stopped;
  await rm(directory, { recursive: true, force: true });
}
