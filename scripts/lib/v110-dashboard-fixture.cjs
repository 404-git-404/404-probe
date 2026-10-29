// Native DOM integration fixture. All traffic stays on the loopback server.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {load,v5Card}=require('./r07-harness.cjs'),Name=require('../../web/static/organization-name.js');
const root=path.resolve(__dirname,'../../web/static'),ID=i=>(i+1).toString(16).padStart(32,'0');
const clone=v=>structuredClone(v),DAY=86400000;
const rat=(n,d=1n)=>({n:BigInt(n),d:BigInt(d)}),add=(a,b)=>rat(a.n*b.d+b.n*a.d,a.d*b.d),div=(a,b)=>rat(a.n*b.d,a.d*b.n);
function decimal(s){const [a,b='']=s.split('.');return rat(BigInt(a+b),10n**BigInt(b.length));}
function fixed(a,digits){const base=10n**BigInt(digits),q=(a.n*base*2n/a.d+1n)/2n,s=q.toString().padStart(digits+1,'0');return digits?s.slice(0,-digits)+'.'+s.slice(-digits):s;}
function makeRows(n){const now=Date.now(),names=['DediRock-LAX-1','Aike-HKG-2','Vultr-NRT-1','DediRock-LAX-备用','Bandwagon-LAX-1','Misaka-SIN-Test-Long-Name-Example','Netcup-NUE-1'],countries=['US','HK','JP','US','US','SG','DE'];
 return Array.from({length:n},(_,i)=>{const currency=i<10?'USD':i<15?'CNY':'EUR',price=i<10?'5.23':i<15?'42':'2.10',name=names[i]||'Synthetic-'+String(i+1).padStart(3,'0'),cpu=[1,3.8,2.4][i]??1.1,ram=[14,41,22][i]??35,disk=[18,33,51][i]??33;
  const a={agent_id:ID(i),name,hostname:'synthetic-'+i,created_at:now-DAY*60,online:i!==3&&i!==5,disabled_at:i===5?now-1000:null,revoked:false,last_seen:now-(i===3?600000:1000),version:i<2?'v1.0.9':'v1.0.10',upgrade_capable:true,upgrade_v2:true,country_code:countries[i]||'US',country_source:'automatic',group_id:n>3?(i<3?ID(100):i<5?ID(101):null):null,tag_ids:[],organization_revision:1,
   resources:{known:true,stale:i===3,cpu_percent:i===12?92:cpu,ram_percent:ram,disk_percent:disk,collected_at:now,max_age_ms:600000},network:{known:true,active:i===1||i===2,monitored:true,checked_at:now,expires_at:now+600000},renewal:{type:i===1||i===2?'due_soon':'upcoming',days:i===1?2:i===2?7:40+i,date:i===1?'2026-10-31':i===2?'2026-11-05':'2026-12-10',timezone:'UTC'},traffic:{known:true,stale:false,quota_state:'current',cycle_current:true},accumulated_traffic:{rx_total:79.3e9,tx_total:79.1e9,started_at:now-27*DAY},masked_ips:['203.0.*.*','2001:db8:…'],search_fields:[name,'synthetic-'+i,'203.0.*.*','2001:db8:…'],
   state:{hostname:'synthetic-'+i,os:i===5?'Alpine 3.24':'Debian 13.6',arch:'amd64',uptime:54*86400,cpu_percent:cpu,cpu_cores:2,cpu_steal_percent:0,ram_percent:ram,ram_used:206*2**20,ram_total:1.44*2**30,swap_used:0,swap_total:2**30,disk_percent:disk,disk_used:4.49*2**30,disk_total:24.5*2**30,load1:.05,load5:.03,load15:.01,disk_busy_percent:0,rx_rate:1750,tx_rate:1140,collected_at:now,public_egress:{}},
   google_status:{supported:true,pending:false,checked_at:now,next_due_at:now+DAY,result:{youtube:{status:'not_cn',region:countries[i]||'US'}}},outbounds:{configured:true,available:true,status:'connected',stale:false,order_source:'config',selectors:[{name:'YouTube',current:'直连',choices:['直连','备用出口']}]},
   plan:{agent_id:ID(i),currency,renewal_price:i<17?price:'',renewal_price_period:'monthly',renewal_date:i===1?'2026-10-31':'2026-12-10',expiry_date:'2027-12-10',purchase_date:'2026-01-31',timezone:'UTC',bandwidth_value:'1',bandwidth_unit:'Gbps',traffic_mode:'sum',quota_bytes:5e12,quota_value:'5',quota_unit:'TB',cycle_kind:'monthly',cycle_count:1,cycle_anchor:'2026-10-01',cycle_start:now-27*DAY,cycle_end:now+4*DAY,usage_bytes:36.6e9,usage_status:'partial',cycle_rx_bytes:20e9,cycle_tx_bytes:16.6e9},management:{country_code_lookup:true},security:{supported:false},public_egress_check:{enabled:false,supported:true}};
  if(n>3){if(i<7)a.tag_ids.push(ID(200));if([0,1,7,8,9,10].includes(i))a.tag_ids.push(ID(201));if([1,2,11].includes(i))a.tag_ids.push(ID(202));if([5,12,13,14].includes(i))a.tag_ids.push(ID(203));if([6,15].includes(i))a.tag_ids.push(ID(204));}return a;
 });
}
async function start({n=28}={}){
 const state={items:makeRows(n),revision:1,groups:n>3?[{id:ID(100),name:'常用',sort_order:0},{id:ID(101),name:'备用',sort_order:1}]:[],tags:n>3?['GIA','便宜','视频专用','测试','大硬盘'].map((name,i)=>({id:ID(200+i),name})):[],exchange_rates:[{currency:'USD',units_per_usd:'1'},{currency:'CNY',units_per_usd:'7.1'},{currency:'EUR',units_per_usd:'.93'.replace(/^\./,'0.')},{currency:'HKD',units_per_usd:'7.78'},{currency:'JPY',units_per_usd:'152'}]},requests=[],streams=new Set();let counter=500,conflict=false;
 const expense=ids=>{const selected=ids===null?state.items:state.items.filter(a=>ids.includes(a.agent_id)),out={revision:state.revision,scope_count:selected.length,included_count:0,once_count:0,missing_price_count:0,excluded_count:0,currencies:[],devices:[],excluded:[],missing_price_devices:[],missing_rates:[]};let total=rat(0);const currencies=new Map(),missing=new Map();
  for(const a of selected){const p=a.plan||{},purchase=!p.renewal_price&&p.renewal_price!=='0',price=purchase?p.purchase_price:p.renewal_price,period=purchase?p.purchase_price_period:p.renewal_price_period,device={id:a.agent_id,name:a.name,currency:p.currency,price:price||'',period,estimated_from_purchase:purchase,monthly_usd:null,monthly_original:'0'};let reason=period==='once'?'once':price===undefined||price===''?'missing_price':!p.currency?'missing_currency':!period?'missing_period':!['monthly','quarterly','semiannual','yearly'].includes(period)?'invalid_period':'';
   if(!reason){const original=div(decimal(price),rat({monthly:1,quarterly:3,semiannual:6,yearly:12}[period])),rate=state.exchange_rates.find(r=>r.currency===p.currency);device.monthly_original=fixed(original,12);const c=currencies.get(p.currency)||{currency:p.currency,device_count:0,original:rat(0),usd:rat(0),rate_missing:!rate};c.device_count++;c.original=add(c.original,original);if(rate){const usd=div(original,decimal(rate.units_per_usd));device.monthly_usd=fixed(usd,12);total=add(total,usd);c.usd=add(c.usd,usd);out.included_count++;}else{reason='missing_rate';missing.set(p.currency,(missing.get(p.currency)||0)+1);}currencies.set(p.currency,c);}
   if(reason){device.exclusion=reason;out.excluded.push(device);out.excluded_count++;if(reason==='once')out.once_count++;if(reason==='missing_price'){out.missing_price_count++;out.missing_price_devices.push(device);}}out.devices.push(device);
  }
  out.monthly_usd=fixed(total,2);out.yearly_usd=fixed(rat(total.n*12n,total.d),2);out.currencies=[...currencies.values()].map(c=>({currency:c.currency,device_count:c.device_count,rate_missing:c.rate_missing,monthly_original:fixed(c.original,12),monthly_usd:c.rate_missing?null:fixed(c.usd,12)}));out.missing_rates=[...missing].map(([currency,device_count])=>({currency,device_count}));return out;
 };
 const catalog=()=>({...clone(state),partial:false,total:state.items.length,server_now:Date.now(),expenses:expense(null)});
 const config=()=>{const value=load('v4-running');value.slots.forEach((s,i)=>{s.kind='custom';s.region=['上海','广州','北京'][i];s.isp=['电信','联通','移动'][i];});return value;};
 const server=http.createServer(async(req,res)=>{const url=new URL(req.url,'http://localhost'),method=req.method;let raw='';for await(const b of req)raw+=b;let body=null;try{body=raw?JSON.parse(raw):null;}catch{}requests.push({method,path:url.pathname,query:url.search,body,headers:req.headers});const json=(value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(value));},fail=(code='invalid_request',status=400)=>json({error:{code}},status);
  if(url.pathname==='/api/v1/web/events'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write('retry: 3000\n\n');streams.add(res);req.on('close',()=>streams.delete(res));return;}
  if(url.pathname==='/api/v1/web/session')return json({csrf_token:'synthetic-csrf'});if(url.pathname==='/api/v1/web/version')return json({version:'v1.0.10',upgrade_eligible:true});
  if(url.pathname==='/api/v1/web/organization/catalog')return json(catalog());
  if(url.pathname==='/api/v1/web/dashboard'){const c=catalog();if(url.searchParams.has('ids')){const ids=url.searchParams.get('ids').split(',');if(ids.some(id=>!state.items.some(a=>a.agent_id===id)))return fail('not_found',404);c.items=c.items.filter(a=>ids.includes(a.agent_id));c.partial=true;}return json(c);}
  if(url.pathname==='/api/v1/web/expenses/estimate'){if(body.scope==='filtered'&&body.agent_ids.some(id=>!state.items.some(a=>a.agent_id===id)))return fail('not_found',404);return json(expense(body.scope==='all'?null:body.agent_ids));}
  const agentMatch=/^\/api\/v1\/web\/agents\/([a-f0-9]{32})(.*)$/.exec(url.pathname),a=agentMatch&&state.items.find(a=>a.agent_id===agentMatch[1]),suffix=agentMatch?.[2];
  if(agentMatch&&!a)return fail('not_found',404);
  if(a&&method==='GET'){
   if(!suffix)return json({...clone(a),traffic:clone(a.accumulated_traffic)});
   if(suffix==='/organization')return json({revision:state.revision,item:{agent_id:a.agent_id,group_id:a.group_id,tag_ids:a.tag_ids}});
   if(suffix==='/upgrade-targets')return json({targets:[{version:'v1.0.10',supported:a.version!=='v1.0.10',reason_code:a.version==='v1.0.10'?'already_latest':''}]});
   if(suffix==='/upgrade'){res.statusCode=204;return res.end();}
   if(suffix.endsWith('/v4/config'))return json(config());if(suffix.endsWith('/v5/card'))return json(v5Card(config(),url.searchParams.get('family')||'ipv4',Date.now()));
   if(suffix==='/plan')return json(a.plan);
  }
  if(method!=='GET'&&!url.pathname.includes('/plan')){
   const expected=method==='DELETE'?Number((req.headers['if-match']||'').replaceAll('"','')):body?.expected_revision;
   if(conflict){conflict=false;state.revision++;return fail('revision_conflict',409);}if(expected!==state.revision)return fail('revision_conflict',409);
   const resource=/^\/api\/v1\/web\/(groups|tags)(?:\/(.*))?$/.exec(url.pathname);
   if(resource){const [,kind,id]=resource,list=state[kind];if(id==='order'){if(body.ids.length!==state.groups.length||new Set(body.ids).size!==body.ids.length)return fail();state.groups=body.ids.map((id,sort_order)=>({...list.find(g=>g.id===id),sort_order}));}
    else if(method==='POST'){if(list.some(t=>Name.fold(t.name)===Name.fold(body.name)))return fail('name_conflict',409);list.push({id:ID(counter++),name:body.name,sort_order:list.length});}
    else {const item=list.find(t=>t.id===id);if(!item)return fail('not_found',404);if(method==='PATCH')item.name=body.name;if(method==='DELETE'){state[kind]=list.filter(t=>t.id!==id);for(const a of state.items){if(kind==='groups'&&a.group_id===id)a.group_id=null;if(kind==='tags')a.tag_ids=a.tag_ids.filter(t=>t!==id);}}}
   }else if(a&&suffix==='/organization'){
    a.group_id=body.group_id;a.tag_ids=[...body.tag_ids];if(body.new_group){const g={id:ID(counter++),name:body.new_group,sort_order:state.groups.length};state.groups.push(g);a.group_id=g.id;}for(const name of body.new_tags||[]){if(state.tags.some(t=>Name.fold(t.name)===Name.fold(name)))return fail('name_conflict',409);const t={id:ID(counter++),name};state.tags.push(t);a.tag_ids.push(t.id);}
   }else if(url.pathname==='/api/v1/web/exchange-rates'){for(const value of body.rates){state.exchange_rates=state.exchange_rates.filter(r=>r.currency!==value.currency);state.exchange_rates.push(value);}}
   else if(url.pathname.startsWith('/api/v1/web/exchange-rates/'))state.exchange_rates=state.exchange_rates.filter(r=>r.currency!==url.pathname.split('/').at(-1));
   else return fail('not_found',404);
   state.revision++;return json({revision:state.revision,changed_agent_ids:state.items.map(a=>a.agent_id)});
  }
  if(url.pathname.startsWith('/api/'))return fail('not_found',404);
  const file=path.resolve(root,'.'+(url.pathname==='/'?'/index.html':url.pathname));if(!file.startsWith(root+path.sep)||!fs.existsSync(file)){res.statusCode=404;return res.end();}res.writeHead(200,{'Content-Type':file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.svg')?'image/svg+xml':'text/html'});res.end(fs.readFileSync(file));
 });await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 return {base:'http://127.0.0.1:'+server.address().port,state,requests,catalog,expense,conflict(){conflict=true;},disconnect(){for(const s of streams)s.end();streams.clear();},publish(event,data){for(const s of streams)s.write('event: '+event+'\ndata: '+JSON.stringify(data)+'\n\n');},close(){server.closeAllConnections();server.close();}};
}
module.exports={start,ID,makeRows};
