const container=document.querySelector('#agents');
const empty=document.querySelector('#empty');
const stream=document.querySelector('#stream');
const agents=new Map();

const bytes=(value,rate=false)=>{let n=Number(value||0),units=['B','KiB','MiB','GiB','TiB','PiB'],i=0;while(n>=1024&&i<units.length-1){n/=1024;i++}return `${n.toFixed(i?1:0)} ${units[i]}${rate?'/s':''}`};
const duration=value=>{let s=Number(value||0),d=Math.floor(s/86400),h=Math.floor(s%86400/3600),m=Math.floor(s%3600/60);return d?`${d}天 ${h}小时`:`${h}小时 ${m}分`};
const seen=value=>value?new Date(value).toLocaleString():'从未上报';
const pct=value=>`${Number(value||0).toFixed(1)}%`;

function metric(label,value){const box=document.createElement('div');box.className='metric';const l=document.createElement('label');l.textContent=label;const strong=document.createElement('strong');strong.textContent=value;box.append(l,strong);return box}
function render(){container.replaceChildren();const list=[...agents.values()].sort((a,b)=>(a.name||'').localeCompare(b.name||''));empty.classList.toggle('hidden',list.length>0);for(const a of list){
  const card=document.createElement('article');card.className='card';
  const head=document.createElement('div');head.className='card-head';const title=document.createElement('div');const h=document.createElement('h2');h.textContent=a.hostname||a.name;const name=document.createElement('div');name.className='name';name.textContent=a.name||a.agent_id;title.append(h,name);const status=document.createElement('span');status.className=`status ${a.online?'online':'offline'}`;status.textContent=a.online?'● ONLINE':'● OFFLINE';head.append(title,status);card.append(head);
  const metrics=document.createElement('div');metrics.className='metrics';metrics.append(metric('CPU',pct(a.cpu_percent)),metric('RAM',pct(a.ram_percent)),metric('Swap',pct(a.swap_percent)),metric('Disk',pct(a.disk_percent)),metric('Load',`${Number(a.load1||0).toFixed(2)} / ${Number(a.load5||0).toFixed(2)} / ${Number(a.load15||0).toFixed(2)}`));card.append(metrics);
  const network=document.createElement('div');network.className='network';for(const [arrow,rate,total] of [['↓',a.rx_rate,a.rx_total],['↑',a.tx_rate,a.tx_total]]){const box=document.createElement('div');const strong=document.createElement('strong');strong.textContent=`${arrow} ${bytes(rate,true)}`;const small=document.createElement('small');small.textContent=`累计 ${bytes(total)}`;box.append(strong,document.createElement('br'),small);network.append(box)}card.append(network);
  const meta=document.createElement('div');meta.className='meta';for(const text of [`Uptime: ${duration(a.uptime)}`,`${a.os||'未知 OS'} / ${a.arch||'未知架构'}`,`Last Seen: ${seen(a.last_seen)}`]){const line=document.createElement('span');line.textContent=text;meta.append(line)}card.append(meta);
  const link=document.createElement('a');link.className='details';link.href=`/history.html?id=${encodeURIComponent(a.agent_id)}`;link.textContent='查看 24 小时历史 →';card.append(link);container.append(card)
}}
async function refresh(){try{const response=await fetch('/api/v1/agents',{cache:'no-store'});if(!response.ok)throw new Error(response.statusText);for(const a of await response.json())agents.set(a.agent_id,a);render()}catch(error){stream.textContent='读取失败'}}
refresh();setInterval(refresh,15000);
const events=new EventSource('/api/v1/events');events.onopen=()=>{stream.textContent='实时连接';stream.classList.add('live')};events.onerror=()=>{stream.textContent='正在重连';stream.classList.remove('live')};events.addEventListener('agent',event=>{const a=JSON.parse(event.data);agents.set(a.agent_id,a);render()});
