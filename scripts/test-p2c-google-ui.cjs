// Executes complete production scripts and their actual rendering/measurement functions.
const test=require('node:test'), assert=require('node:assert/strict'), fs=require('node:fs'), vm=require('node:vm');
const State=require('../web/static/agent-state.js');
class Element {
  constructor(tag='div') {this.tagName=tag.toUpperCase();this.children=[];this.value='';this.dataset={};this.attributes={};this.style={};this.ownText='';this.disabled=false;this.classList={add(){},remove(){},toggle(){}};}
  set textContent(value) {this.ownText=String(value);this.children=[];}
  get textContent() {return this.ownText+this.children.map(child=>child.textContent).join('');}
  append(...children) {this.children.push(...children);}
  replaceChildren(...children) {this.ownText='';this.children=children;}
  setAttribute(key,value) {this.attributes[key]=value;}
  addEventListener() {} querySelector(){return new Element();} querySelectorAll(){return [];}
}
test('OS1b production history security distinguishes platform unavailable and preserves setup label',()=>{
 const h=page('history.js');
 for(const [status,reason,label]of [['unavailable','platform_unsupported','此平台暂不支持（v1.1）'],['unavailable','setup_required','Setup required'],['failed','platform_unsupported','采集失败']]){
  h.run('renderSecurity(input)',{security:{supported:true,status,reason}});const text=h.nodes.get('#security-state').textContent;assert.ok(text.includes(label),text);
  if(status==='unavailable'&&reason==='platform_unsupported')assert.ok(!text.includes('Setup required'));
  assert.equal(h.nodes.get('#security-sources').children.length,0);assert.equal(h.nodes.get('#security-window').textContent,'');
 }
});

function page(file) {
  const nodes=new Map(); const node=key=>{if(!nodes.has(key))nodes.set(key,new Element());return nodes.get(key);};
  const sandbox={AgentState:{...State,fetchJSON:()=>new Promise(()=>{})},AbortController,URLSearchParams,Intl,Date,Map,Set,Number,Object,JSON,
    document:{querySelector:node,querySelectorAll:()=>[],activeElement:null,createElement:tag=>new Element(tag),addEventListener(){},removeEventListener(){}},
    location:{search:'?id='+('a'.repeat(32)),assign(){}},setInterval(){},setTimeout,queueMicrotask,addEventListener(){},
    navigator:{clipboard:{writeText:async()=>{}}},crypto:{randomUUID:()=> 'b'.repeat(32)},
    fetch:()=>new Promise(()=>{}),EventSource:class{addEventListener(){}close(){}}};
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/management.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/selector.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/network-quality-core.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/network-quality.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/traffic-chart.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/card-metrics.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/resource-history.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/device-details.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/plan-editor.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/plan-renewal.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/overview.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/plan-units.js'),'utf8'),sandbox);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static',file),'utf8'),sandbox);
  return {nodes,run:(code,input)=>{sandbox.input=input;return vm.runInContext(code,sandbox);}};
}
const result={youtube:{status:'not_cn',region:'JP',sent_to_china:false},gemini:{status:'available',region:'JPN'},search:{status:'unknown'},signin:{status:'unknown'}};
const google=(overrides={})=>({supported:true,pending:false,stale:false,result:structuredClone(result),...overrides});
test('P5b actual rendered plan summary is exact decimal quota and binary machine details remain separate',()=>{
 const app=page('app.js');const text=app.run('planPanel(input).textContent',{traffic_mode:'sum',quota_value:'0.001',quota_unit:'GiB',quota_bytes:1073742,usage_bytes:1000000});assert.match(text,/1\.0 MB \/ 0\.001073742 GB/);assert.doesNotMatch(text,/GiB|MiB/);
 const h=page('history.js');h.run('renderAgentRuntime(input)',{agent_id:'a'.repeat(32),state:{ram_used:1073741824,ram_total:2147483648,swap_used:0,swap_total:1073741824,disk_used:1073741824,disk_total:2147483648,disk_busy_percent:1,disk_read_rate:1024,disk_write_rate:2048},traffic:{rx_total:1000000000,tx_total:2000000000}});const runtime=h.nodes.get('#agent-runtime-values').textContent;assert.match(runtime,/RAM1\.0 GiB/);assert.match(runtime,/累计入站1\.0 GB/);assert.match(runtime,/累计出站2\.0 GB/);assert.match(runtime,/1\.0 KiB\/s/);
});
test('actual dashboard panel always has exactly one YouTube row without a retired service slot',()=>{
  const app=page('app.js');
  for(const view of [google(),google({supported:false}),google({pending:true}),google({result:null}),google({stale:true}),
    google({result:{...result,youtube:{status:'cn',region:'CN'},gemini:{status:'blocked'},search:{status:'challenge'},signin:{status:'blocked'}}}),
    google({result:{...result,youtube:{status:'unknown'},gemini:{status:'unknown'}}}),
    google({result:{...result,gemini:{status:'unknown',error:{category:'timeout'}}}})]) {
    const items=JSON.parse(JSON.stringify(app.run('googleCheckItems({google_status:input})',view)));
    assert.deepEqual(items.map(row=>row[0]),['YouTube']);assert.equal(items.length,1);
    const panel=app.run('googleStatusPanel({google_status:input})',view);
    assert.equal(panel.attributes['aria-label'],'YouTube 检测结果');
    const rows=panel.children.filter(child=>child.className==='google-check');
    assert.deepEqual(rows.map(child=>child.children[0].textContent),['▶']);
    assert.equal(rows[0].children[0].className,'youtube-icon');
    assert.doesNotMatch(panel.textContent,/Gemini|Search|Sign-in|登录/);
    if(view.stale) assert.deepEqual(items.map(row=>row[2]),['stale']);
    if(!view.supported) assert.deepEqual(items.map(row=>row[1]),['不支持']);
    if(view.pending) assert.deepEqual(items.map(row=>row[1]),['检测中']);
  }
});
test('actual history summary uses only retained services for current/partial/failed, retaining pending/stale/missing states',()=>{
  const history=page('history.js');
  for(const [view,want] of [
    [google(),'当前结果'],[google({result:{...result,search:{status:'blocked'},signin:{status:'challenge'}}}),'当前结果'],
    [google({result:{...result,youtube:{status:'unknown'}}}),'检测失败'],
    [google({result:{...result,gemini:{status:'unknown',error:{category:'timeout'}}}}),'当前结果'],
    [google({result:{...result,gemini:{status:'blocked'}}}),'当前结果'],
    [google({result:{...result,youtube:{status:'unknown'},gemini:{status:'unknown'}}}),'检测失败'],
    [google({result:{search:{status:'ok'},signin:{status:'reachable'}}}),'检测失败'],
    [google({pending:true}),'检测中…'],[google({stale:true}),'旧结果 · 等待在线后重新核实'],
    [google({result:null}),'尚未检测'],[google({supported:false}),'当前 Agent 不支持检测']]) {
    history.run('renderGoogleStatus(input)',{google_status:view,online:true});
    assert.equal(history.nodes.get('#google-status-state').textContent,want);
    const values=history.nodes.get('#google-status-values');
    if(view.supported && view.result) assert.deepEqual(values.children.filter(child=>child.tagName==='DT').map(child=>child.textContent),['YouTube']);
    assert.doesNotMatch(values.textContent,/Gemini|Search|Sign-in|登录/);
  }
});
test('actual jobs measurement lines retain old and new report data but show only YouTube',()=>{
  const jobs=page('jobs.js');
  for(const report of [result,{...result,search:{status:'ok'},signin:{status:'reachable'}},{youtube:{status:'unknown'},gemini:{status:'unknown'}}]) {
    const before=JSON.stringify(report),rows=JSON.parse(JSON.stringify(jobs.run('measurementLines(input)',{result:{measurement:{google_status:report}}})));
    assert.deepEqual(rows.map(row=>row[0]),['YouTube']);assert.equal(rows.length,1);
    assert.equal(JSON.stringify(report),before);
  }
});
