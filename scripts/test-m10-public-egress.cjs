const test = require('node:test');
const assert = require('node:assert/strict');
const State = require('../web/static/agent-state.js');

test('M10: exact decimal epoch/sequence prevent queued egress regression and cross-Agent inheritance', () => {
  const store=State.create(),a='a',b='b';
  const state=(epoch,sequence,ip)=>({cpu_percent:12,public_egress:{version:1,epoch,sequence,ipv4:{family:'ipv4',status:'available',ip}}});
  let cycle=store.beginRefresh();store.commitList(cycle,[{agent_id:a,state:state('9007199254740993','10','8.8.8.8')},{agent_id:b,state:state('1','1','1.1.1.1')}]);store.endRefresh(cycle);
  store.event({agent_id:a,state:state('9007199254740992','9999999999','9.9.9.9')});
  assert.equal(store.agents.get(a).state.public_egress.ipv4.ip,'8.8.8.8');
  store.event({agent_id:a,state:state('9007199254740993','9','9.9.9.9')});
  assert.equal(store.agents.get(a).state.public_egress.ipv4.ip,'8.8.8.8');
  store.event({agent_id:a,state:state('9007199254740993','11','4.2.2.2')});
  assert.equal(store.agents.get(a).state.public_egress.ipv4.ip,'4.2.2.2');
  assert.equal(store.agents.get(b).state.public_egress.ipv4.ip,'1.1.1.1');
  const token=store.read(a);assert.equal(store.commitDetail(token,{agent_id:b,state:state('9999999999999999','1','9.9.9.9')}),false);
  store.event({agent_id:a,state:{public_egress:{version:1,supported:false,epoch:'9007199254740994',sequence:'1',ipv4:{family:'ipv4',status:'unknown',reason:'not_reported'}}}});
  assert.equal(store.agents.get(a).state.public_egress.ipv4.status,'unknown');
});
