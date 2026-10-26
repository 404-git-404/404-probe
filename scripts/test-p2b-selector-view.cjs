// Actual production view/callbacks in a minimal DOM. Not browser/pixel evidence.
const {test} = require('node:test'), assert = require('node:assert/strict');
const fs = require('node:fs'), vm = require('node:vm'), State = require('../web/static/agent-state.js');
function setup({fetcher=()=>{throw Error('unexpected POST');}}={}) {
  const document = {activeElement: null}, events = new Map();
  class Element {
    constructor(tag) {
      this.tagName = tag.toUpperCase(); this.children = []; this.dataset = {}; this.style = {}; this.attrs = {}; this.listeners = new Map(); this.isConnected = true;
      this.showModal=()=>{this.open=true;};this.close=()=>{this.open=false;this.dispatch('close');};
    }
    get nodeType(){return 1;}get nodeName(){return this.tagName;}get childNodes(){return this.children;}
    get firstChild(){return this.children[0]||null;}get nextSibling(){const a=this.parent?.children||[];return a[a.indexOf(this)+1]||null;}
    get attributes(){return Object.entries(this.attrs).map(([name,value])=>({name,value:String(value)}));}
    get parentNode(){return this.parent||null;}hasAttribute(k){return Object.hasOwn(this.attrs,k);}getAttribute(k){return this.attrs[k]??null;}
    removeAttribute(k){delete this.attrs[k];if(k==='disabled')this._disabled=false;}
    get disabled(){return this._disabled||false;}set disabled(v){this._disabled=Boolean(v);if(v)this.attrs.disabled='';else delete this.attrs.disabled;}
    insertBefore(node,before){if(node===before)return node;if(node.parent)node.parent.children=node.parent.children.filter(n=>n!==node);node.parent=this;node.isConnected=true;const i=before?this.children.indexOf(before):-1;if(i<0)this.children.push(node);else this.children.splice(i,0,node);return node;}
    remove(){if(this.parent)this.parent.children=this.parent.children.filter(n=>n!==this);this.parent=null;this.isConnected=false;}
    closest(s){if(s==='[data-selector-kind]'&&this.dataset.selectorKind||s==='[data-selector-group]'&&this.dataset.selectorGroup)return this;return this.parent?.closest(s)||null;}
    dispatch(type,event={}){const e={target:this,...event};for(let n=this;n;n=n.parent)for(const fn of n.listeners.get(type)||[])fn(e);}
    setAttribute(k, v) {this.attrs[k] = String(v);if(k==='disabled')this._disabled=true;}
    append(...nodes) {for (const n of nodes) {n.parent = this; this.children.push(n);}}
    contains(node) {return node === this || this.children.some(n => n.contains(node));}
    replaceChildren() {if (this.contains(document.activeElement)) document.activeElement = document.body; this.children = [];}
    addEventListener(type, fn) {this.listeners.set(type,[...(this.listeners.get(type)||[]),fn]);}
    focus() {document.activeElement = this;}
    getBoundingClientRect() {return {left: 350, top: 430, bottom: 470};}
    matches(s) {return s === ':popover-open' && Boolean(this.shown);}
    querySelectorAll(s) {const all = this.children.flatMap(n => [n, ...n.querySelectorAll(s)]); return all.filter(n => s.startsWith('[data-selector-kind=') ? n.dataset.selectorKind === s.match(/"([^"]+)"/)[1] : s === '[data-selector-kind]' ? n.dataset.selectorKind : s === 'button' ? n.tagName === 'BUTTON' : s === 'select:not(:disabled)' ? n.tagName === 'SELECT'&&!n.disabled : false);}
    querySelector(s) {return this.querySelectorAll(s)[0];}
    click() {this.focus(); this.dispatch('click');}
  }
  document.body = new Element('body'); document.createElement = tag => new Element(tag);
  document.createElementNS=(_,tag)=>new Element(tag);
  const context = {document, innerWidth: 390, innerHeight: 640, Date, Map, Set, Object,
    crypto:require('node:crypto').webcrypto,AbortController,setTimeout,clearTimeout,
    addEventListener: (type, fn) => events.set(type, fn)};
  vm.createContext(context); vm.runInContext(fs.readFileSync(require('node:path').join(__dirname, '../web/static/selector.js'), 'utf8'), context);
  const state = State.create(), agent = {agent_id: 'a', online: true, outbounds: {configured: true, available: true, order_source: 'config', selectors: [
    {name: 'z', current: 'old', choices: ['old', 'new']}, {name: 'a', current: 'old', choices: ['old', 'new']},
  ]}};
  state.agents.set('a', agent);
  let view;
  const controller = context.Selector.create({state, csrf: () => 'csrf', fetcher, onchange: id => view.refresh(id)});
  view = context.Selector.view({controller, state, csrf: () => 'csrf'});
  const trigger = view.trigger(agent); document.body.append(trigger); trigger.click();
  const panel = document.body.children.find(n => n.id === 'selector-popover');
  const controls = () => panel.querySelectorAll('[data-selector-kind]');
  const get = (kind, name) => controls().find(n => n.dataset.selectorKind === kind && (name === undefined || n.dataset.selectorName === name));
  return {document, events, state, agent, controller, view, trigger, panel, get};
}
test('outbound modal: one row per group, current preselection and independent drafts',()=>{
 const f=setup();assert.equal(f.panel.tagName,'DIALOG');assert.equal(f.panel.open,true);
 assert.equal(f.trigger.children.filter(n=>n.tagName==='SPAN').map(n=>n.textContent).join(''),'出站选择 ⌄');
 assert.equal(f.trigger.children[0].getAttribute('stroke'),'currentColor');assert.equal(f.trigger.title,'z：old\na：old');
 assert.deepEqual(f.panel.children.filter(n=>n.dataset.selectorGroup).map(n=>n.dataset.selectorGroup),['z','a']);
 const choice=f.get('choice','z');assert.equal(choice.tagName,'SELECT');assert.equal(choice.value,'old');
 assert.deepEqual(choice.children.map(n=>n.value),['old','new']);assert.equal(f.get('choice','a').value,'old');
 assert.equal(f.get('submit','z').disabled,true);assert.equal(f.get('submit','a').disabled,true);
 choice.value='new';choice.dispatch('change');assert.equal(f.controller.group('a','z').draft,'new');assert.equal(f.get('submit','z').disabled,false);assert.equal(f.get('submit','a').disabled,true);
 choice.value='old';choice.dispatch('change');assert.equal(f.get('submit','z').disabled,true);
 assert.equal(f.get('close').getAttribute('aria-label'),'关闭');assert.equal(f.get('submit','z').getAttribute('aria-label'),'切换 z');
});
test('R33 modal: cancel and native Esc close restore the opening trigger',()=>{
 const f=setup();f.get('close').click();assert.equal(f.panel.open,false);assert.equal(f.document.activeElement,f.trigger);
 f.trigger.click();for(const fn of f.panel.listeners.get('cancel'))fn({preventDefault(){}});
 assert.equal(f.panel.open,false);assert.equal(f.document.activeElement,f.trigger);
});
test('R33 modal: offline state keeps choices readable and disables switching',()=>{
 const f=setup();f.agent.online=false;f.view.refresh('a');
 assert.equal(f.get('choice','z').disabled,true);assert.equal(f.get('submit').disabled,true);
});
test('outbound modal: refresh retains focused keyed select; removing it falls back to close',()=>{
 const f=setup(),select=f.get('choice','z');select.focus();f.view.refresh('a');assert.equal(f.document.activeElement,select);
 f.agent.outbounds={...f.agent.outbounds,selectors:[]};f.view.refresh('a');
 assert.equal(f.get('choice','z'),undefined);assert.equal(f.document.activeElement,f.get('close'));
});
test('outbound modal: each switch submits only its own group and preserves the other draft',async()=>{
 const calls=[],f=setup({fetcher:async(url,options)=>{calls.push({url,body:JSON.parse(options.body)});return {status:409,ok:false,json:async()=>({error:{code:'switch_failed'}})};}});
 for(const name of ['z','a']){const select=f.get('choice',name);select.value='new';select.dispatch('change');}
 f.get('submit','a').click();await new Promise(setImmediate);
 assert.equal(calls.length,1);assert.equal(calls[0].url,'/api/v1/web/agents/a/outbounds/switch');
 assert.equal(calls[0].body.selector,'a');assert.equal(calls[0].body.choice,'new');assert.match(calls[0].body.request_id,/^[a-f0-9]{32}$/);
 assert.equal(f.controller.group('a','z').draft,'new');assert.equal(f.controller.group('a','z').feedback,null);
 assert.equal(f.controller.group('a','a').feedback.error,true);
});
test('outbound modal: single group keeps the same row and an invalid draft stays disabled',()=>{
 const f=setup();f.agent.outbounds.selectors.splice(1);f.view.refresh('a');
 assert.equal(f.panel.children.filter(n=>n.dataset.selectorGroup).length,1);
 const select=f.get('choice','z');select.value='new';select.dispatch('change');
 f.agent.outbounds.selectors[0].choices=['old'];f.view.refresh('a');
 assert.equal(f.get('choice','z').value,'new');assert.equal(f.get('choice','z').children[0].disabled,true);assert.equal(f.get('submit','z').disabled,true);
});
