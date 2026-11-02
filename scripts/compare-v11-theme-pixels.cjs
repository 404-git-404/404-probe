// Exact RGBA comparison: no tolerance, no anti-alias exclusion.
const fs=require('node:fs'),path=require('node:path');
const {PNG}=require(process.env.M02_PNGJS||'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/pngjs');
const dir=path.resolve(process.argv[2]||'../release-records/v1.1.0/v11-CX-深色-evidence/pixel'),results=[];
for(const file of fs.readdirSync(dir).filter(n=>n.endsWith('-baseline.png'))){
 const actual=file.replace('-baseline.png','-actual.png'),a=PNG.sync.read(fs.readFileSync(path.join(dir,file))),b=PNG.sync.read(fs.readFileSync(path.join(dir,actual)));
 if(a.width!==b.width||a.height!==b.height)throw Error('dimensions differ: '+file);
 const heat=new PNG({width:a.width,height:a.height});let pixels=0,maxChannelDifference=0,x0=a.width,y0=a.height,x1=0,y1=0;
 for(let i=0;i<a.data.length;i+=4){let different=false;for(let k=0;k<4;k++){const d=Math.abs(a.data[i+k]-b.data[i+k]);maxChannelDifference=Math.max(maxChannelDifference,d);different||=d!==0;}
  if(different){pixels++;const x=i/4%a.width,y=Math.floor(i/4/a.width);x0=Math.min(x0,x);x1=Math.max(x1,x);y0=Math.min(y0,y);y1=Math.max(y1,y);}
  heat.data.set(different?[255,0,0,255]:[b.data[i]/3,b.data[i+1]/3,b.data[i+2]/3,255],i);
 }
 const name=file.replace('-baseline.png',''),heatmap=pixels?name+'-heat.png':null;
 if(heatmap)fs.writeFileSync(path.join(dir,heatmap),PNG.sync.write(heat));
 const result={name,pixels,maxChannelDifference,bounds:pixels?[x0,y0,x1,y1]:null,heatmap};results.push(result);console.log(`${name}: ${pixels} (max channel ${maxChannelDifference})`);
}
fs.writeFileSync(path.join(dir,'pixel-results.json'),JSON.stringify(results,null,2)+'\n');
console.log(`${results.length} pairs; ${results.filter(r=>!r.pixels).length} exact; ${results.reduce((n,r)=>n+r.pixels,0)} differing pixels`);
