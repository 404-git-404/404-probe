// Contrast of the card colours (design 4.5): text 4.5:1, meaningful graphics 3:1 or else a shape / outline.
// Reads the tokens of web/static/card-v7.css; run: node scripts/check-card-contrast.cjs
const fs=require('node:fs'),path=require('node:path');
const css=fs.readFileSync(path.join(__dirname,'..','web','static','card-v7.css'),'utf8');
const tokens={};for(const m of css.matchAll(/--c7-([a-z0-9-]+):\s*(#[0-9A-Fa-f]{6})/g))tokens[m[1]]=m[2];
const hex=h=>[1,3,5].map(i=>parseInt(h.slice(i,i+2),16)/255);
const lin=c=>c<=.03928?c/12.92:((c+.055)/1.055)**2.4;
const lum=h=>{const [r,g,b]=hex(h).map(lin);return .2126*r+.7152*g+.0722*b;};
const ratio=(a,b)=>{const [x,y]=[lum(a),lum(b)].sort((p,q)=>q-p);return (x+.05)/(y+.05);};
const t=n=>{if(!tokens[n])throw Error('missing token --c7-'+n);return tokens[n];};
const results=[];
const check=(name,fg,bg,min)=>{const r=ratio(t(fg),t(bg));results.push({name,r,min,ok:r>=min});};
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
check('yellow bar on yellow block','mark-yellow','block-yellow',4.5);check('red dot on red block','mark-red','block-red',4.5);
// segmented control, renew button, offline label
check('selected segment text','seg-on-text','seg-on',4.5);
check('renew (soon) text','renew-warn-text','renew-warn-bg',4.5);check('renew (overdue) text','renew-crit-text','renew-crit-bg',4.5);
check('offline label','offline','offline-bg',4.5);
let failed=0;
for(const r of results){if(!r.ok)failed++;console.log(`${r.ok?'ok  ':'FAIL'} ${r.r.toFixed(2).padStart(5)} (min ${r.min}) ${r.name}`);}
// Green and yellow are told apart by shape, not by colour: say so instead of pretending the colours differ.
console.log(`note: green vs yellow fill contrast ${ratio(t('block-green'),t('block-yellow')).toFixed(2)}:1 - the yellow block carries a bar, the red one a white dot`);
if(failed){console.error(`${failed} contrast checks failed`);process.exit(1);}
console.log(`${results.length} contrast checks passed`);
