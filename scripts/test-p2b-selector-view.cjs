// Actual production view/callbacks in a minimal DOM. Not browser/pixel evidence.
const {test} = require('node:test'), assert = require('node:assert/strict');
const fs = require('node:fs'), vm = require('node:vm'), State = require('../web/static/agent-state.js');
function setup(native = true) {
  const document = {activeElement: null}, events = new Map();
  class Element {
    constructor(tag) {
      this.tagName = tag.toUpperCase(); this.children = []; this.dataset = {}; this.style = {}; this.attrs = {}; this.listeners = new Map(); this.isConnected = true;
      if (native) {this.showPopover = () => {this.shown = true;}; this.hidePopover = () => {this.shown = false;};}
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
    dispatch(type,event={}){const e={target:this,...event};for(let n=this;n;n=n.parent)n.listeners.get(type)?.(e);}
    setAttribute(k, v) {this.attrs[k] = String(v);if(k==='disabled')this._disabled=true;}
    append(...nodes) {for (const n of nodes) {n.parent = this; this.children.push(n);}}
    contains(node) {return node === this || this.children.some(n => n.contains(node));}
    replaceChildren() {if (this.contains(document.activeElement)) document.activeElement = document.body; this.children = [];}
    addEventListener(type, fn) {this.listeners.set(type, fn);}
    focus() {document.activeElement = this;}
    getBoundingClientRect() {return {left: 350, top: 430, bottom: 470};}
    matches(s) {return s === ':popover-open' && Boolean(this.shown);}
    querySelectorAll(s) {const all = this.children.flatMap(n => [n, ...n.querySelectorAll(s)]); return all.filter(n => s.startsWith('[data-selector-kind=') ? n.dataset.selectorKind === s.match(/"([^"]+)"/)[1] : s === '[data-selector-kind]' ? n.dataset.selectorKind : s === 'button' ? n.tagName === 'BUTTON' : s === 'select' ? n.tagName === 'SELECT' : false);}
    querySelector(s) {return this.querySelectorAll(s)[0];}
    click() {this.focus(); this.dispatch('click');}
  }
  document.body = new Element('body'); document.createElement = tag => new Element(tag);
  const context = {document, innerWidth: 390, innerHeight: 640, Date, Map, Set, Object,
    addEventListener: (type, fn) => events.set(type, fn)};
  vm.createContext(context); vm.runInContext(fs.readFileSync(require('node:path').join(__dirname, '../web/static/selector.js'), 'utf8'), context);
  const state = State.create(), agent = {agent_id: 'a', online: true, outbounds: {configured: true, available: true, order_source: 'config', selectors: [
    {name: 'z', current: 'old', choices: ['old', 'new']}, {name: 'a', current: 'old', choices: ['old', 'new']},
  ]}};
  state.agents.set('a', agent);
  let view;
  const controller = context.Selector.create({state, csrf: () => 'csrf', fetcher: () => {throw Error('unexpected POST');}, onchange: id => view.refresh(id)});
  view = context.Selector.view({controller, state, csrf: () => 'csrf'});
  const trigger = view.trigger(agent); document.body.append(trigger); trigger.click();
  const panel = document.body.children.find(n => n.id === 'selector-popover');
  const controls = () => panel.querySelectorAll('[data-selector-kind]');
  const get = (kind, name) => controls().find(n => n.dataset.selectorKind === kind && (name === undefined || n.dataset.selectorName === name));
  return {document, events, state, agent, controller, view, trigger, panel, get};
}
test('actual view: warm anchored surface outside card, order and one expansion, draft-only callback', () => {
  const f = setup(); assert.equal(f.panel.attrs.popover, 'auto');
  assert.equal(f.panel.style.width, '366px'); assert.equal(f.panel.style.left, '12px');
  assert.deepEqual(f.panel.querySelectorAll('[data-selector-kind]').filter(n => n.dataset.selectorKind === 'summary').map(n => n.dataset.selectorName), ['z', 'a']);
  f.get('summary', 'z').click(); const select = f.get('select', 'z'); select.value = 'new'; select.dispatch('change');
  assert.equal(f.controller.group('a', 'z').draft, 'new'); assert.equal(f.get('submit', 'z').disabled, false);
  f.get('summary', 'a').click(); assert.equal(f.get('select', 'z'), undefined); assert.ok(f.get('select', 'a'));
});
test('actual view: close and group control focus survives rebuild; success-collapse fallback focus', () => {
  const f = setup(); f.get('close').focus(); f.view.refresh('a'); assert.equal(f.document.activeElement, f.get('close'));
  f.get('jobs').focus(); f.view.refresh('a'); assert.equal(f.document.activeElement, f.get('jobs')); assert.equal(f.get('jobs').href, '/jobs.html');
  f.get('summary', 'z').click(); f.get('select', 'z').focus(); f.view.refresh('a'); assert.equal(f.document.activeElement, f.get('select', 'z'));
  f.controller.model('a').expanded = ''; f.view.refresh('a'); assert.equal(f.document.activeElement, f.get('summary', 'z'));
  f.get('close').click(); assert.equal(f.document.activeElement, f.trigger); assert.equal(f.panel.shown, false);
  f.view.refresh('a'); assert.equal(f.panel.shown, false);
  f.trigger.click(); f.panel.shown = false;
  f.panel.listeners.get('toggle')({newState: 'closed'});
  assert.notEqual(f.panel.hidden, true);
  f.trigger.click(); assert.equal(f.panel.shown, true);
});
test('actual view: fixed fallback outside click/Esc restore trigger; offline snapshot readable disabled controls', () => {
  const f = setup(false); assert.equal(f.panel.hidden, false); assert.equal(f.panel.attrs.popover, undefined);
  f.events.get('pointerdown')({target: f.document.body}); assert.equal(f.panel.hidden, true); assert.equal(f.document.activeElement, f.trigger);
  f.trigger.click(); f.get('summary', 'z').click(); f.agent.online = false; f.view.refresh('a');
  assert.equal(f.get('select', 'z').disabled, true); assert.equal(f.get('submit', 'z').disabled, true);
  f.events.get('keydown')({key: 'Escape', preventDefault() {}}); assert.equal(f.panel.hidden, true); assert.equal(f.document.activeElement, f.trigger);
});

test('removed focused Selector group falls back to the live panel close button', () => {
  const f = setup();
  f.get('summary', 'z').click();
  f.get('select', 'z').value = 'new';
  f.get('select', 'z').dispatch('change');
  f.get('submit', 'z').focus();
  const close = f.get('close');
  f.agent.outbounds = {...f.agent.outbounds, selectors: []};
  f.view.refresh('a');
  assert.equal(f.get('summary', 'z'), undefined);
  assert.equal(f.get('close'), close);
  assert.equal(f.document.activeElement, close);
  assert.equal(f.panel.contains(f.document.activeElement), true);
});
