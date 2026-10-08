'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const PlanEditor=require('../web/static/plan-editor.js');
// Small DOM surface; all lifecycle decisions execute the production module.
function fixture(){
 const listeners=new Map(),nodes=new Map(),cards=new Map(),agents=new Map([['a',{}],['b',{}]]);
 const doc={activeElement:null,querySelector(selector){if(selector[0]==='#')return nodes.get(selector.slice(1));const id=/data-agent-id="([^"]+)"/.exec(selector)?.[1];return selector.includes('data-focus-key')?cards.get(id)?.trigger:cards.get(id);}};
 class Element{
  constructor(){this.handlers=new Map();this.style={};this.hidden=false;this.disabled=false;this.open=false;this.isConnected=true;this.rect={left:100,top:80,bottom:680,width:560,height:600};}
  addEventListener(type,fn){if(!this.handlers.has(type))this.handlers.set(type,new Set());this.handlers.get(type).add(fn);}
  removeEventListener(type,fn){this.handlers.get(type)?.delete(fn);}
  emit(type){const e={prevented:false,preventDefault(){this.prevented=true;}};for(const fn of this.handlers.get(type)||[])fn(e);return e;}
  focus(){doc.activeElement=this;}
  showModal(){this.open=true;}
  close(){this.open=false;this.emit('close');}
  getBoundingClientRect(){return this.rect;}
 }
 for(const id of ['plan-dialog','plan-save','plan-clear','plan-confirmation','plan-confirm-note','plan-continue','plan-discard','plan-cancel','plan-dismiss'])nodes.set(id,new Element());
 for(const id of agents.keys()){const card=new Element();card.trigger=new Element();cards.set(id,card);}
 const win={innerWidth:1366,innerHeight:900,scrollX:2,scrollY:340,scrollCalls:[],scrollTo(...args){this.scrollCalls.push(args);},addEventListener(type,fn){listeners.set(type,fn);},removeEventListener(type,fn){if(listeners.get(type)===fn)listeners.delete(type);}};
 let values={text:'',number:'',select:'auto',checkbox:false,country:''},closes=0,clears=0;
 const editor=PlanEditor.create({document:doc,window:win,agents,read:()=>({...values}),onclose:()=>closes++,onclear:()=>clears++});
 const open=(id='a',initial={})=>editor.open(id,cards.get(id).trigger,()=>{values={text:'old',number:'1',select:'auto',checkbox:false,country:'',...initial};});
 return{editor,open,nodes,cards,agents,doc,win,listeners,set:(key,value)=>values[key]=value,values:()=>({...values}),closes:()=>closes,clears:()=>clears};
}
test('production geometry anchors, flips and clamps near every viewport edge',()=>{
 assert.deepEqual(PlanEditor.position({left:100,top:80,bottom:380,width:450},400,{width:1366,height:900}),{left:100,top:80,width:450});
 assert.deepEqual(PlanEditor.position({left:1200,top:790,bottom:850,width:450},600,{width:1366,height:900}),{left:900,top:250,width:450});
 assert.deepEqual(PlanEditor.position({left:-200,top:-100,bottom:100,width:800},1000,{width:1366,height:900}),{left:16,top:16,width:620});
 assert.deepEqual(PlanEditor.position(null,800,{width:390,height:844}),{left:0,top:0,width:390});
});
test('every persisted value is dirty, reversion is clean, unrelated focus and search are ignored',()=>{
 const f=fixture();f.open();const original=f.values();
 for(const [key,value] of Object.entries({text:'new',number:'2',select:'monthly',checkbox:true,country:'DE'})){f.set(key,value);assert.equal(f.editor.dirty(),true,key);f.set(key,original[key]);assert.equal(f.editor.dirty(),false,key);}
 f.nodes.get('plan-clear').focus();f.doc.search='Germany';f.doc.foldOpen=true;assert.equal(f.editor.dirty(),false);f.editor.requestClose();assert.equal(f.nodes.get('plan-dialog').open,false);
});
for(const path of ['plan-cancel','plan-dismiss','escape'])test(`${path} shares inline protection, continue preserves draft and discard closes`,()=>{
 const f=fixture();f.open();f.set('text','draft');f.cards.get('a').trigger.focus();
 if(path==='escape')assert.equal(f.nodes.get('plan-dialog').emit('cancel').prevented,true);else f.nodes.get(path).emit('click');
 assert.equal(f.editor.snapshot().confirmation,'close');assert.equal(f.nodes.get('plan-dialog').open,true);assert.equal(f.doc.activeElement,f.nodes.get('plan-continue'));
 f.nodes.get('plan-continue').emit('click');assert.equal(f.values().text,'draft');assert.equal(f.editor.snapshot().confirmation,null);
 f.editor.requestClose();f.nodes.get('plan-discard').emit('click');assert.equal(f.nodes.get('plan-dialog').open,false);assert.equal(f.closes(),1);
});
test('escape from confirmation returns to editing without losing draft',()=>{
 const f=fixture();f.open();f.set('number','9');f.nodes.get('plan-clear').focus();f.editor.requestClose();f.nodes.get('plan-dialog').emit('cancel');assert.equal(f.editor.snapshot().confirmation,null);assert.equal(f.editor.snapshot().open,true);assert.equal(f.doc.activeElement,f.nodes.get('plan-clear'));assert.equal(f.values().number,'9');
});
test('clear requires explicit inline confirmation and cancellation never calls the mutation',()=>{
 const f=fixture();f.open();f.nodes.get('plan-clear').emit('click');assert.equal(f.editor.snapshot().confirmation,'clear');assert.match(f.nodes.get('plan-confirm-note').textContent,/永久流量累计不会删除/);assert.equal(f.clears(),0);
 f.nodes.get('plan-continue').emit('click');assert.equal(f.clears(),0);f.nodes.get('plan-clear').emit('click');f.nodes.get('plan-discard').emit('click');assert.equal(f.clears(),1);assert.equal(f.editor.snapshot().open,true);
});
test('dirty device switch waits for consent and initializes the next device only once',()=>{
 const f=fixture();f.open();f.set('text','draft-a');f.open('b',{text:'stored-b'});assert.equal(f.editor.snapshot().id,'a');assert.equal(f.values().text,'draft-a');assert.equal(f.editor.snapshot().confirmation,'switch');
 f.nodes.get('plan-discard').emit('click');assert.equal(f.editor.snapshot().id,'b');assert.equal(f.values().text,'stored-b');assert.equal(f.editor.dirty(),false);assert.equal(f.closes(),1);
});
test('pending submission cannot repeat or clear; close warning is honest; old finish cannot unlock new pending',()=>{
 const f=fixture();f.open();const old=f.editor.begin();assert.equal(f.editor.begin(),null);assert.equal(f.nodes.get('plan-save').disabled,true);assert.equal(f.nodes.get('plan-clear').disabled,true);
 f.nodes.get('plan-clear').emit('click');assert.equal(f.editor.snapshot().confirmation,null);f.editor.requestClose();assert.match(f.nodes.get('plan-confirm-note').textContent,/关闭不代表未写入/);
 f.nodes.get('plan-discard').emit('click');f.open('b');const current=f.editor.begin();f.editor.finish(old);assert.equal(f.editor.snapshot().busy,true);assert.equal(f.nodes.get('plan-save').disabled,true);f.editor.finish(current);assert.equal(f.editor.snapshot().busy,false);
});
test('SSE identity replacement and resize change positioning only; closing restores current trigger and original scroll',()=>{
 const f=fixture();f.open();f.set('country','JP');f.nodes.get('plan-clear').focus();const old=f.cards.get('a');old.trigger.isConnected=false;const replacement={rect:{left:800,top:200,bottom:650,width:420},trigger:{focus(){f.doc.activeElement=this;}},getBoundingClientRect(){return this.rect;}};f.cards.set('a',replacement);
 f.editor.sync();f.listeners.get('resize')();assert.equal(f.nodes.get('plan-dialog').style.left,'800px');assert.equal(f.values().country,'JP');assert.equal(f.doc.activeElement,f.nodes.get('plan-clear'));assert.equal(f.editor.snapshot().open,true);
 f.editor.close();assert.equal(f.doc.activeElement,replacement.trigger);assert.deepEqual(f.win.scrollCalls,[[2,340]]);
});
test('agent removal closes without restoring another agent focus; shutdown removes lifecycle listeners',()=>{
 const f=fixture();f.open();f.agents.delete('a');f.editor.sync();assert.equal(f.editor.snapshot().open,false);assert.notEqual(f.doc.activeElement,f.cards.get('b').trigger);
 f.open('b');f.editor.shutdown();assert.equal(f.listeners.has('resize'),false);assert.equal(f.nodes.get('plan-dialog').handlers.get('cancel').size,0);assert.equal(f.nodes.get('plan-dialog').handlers.get('close').size,0);f.open('b');assert.equal(f.editor.snapshot().open,false);assert.equal(f.editor.canSubmit(),false);
});
