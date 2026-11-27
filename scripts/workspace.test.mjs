import assert from "node:assert/strict";
import test from "node:test";
import { listURL, readWorkspace, saveView, removeView, visitTrace, emptyWorkspace } from "../web/assets/workspace.mjs";

const origin="http://127.0.0.1:4318";
test("saved list URLs retain supported filters and drop sensitive or transient state",()=>{
 const value=listURL("/failures?application=group%3Astore&service=api&evidence_service=db&range=24h&status=500&view=groups&state=muted&pinned=true&q=private-message&attribute_digest=secret&cursor=token&group=id&run_id=run#fragment",origin);
 const url=new URL(value,origin);
 assert.equal(url.pathname,"/failures");
 assert.deepEqual([...url.searchParams.keys()],["application","service","evidence_service","range","status","view","state","pinned"]);
 assert.equal(url.searchParams.get("application"),"group:store");
 for(const invalid of ["https://evil.test/traces","//evil.test/failures","javascript:alert(1)","/traces/"+"a".repeat(32),"/api/v1/failures","/traces?service="+"x".repeat(257),"/traces?range=forever","/traces?status=999","/failures?application=wrong","/traces?view=groups"]){assert.equal(listURL(invalid,origin),null,invalid)}
});

test("saved views are immutable, bounded, replace by name and remove cleanly",()=>{
 const original=emptyWorkspace();
 let state=saveView(original,"Store errors","/failures?application=group%3Astore",origin);
 assert.equal(original.views.length,0);
 state=saveView(state,"store ERRORS","/traces?range=1h",origin);
 assert.equal(state.views.length,1);
 assert.equal(state.views[0].url,"/traces?range=1h");
 assert.equal(state.lastView,"/traces?range=1h");
 for(let i=1;i<12;i++) state=saveView(state,"Project "+i,"/failures",origin);
 assert.throws(()=>saveView(state,"Extra","/failures",origin),/12/);
 assert.throws(()=>saveView(state,"x".repeat(65),"/failures",origin));
 assert.throws(()=>saveView(state,"secret\nvalue","/failures",origin));
 assert.equal(removeView(state,"store ERRORS").views.length,11);
});

test("untrusted local storage cannot inject links, oversized lists or unexpected content",()=>{
 const data={version:1,views:[{name:"Safe",url:"/traces?q=secret&service=api",payload:"private"},{name:"Evil",url:"https://evil.test/traces"}],lastView:"//evil.test/failures",remember:false,recent:[{id:"a".repeat(32),at:Date.now()}],token:"secret"};
 const state=readWorkspace(JSON.stringify(data),origin);
 assert.deepEqual(state.views,[{name:"Safe",url:"/traces?service=api"}]);
 assert.equal(state.lastView,null);
 assert.deepEqual(state.recent,[]);
 assert(!JSON.stringify(state).includes("secret"));
 assert.throws(()=>readWorkspace("{" ,origin));
 assert.throws(()=>readWorkspace(" ".repeat(65537),origin));
 assert.throws(()=>readWorkspace('{"version":2}',origin));
});

test("recent investigations require opt-in, retain IDs only, deduplicate and expire",()=>{
 const now=Date.now(),id="ab".repeat(16);
 assert.deepEqual(visitTrace(emptyWorkspace(),id,now).recent,[]);
 let state={...emptyWorkspace(),remember:true};
 state=visitTrace(state,id,now);
 state=visitTrace(state,id,now+1);
 assert.deepEqual(state.recent,[{id,at:now+1}]);
 for(let i=1;i<25;i++) state=visitTrace(state,i.toString(16).padStart(32,"0"),now+i);
 assert.equal(state.recent.length,20);
 assert.throws(()=>visitTrace(state,"bad-id",now));
 const stale={version:1,views:[],remember:true,recent:[{id,at:now-31*86400000},{id:"cd".repeat(16),at:now}]};
 assert.equal(readWorkspace(JSON.stringify(stale),origin,now).recent.length,1);
});
