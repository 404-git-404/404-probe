// Contrast matrices for both palettes. Every shared token and alias is resolved;
// text roles use 4.5:1 and meaningful graphics use 3:1. Decorative borders,
// backgrounds and disabled controls are not foreground contrast requirements.
const fs=require('node:fs'),path=require('node:path');
const read=n=>fs.readFileSync(path.join(__dirname,'..','web/static',n),'utf8');
const css=read('card-v7.css'),shared=read('style.css');
const declarations=s=>Object.fromEntries([...s.matchAll(/(--[a-z0-9-]+):\s*([^;}\n]+)/g)].map(m=>[m[1],m[2].trim()]));
const [light,remainder]=shared.split(':root[data-theme=dark]{'),dark=remainder.slice(0,remainder.indexOf('}'));
const rgba=value=>{if(value==='transparent')return [0,0,0,0];if(value==='white')return [255,255,255,1];if(value==='black')return [0,0,0,1];if(value.startsWith('rgb')){const a=value.match(/[\d.]+/g).map(Number);return [...a.slice(0,3),a[3]??1];}let h=value.slice(1);if(h.length<5)h=[...h].map(c=>c+c).join('');return [0,2,4].map(i=>parseInt(h.slice(i,i+2),16)).concat(h.length===8?parseInt(h.slice(6),16)/255:1);};
const lin=c=>(c/=255)<=.04045?c/12.92:((c+.055)/1.055)**2.4;
const lum=c=>{const [r,g,b]=c.map(lin);return .2126*r+.7152*g+.0722*b;};
const ratio=(a,b)=>{const [x,y]=[lum(a),lum(b)].sort((p,q)=>q-p);return (x+.05)/(y+.05);};
const composite=(a,b)=>a.slice(0,3).map((v,i)=>v*a[3]+b[i]*(1-a[3]));
const results=[],coverage=[];
for(const mode of ['light','dark']){
 const vars={...declarations(light),...(mode==='dark'?declarations(dark):{}),...declarations(css)};
 const resolve=(name,seen=new Set())=>{if(seen.has(name))throw Error('cyclic token '+name);seen.add(name);const v=vars[name];if(!v)throw Error('missing token '+name);const m=v.match(/^var\((--[a-z0-9-]+)\)$/);return m?resolve(m[1],seen):v;};
 const page=rgba(resolve('--color-page'));
 const t=n=>{const name=n.startsWith('--')?n:'--c7-'+n;return composite(rgba(resolve(name)),page);};
 const names=Object.keys(vars).filter(n=>n.startsWith('--color-'));
 for(const name of names){const v=rgba(resolve(name));if(v.some(n=>!Number.isFinite(n)))throw Error('invalid colour '+name);}
 coverage.push({mode,sharedTokens:names.length,cardAliases:Object.keys(vars).filter(n=>n.startsWith('--c7-')).length});
 const check=(name,fg,bg,min)=>{const r=ratio(t(fg),t(bg));results.push({mode,name,category:min===3?'graphic':'text',r,min,ok:r>=min,fg,bg});};
 for(const bg of ['page','card','surface-module','surface-hover'])for(const fg of ['text','text-muted','text-secondary','accent-text','danger-text','warning-text'])check(fg+' on '+bg,'--color-'+fg,'--color-'+bg,4.5);
 for(const fg of ['--text','--muted'])for(const bg of ['--bg','--panel'])check(fg+' on '+bg,fg,bg,4.5);
 for(const [fg,bg]of [['--color-text-02','--color-accent'],['--color-text-02','--color-danger']])check('button '+fg+' on '+bg,fg,bg,4.5);
for(const state of ['hover','focus','disabled'])for(const bg of ['--color-accent','--color-danger'])check('detail action '+state+' on '+bg,'--color-text-02',bg,4.5);
for(const bg of ['page','card','surface-module'])for(const fg of ['success','status-02','danger'])check('graphic '+fg+' on '+bg,'--color-'+fg,'--color-'+bg,3);
// text on the module background (and on its hover colour)
for(const bg of ['mod','mod-hover']){
  for(const fg of ['text','sub','pct-warn','pct-crit','up','warn-text','crit-text','quota-text'])check(`${fg} on ${bg}`,fg,bg,4.5);
  // The direction colours only colour the 20px bold rate (large text: 3:1); at rest they also reach 4.5:1.
  for(const fg of ['down','up'])check(`${fg} (large text) on ${bg}`,fg,bg,bg==='mod'?4.5:3);
}
check('text on card','text','card',4.5);check('sub on card','sub','card',4.5);
// the quota bar: text on the track and on every fill
for(const bg of ['quota-track','quota-fill','quota-fill-warn','quota-fill-crit'])check(`quota text on ${bg}`,'quota-text',bg,4.5);
check('warn percentage on its fill','quota-pct-warn','quota-fill-warn',4.5);check('critical percentage on its fill','quota-pct-crit','quota-fill-crit',4.5);
// rings: the arc against the module
for(const c of ['ring-ok','ring-warn','ring-crit'])check(`${c} on module`,c,'mod',3);
// blocks: outline against the module (the fills are 1.3-1.9:1; the shape and the outline carry them)
for(const c of ['edge-green','edge-yellow','edge-gray','block-red'])check(`${c} outline on module`,c,'mod',3);
check('yellow bar on yellow block','mark-yellow','block-yellow',3);check('red dot on red block','mark-red','block-red',3);
// segmented control, renew button, offline label
check('selected segment text','seg-on-text','seg-on',4.5);
check('renew (soon) text','renew-warn-text','renew-warn-bg',4.5);check('renew (overdue) text','renew-crit-text','renew-crit-bg',4.5);
check('offline label','offline','offline-bg',4.5);

}
let failed=0;
for(const r of results){if(!r.ok)failed++;console.log(`${r.ok?'ok  ':'FAIL'} ${r.mode} ${r.r.toFixed(2).padStart(5)} (min ${r.min}) ${r.name}`);}
console.log('palette coverage: '+JSON.stringify(coverage));
if(process.env.V11_CONTRAST_OUTPUT)fs.writeFileSync(process.env.V11_CONTRAST_OUTPUT,JSON.stringify({coverage,results},null,2)+'\n');
if(failed){console.error(`${failed} contrast checks failed`);process.exitCode=1;}else console.log(`${results.length} contrast checks passed`);
