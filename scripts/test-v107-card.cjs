// R28 (v1.0.7): the card's own logic, without a browser: faces, flipping, view models, the range slider.
const {test}=require('node:test'),assert=require('node:assert/strict');
const Card=require('../web/static/card.js');
const RS=require('../web/static/range-slider.js');
const NOW=1792080030000,DAY=86400000;

// ---- which face is shown ----
test('stored faces: three letters; anything else, or a storage that throws, is AAA',()=>{
 for(const [v,out] of [['AAB','AAB'],['bba','BBA'],['ABBA','AAA'],['AB','AAA'],['XYZ','AAA'],['',  'AAA'],[null,'AAA'],[undefined,'AAA'],[7,'AAA']])assert.equal(Card.parseFaces(v),out,String(v));
 const map=new Map(),storage={getItem:k=>map.get(k)??null,setItem:(k,v)=>map.set(k,v),removeItem:k=>map.delete(k),get length(){return map.size;},key:i=>[...map.keys()][i]};
 const store=Card.createFaceStore({storage:()=>storage});
 assert.equal(store.read('a'),'AAA');store.write('a','ABA');assert.equal(map.get('404-probe:card-face:v1:a'),'ABA');
 assert.equal(Card.createFaceStore({storage:()=>storage}).read('a'),'ABA','survives a reload');
 map.set('404-probe:card-face:v1:b','garbage');assert.equal(store.read('b'),'AAA');
 const broken=()=>{throw new Error('SecurityError');};
 const bad=Card.createFaceStore({storage:broken});
 assert.equal(bad.read('a'),'AAA');bad.write('a','BBB');assert.equal(bad.read('a'),'BBB','kept in memory for this page');bad.sync(['a']);
 const noWrite=Card.createFaceStore({storage:()=>({getItem:()=>'BAB',setItem(){throw new Error('quota');}})});
 assert.equal(noWrite.read('x'),'BAB');assert.doesNotThrow(()=>noWrite.write('x','AAA'));assert.equal(noWrite.read('x'),'AAA');
});

test('stored faces of devices that are gone are removed',()=>{
 const map=new Map([['404-probe:card-face:v1:a','AAB'],['404-probe:card-face:v1:gone','BBB'],['other','keep']]),storage={getItem:k=>map.get(k)??null,setItem:(k,v)=>map.set(k,v),removeItem:k=>map.delete(k),get length(){return map.size;},key:i=>[...map.keys()][i]};
 const store=Card.createFaceStore({storage:()=>storage});store.read('a');store.sync(['a']);
 assert.deepEqual([...map.keys()].sort(),['404-probe:card-face:v1:a','other']);
 store.sync([]);assert.equal(map.has('404-probe:card-face:v1:a'),true,'an empty list (nothing loaded yet) never wipes the choices');
});

test('the header button: any module on A sends all to B, otherwise all back to A',()=>{
 assert.equal(Card.flipAll('AAA'),'BBB');assert.equal(Card.flipAll('ABB'),'BBB');assert.equal(Card.flipAll('BBA'),'BBB');assert.equal(Card.flipAll('BBB'),'AAA');
});

// ---- the tap ----
class Node{
 constructor(){this.listeners={};this.hidden=false;this.dataset={};this.classList={add(){}};this.attributes={};this.children=[];this.parentNode=null;}
 addEventListener(t,f){(this.listeners[t]||(this.listeners[t]=[])).push(f);}
 fire(t,e={}){for(const f of this.listeners[t]||[])f(e);}
 setAttribute(k,v){this.attributes[k]=v;}append(...c){for(const n of c){n.parentNode=this;this.children.push(n);}}
 contains(n){return n===this||this.children.some(c=>c.contains(n));}
}
const target=(interactive)=>({closest:sel=>interactive&&sel===Card.INTERACTIVE?{}:null});
test('a click on blank space flips; a control, a selection or a drag does not',()=>{
 let flips=0,selected='';
 const node=new Node();Card.attachFlip(node,{toggle:()=>flips++,selection:()=>selected});
 node.fire('click',{target:target(false),detail:1});assert.equal(flips,1);
 node.fire('click',{target:target(true),detail:1});assert.equal(flips,1,'a button / select / row');
 selected='abc';node.fire('click',{target:target(false),detail:1});assert.equal(flips,1,'text is selected');selected='';
 node.fire('pointerdown',{pointerId:1,clientX:0,clientY:0});node.fire('pointermove',{pointerId:1,clientX:30,clientY:0});node.fire('pointerup',{});
 node.fire('click',{target:target(false),detail:1});assert.equal(flips,1,'the pointer moved: a drag');
 node.fire('click',{target:target(false),detail:1});assert.equal(flips,2,'the next click is a click again');
 node.fire('pointerdown',{pointerId:2,clientX:0,clientY:0});node.fire('pointermove',{pointerId:2,clientX:2,clientY:1});node.fire('pointerup',{});
 node.fire('click',{target:target(false),detail:1});assert.equal(flips,3,'a tremble of a few pixels is still a click');
 node.fire('click',{target:target(false),detail:0});assert.equal(flips,4);
});

test('the interactive elements the module must leave alone',()=>{
 for(const part of ['button','select','a','input','[role=slider]','[data-no-flip]','[role=tooltip]','output'])assert.ok(Card.INTERACTIVE.includes(part),part);
});

test('a module shows one face at a time and says which',()=>{
 const doc={activeElement:null},host=new Node(),a=new Node(),b=new Node();let seen=[];
 for(const n of [host,a,b]){n.classList={add(){}};n.closest=()=>null;}
 host.focus=()=>{};
 const m=Card.createModule({document:doc,key:'machine',label:'机器',host,a,b,onFace:f=>seen.push(f)});
 assert.equal(host.title,'点击空白处切换 简要 / 详细');
 m.set('A');assert.deepEqual([a.hidden,b.hidden,host.dataset.face],[false,true,'A']);assert.equal(b.inert,true);assert.equal(a.inert,false);
 assert.equal(m.toggle(),'B');assert.deepEqual([a.hidden,b.hidden],[true,false]);
 m.set('x');assert.equal(m.face,'B');assert.deepEqual(seen,['A','B']);
 m.set('A',{notify:false});assert.deepEqual(seen,['A','B']);
});

// ---- numbers ----
test('three significant digits',()=>{
 for(const [v,rate,out] of [[369,true,'369 B/s'],[1230,true,'1.23 KB/s'],[12300,true,'12.3 KB/s'],[123000,true,'123 KB/s'],[999.6,true,'1.00 KB/s'],[99960,true,'100 KB/s'],[9996,true,'10.0 KB/s'],[36.6e9,false,'36.6 GB'],[5e12,false,'5.00 TB'],[0,false,'0 B'],[null,false,'—'],[-1,false,'—'],[NaN,false,'—']])assert.equal(Card.sig3(v,rate),out,String(v));
});

test('ring colours: normal below 80, yellow from 80, red from 90; unknown and old are empty and grey',()=>{
 for(const [v,t] of [[0,'ok'],[79.9,'ok'],[80,'warn'],[89.9,'warn'],[90,'crit'],[100,'crit'],[null,'unknown'],[undefined,'unknown'],[-1,'unknown']])assert.equal(Card.ringTone(v),t,String(v));
 assert.equal(Card.ringTone(95,true),'unknown');assert.match(Card.ringDash(95,true),/^0\.0 /);assert.match(Card.ringDash(50),/^75\.4 150\.8$/);
});

const agent=(over={})=>({agent_id:'a',online:true,state:{cpu_percent:12.4,ram_percent:83.2,disk_percent:92.1,cpu_cores:1,load1:.09,load5:.06,load15:.01,cpu_steal_percent:0,ram_used:234*1048576,ram_total:1.44*1073741824,swap_used:0,swap_total:512*1048576,disk_used:4.49*1073741824,disk_total:24.5*1073741824,disk_read_rate:12288,disk_write_rate:49152,disk_busy_percent:1,rx_rate:369,tx_rate:265},...over});
test('machine face A: three rings; face B: four rows, with "!" only where something is wrong',()=>{
 const v=Card.machineView(agent(),{cpu:'healthy',io:'healthy'},false);
 assert.deepEqual(v.rings.map(r=>r.tone),['ok','warn','crit']);assert.deepEqual(v.rings.map(r=>r.text),['12.4%','83.2%','92.1%']);
 assert.deepEqual(v.rows.map(r=>r.key),['CPU','内存','磁盘','IP']);
 assert.equal(v.rows[0].value,'1 核 · Load 0.09 / 0.06 / 0.01 · Steal 0.0%');
 assert.match(v.rows[1].value,/^! .*Swap 0 B/);assert.match(v.rows[2].value,/^! /);
 const hot=Card.machineView(agent(),{cpu:'critical',io:'critical'},false);
 assert.match(hot.rows[0].value,/^! /);assert.equal(hot.rows[0].hot,true);assert.match(hot.rings[0].title,/CPU 持续高负载/);assert.match(hot.rings[2].title,/磁盘 I\/O 持续繁忙/);
 const calm=Card.machineView(agent({state:{...agent().state,ram_percent:20,disk_percent:30}}),{},false);
 assert.doesNotMatch(calm.rows.map(r=>r.value).join('|'),/!/);
 const old=Card.machineView(agent(),{cpu:'critical'},true);
 assert.deepEqual(old.rings.map(r=>r.tone),['unknown','unknown','unknown']);assert.ok(old.rings.every(r=>r.old));assert.match(old.rows[0].value,/旧数据/);assert.doesNotMatch(old.rows[0].value,/^!/);
 const none=Card.machineView({agent_id:'a'},{},true);assert.equal(none.rings[0].text,'—');
});

test('machine face B: the IP row follows the existing masking and freshness rules',()=>{
 const egress=o=>agent({state:{...agent().state,public_egress:o}});
 const fresh=(ip)=>({status:'available',ip,checked_at:1,valid_until:Date.now()+600000});
 globalThis.PublicEgressCheck={maskIP:ip=>ip.replace(/\.\d+\.\d+$/,'.*.*')};
 try{
  assert.equal(Card.ipRow(egress({ipv4:fresh('203.0.113.9')})),'IPv4 203.0.*.* · IPv6 —');
  assert.equal(Card.ipRow(egress({ipv4:{status:'unavailable',checked_at:1}})),'IPv4 不可用 · IPv6 —');
  assert.match(Card.ipRow(agent({online:false,state:{...agent().state,public_egress:{ipv4:fresh('203.0.113.9')}}})),/IPv4 旧 203/);
  assert.match(Card.ipRow(agent({public_egress_check:{enabled:false}})),/未检测/);
  assert.doesNotMatch(Card.ipRow(egress({ipv4:fresh('203.0.113.9')})),/203\.0\.113\.9/,'never the whole address');
 }finally{delete globalThis.PublicEgressCheck;}
});

const plan=(over={})=>({traffic_mode:'sum',cycle_kind:'monthly',cycle_count:1,cycle_anchor:'2026-10-01',quota_bytes:5e12,quota_value:'5',quota_unit:'TB',usage_bytes:36.6e9,usage_status:'',cycle_rx_bytes:20.1e9,cycle_tx_bytes:16.5e9,cycle_dir_basis:'exact',cycle_start:NOW-9*DAY-3600000,cycle_end:NOW+21*DAY-3600000,bandwidth_value:'1',bandwidth_unit:'Gbps',...over});
test('traffic face A: rates, the cycle amounts, and the quota bar in each plan state',()=>{
 const a=agent({plan:plan(),traffic:{rx_total:79.3e9,tx_total:79.1e9}});
 const v=Card.trafficView(a,{now:NOW,quotaDisplay:{value:'5',unit:'TB'}});
 assert.deepEqual([v.rx,v.tx,v.label,v.down,v.up,v.mark],['↓ 369 B/s','↑ 265 B/s','周期','20.1 GB','16.5 GB','']);
 assert.equal(v.bar.kind,'bar');assert.equal(v.bar.left,'已用 36.6 GB / 5 TB · ');assert.equal(v.bar.percent,'0.7%');assert.equal(v.bar.tone,'ok');assert.equal(v.bar.right,'第 10/30 天 · 1 Gbps');
 assert.ok(Math.abs(v.bar.time-0.3)<.03);
 assert.equal(Card.trafficView(agent({plan:plan({usage_bytes:4e12})}),{now:NOW}).bar.tone,'warn');
 assert.equal(Card.trafficView(agent({plan:plan({usage_bytes:4.6e12})}),{now:NOW}).bar.tone,'crit');
 assert.equal(Card.trafficView(agent({plan:plan({usage_bytes:6e12})}),{now:NOW}).bar.fill,100,'the bar stops at the end, the percentage does not');
 const nocap=Card.trafficView(agent({plan:plan({quota_bytes:undefined})}),{now:NOW});
 assert.equal(nocap.bar.kind,'nocap');assert.equal(nocap.bar.right,'第 10/30 天 · 1 Gbps');assert.doesNotMatch(JSON.stringify(nocap.bar),/已用|配额 /);
 const none=Card.trafficView(agent({plan:undefined,traffic:{rx_total:79.3e9,tx_total:79.1e9}}),{now:NOW});
 assert.deepEqual([none.label,none.down,none.up,none.bar.kind,none.bar.text],['累计','79.3 GB','79.1 GB','none','未设置流量配额 · 点此编辑套餐']);
 assert.equal(Card.trafficView(agent({plan:plan({cycle_kind:'none'})}),{now:NOW}).label,'累计','a plan without a cycle counts the device total');
 assert.equal(Card.trafficView(agent({plan:{currency:'USD'}}),{now:NOW}).bar.kind,'none');
 const old=Card.trafficView(a,{old:true,now:NOW});assert.deepEqual([old.rx,old.tx],['↓ —','↑ —']);
});

test('the first cycle after the upgrade says where its direction split starts',()=>{
 const since=Card.trafficView(agent({plan:plan({cycle_dir_basis:'since',cycle_dir_since:Date.UTC(2026,9,5,12)})}),{now:NOW});
 assert.equal(since.mark,'*');assert.match(since.markTitle,/自 10-0\d 起统计；此前的用量没有分方向记录/);
 const back=Card.trafficView(agent({plan:plan({cycle_dir_basis:'backfill',cycle_dir_since:Date.UTC(2026,9,1)})}),{now:NOW});
 assert.equal(back.mark,'≈');assert.match(back.markTitle,/回填/);
 assert.equal(Card.trafficView(agent({plan:plan()}),{now:NOW}).mark,'');
});

test('traffic face B: 24 hours, reset day, device total',()=>{
 const d=Card.trafficDetail(agent({plan:plan(),usage_24h:{rx:1.3e9,tx:.9e9,coverage_ms:DAY-30000},traffic:{rx_total:79.3e9,tx_total:79.1e9,started_at:Date.UTC(2026,9,1,12)}}));
 assert.equal(d.day24,'近 24h　↓ 1.30 GB　↑ 900 MB');assert.equal(d.reset,'每月 1 日重置');assert.equal(d.total,'设备累计　↓ 79.3 GB　↑ 79.1 GB');assert.match(d.since,/^自 10-\d\d 起$/);
 assert.match(Card.trafficDetail(agent({usage_24h:{rx:1e6,tx:1e6,coverage_ms:5*3600000}})).day24,/^近 5 小时/);
 assert.equal(Card.trafficDetail(agent({})).day24,'近 24h　—');
 assert.equal(Card.trafficDetail(agent({traffic:{rx_total:1,tx_total:2}})).since,'自接入起');
 assert.equal(Card.resetText(plan({cycle_kind:'days',cycle_count:7})),'每 7 天重置');assert.equal(Card.resetText(plan({cycle_kind:'yearly',cycle_anchor:'2026-03-09'})),'每年 3 月 9 日重置');assert.equal(Card.resetText({}),'未设置周期');
});

test('"last seen" for an offline card',()=>{
 assert.equal(Card.agoText(NOW-12*60000,NOW),'12 分钟前');assert.equal(Card.agoText(NOW-5*3600000,NOW),'5 小时前');assert.equal(Card.agoText(NOW-3*DAY,NOW),'3 天前');assert.equal(Card.agoText(0,NOW),'从未上报');assert.equal(Card.agoText(NOW-1000,NOW),'刚刚');
});

// ---- the range slider ----
class El{
 constructor(tag){this.tagName=tag;this.children=[];this.attributes={};this.dataset={};this.style={};this.className='';this.listeners={};}
 append(...c){this.children.push(...c);}replaceChildren(...c){this.children=c;}setAttribute(k,v){this.attributes[k]=String(v);}
 addEventListener(t,f){(this.listeners[t]||(this.listeners[t]=[])).push(f);}fire(t,e={}){for(const f of this.listeners[t]||[])f({preventDefault(){},stopPropagation(){},...e});}
 getBoundingClientRect(){return {width:240,left:0,top:0,height:14,right:240,bottom:14};}remove(){}focus(){}
 all(){return this.children.flatMap(c=>[c,...c.all()]);}
}
const doc={createElement:t=>new El(t)};
function slider(over={}){
 const calls=[],T0=NOW;
 const s=RS.create({document:doc,min:T0-DAY,max:T0,start:T0-3600000,end:T0,minSpan:600000,step:60000,follow:true,format:v=>String(v),raf:fn=>{fn();return 1;},cancelRaf(){},onChange:(r,i)=>calls.push([r,i]),...over});
 const thumb=n=>s.element.all().find(x=>x.dataset.thumb===n);
 return {s,calls,start:thumb('start'),end:thumb('end'),T0};
}
test('range slider: two independent handles, a minimum span, follow-now, keyboard',()=>{
 const {s,calls,start,end,T0}=slider();
 assert.equal(start.attributes.role,'slider');assert.equal(end.attributes['aria-valuenow'],String(T0));assert.equal(start.attributes['aria-valuemin'],String(T0-DAY));
 end.fire('keydown',{key:'ArrowLeft'});
 assert.deepEqual([s.getRange().end,s.getRange().follow],[T0-60000,false]);assert.equal(calls.at(-1)[1].committed,true);assert.equal(calls.at(-1)[1].source,'keyboard');
 end.fire('keydown',{key:'End'});assert.deepEqual([s.getRange().end,s.getRange().follow],[T0,true]);
 start.fire('keydown',{key:'ArrowRight',shiftKey:true});assert.equal(s.getRange().start,T0-3600000+600000,'shift: ten steps');
 for(let i=0;i<80;i++)start.fire('keydown',{key:'ArrowRight'});
 assert.equal(s.getRange().end-s.getRange().start,600000,'never narrower than ten minutes');
 start.fire('keydown',{key:'Home'});assert.equal(s.getRange().start,T0-DAY);
 end.fire('keydown',{key:'Home'});assert.equal(s.getRange().end-s.getRange().start,600000);
 // time moves on: a following range keeps its end on "now", a held one stays where it is
 s.setRange(T0-7200000,T0,{follow:true});s.setBounds(T0-DAY+60000,T0+60000);assert.deepEqual([s.getRange().end,s.getRange().follow],[T0+60000,true]);
 s.setRange(T0-7200000,T0-3600000);assert.equal(s.getRange().follow,false);s.setBounds(T0-DAY+120000,T0+120000);assert.equal(s.getRange().end,T0-3600000);
});

test('range slider: a drag is throttled to one notification per frame and ends committed; the wrapper keeps clicks from the module',()=>{
 const frames=[];
 const {s,calls,end,T0}=slider({raf:fn=>{frames.push(fn);return frames.length;}});
 let stopped=0;s.element.fire('click',{stopPropagation(){stopped++;}});assert.equal(stopped,1);assert.equal(s.element.dataset.noFlip,'true');
 end.fire('pointerdown',{pointerId:1,clientX:240,button:0});
 for(const x of [230,220,210])end.fire('pointermove',{pointerId:1,clientX:x});
 assert.equal(calls.length,0);assert.equal(frames.length,1,'one pending frame, however many moves');
 frames.shift()();assert.equal(calls.length,1);assert.equal(calls[0][1].committed,false);
 end.fire('pointerup',{pointerId:1});assert.equal(calls.at(-1)[1].committed,true);
 assert.ok(s.getRange().end<T0&&s.getRange().follow===false);
 // dragging back to the right edge sticks to "now" again
 end.fire('pointerdown',{pointerId:2,clientX:200,button:0});end.fire('pointermove',{pointerId:2,clientX:240});end.fire('pointerup',{pointerId:2});
 assert.deepEqual([s.getRange().end,s.getRange().follow],[T0,true]);
 s.destroy();
});

test('range slider: marks are drawn as bars inside the track, only yellow and red',()=>{
 const {s,T0}=slider({marks:[{start:T0v(-3600000),end:T0v(-3000000),state:'red'},{start:T0v(-100),end:T0v(0),state:'green'}]});
 function T0v(d){return NOW+d;}
 const bars=()=>s.element.all().filter(n=>/rs-mark rs-mark-/.test(n.className));
 assert.equal(bars().length,1);assert.match(bars()[0].className,/rs-mark-red/);
 s.setMarks([{start:NOW-DAY-5000,end:NOW-DAY+60000,state:'yellow'},{start:NOW-60000,end:NOW-60000,state:'red'}]);
 assert.equal(bars().length,1,'a mark partly before the track is clipped, an empty one is dropped');
});
