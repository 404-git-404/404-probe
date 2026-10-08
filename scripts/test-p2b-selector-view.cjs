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
    setAttribute(k, v) {this.attrs[k] = v;}
    append(...nodes) {for (const n of nodes) {n.parent = this; this.children.push(n);}}
    contains(node) {return node === this || this.children.some(n => n.contains(node));}
    replaceChildren() {if (this.contains(document.activeElement)) document.activeElement = document.body; this.children = [];}
    addEventListener(type, fn) {this.listeners.set(type, fn);}
    focus() {document.activeElement = this;}
    getBoundingClientRect() {return {left: 350, top: 430, bottom: 470};}
    matches(s) {return s === ':popover-open' && Boolean(this.shown);}
    querySelectorAll(s) {const all = this.children.flatMap(n => [n, ...n.querySelectorAll(s)]); return all.filter(n => s === '[data-selector-kind]' ? n.dataset.selectorKind : s === 'button' ? n.tagName === 'BUTTON' : s === 'select' ? n.tagName === 'SELECT' : false);}
    querySelector(s) {return this.querySelectorAll(s)[0];}
    click() {this.focus(); this.listeners.get('click')?.({target: this});}
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
  f.get('summary', 'z').click(); const select = f.get('select', 'z'); select.value = 'new'; select.listeners.get('change')();
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
