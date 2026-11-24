(function(root){
  'use strict';
  const HOUR=3600000, DAY=24*HOUR, BEIJING=8*HOUR;
  const clamp=(v,min,max)=>Math.max(min,Math.min(max,v));
  const beijing=(at,span=DAY)=>{
    const iso=new Date(at+BEIJING).toISOString();
    return span<=DAY?iso.slice(11,16):span<=7*DAY?iso.slice(5,16).replace('T',' '):iso.slice(5,10);
  };
  const resolutionLabel=ms=>({60000:'1 分钟',300000:'5 分钟',3600000:'1 小时'})[ms]||'—';
  const colors=['#57a6fb','#25a989','#f09848','#a986f2','#e173a5','#64b23f','#9990ef','#4cbbc4'];
  const coordinates=(points,getValue,x,y,smooth)=>{
    let path='', previous=null;
    for(const point of points){
      const raw=getValue(point);
      if(raw==null){previous=null;continue;}
      const value=previous!=null&&smooth?previous*.8+raw*.2:raw;
      const px=x(point.start).toFixed(1),py=y(value).toFixed(1);
      path+=(previous==null?'M':'L')+px+' '+py;
      if(previous==null)path+='l.1 0';
      previous=value;
    }
    return path;
  };
  function spark(points){
    if(!points?.length)return '';
    const values=points.map(p=>p.p50_ms).filter(v=>v!=null);
    if(!values.length)return '';
    const lo=Math.min(...values),hi=Math.max(...values),start=points[0].start;
    const span=points.at(-1).start-start||1;
    const path=coordinates(points,p=>p.p50_ms,t=>1+118*(t-start)/span,v=>26-22*(v-lo)/(hi-lo||1));
    return '<svg class="net-spark" aria-hidden="true" viewBox="0 0 120 28" preserveAspectRatio="none"><path d="'+path+'"/></svg>';
  }

  function attach(boxes,{start,end,lines,metric,lossKey,smooth,threshold,onZoom,tooltip}){
    const span=end-start, visible=lines.filter(line=>!line.hidden), plots=[];
    let drag=null, hover=null;
    function draw(box,index){
      const key=index===0?metric:lossKey, percent=index===1, top=10, left=46, right=12, bottom=25;
      const width=Math.max(160,box.clientWidth),height=index===0?240:120,inner=width-left-right, lower=height-bottom;
      const ticks=percent?2:4, increment=percent||key==='jitter_ms'?10:100;
      let largest=percent?5:key==='jitter_ms'?10:100;
      const available=new Set(), markers=new Map();
      for(const line of visible){
        for(const point of line.points){
          if(point[key]!=null)largest=Math.max(largest,point[key]);
          if(point.samples)available.add(point.start);
        }
        for(const change of line.changes)markers.set(change.at,{label:line.name+' 换过目标',className:'net-change'});
        if(line.semanticsChange!=null)markers.set(line.semanticsChange,{label:line.name+' TCP 口径变更',className:'net-change net-semantics'});

      }
      const high=Math.ceil(largest/increment)*increment;
      const x=at=>left+(clamp(at,start,end)-start)*inner/span;
      const time=px=>Math.round(start+clamp((px-left)/inner,0,1)*span);
      const y=value=>lower-(lower-top)*value/high;
      let svg='';
      for(let day=Math.floor((start+BEIJING)/DAY);day<=Math.floor((end+BEIJING)/DAY);day++){
        const evening=day*DAY-BEIJING+20*HOUR;
        if(evening<end&&evening+3*HOUR>start)svg+='<rect class="net-evening" x="'+x(evening)+'" y="'+top+'" width="'+(x(evening+3*HOUR)-x(evening))+'" height="'+(lower-top)+'"/>';
      }
      const points=visible[0]?.points||[];
      let gap=null;
      for(const p of points){
        if(!available.has(p.start)){gap=gap??p.start;continue;}
        if(gap!=null){svg+=gapBand(gap,p.start);gap=null;}
      }
      if(gap!=null)svg+=gapBand(gap,end);
      function gapBand(a,b){return '<rect class="net-gap" x="'+x(a)+'" y="'+top+'" width="'+Math.max(1,x(b)-x(a))+'" height="'+(lower-top)+'"/>';}
      for(let i=0;i<=ticks;i++){
        const value=high*i/ticks, py=y(value);
        svg+='<line class="net-grid" x1="'+left+'" x2="'+(width-right)+'" y1="'+py+'" y2="'+py+'"/>';
        svg+='<text x="'+(left-6)+'" y="'+(py+4)+'" text-anchor="end">'+Math.round(value)+(percent?'%':i===ticks?' ms':'')+'</text>';
      }
      const timeTicks=width<460?3:6;
      for(let i=0;i<=timeTicks;i++){
        const at=start+span*i/timeTicks,anchor=i===0?'start':i===timeTicks?'end':'middle';
        svg+='<text x="'+x(at)+'" y="'+(height-6)+'" text-anchor="'+anchor+'">'+beijing(at,span)+'</text>';
      }
      if(!percent&&key!=='jitter_ms'&&threshold<=high)svg+='<line class="net-threshold" x1="'+left+'" x2="'+(width-right)+'" y1="'+y(threshold)+'" y2="'+y(threshold)+'"/>';
      for(const [at,mark] of markers)svg+='<line class="'+mark.className+'" x1="'+x(at)+'" x2="'+x(at)+'" y1="'+top+'" y2="'+lower+'"/>';
      visible.forEach((line,i)=>{
        for(const alert of line.alerts||[]){
          const a=Math.max(start,alert.start),b=Math.min(end,alert.end??end),row=top+3*i;
          if(b>a)svg+='<line class="net-chart-alert" x1="'+x(a)+'" x2="'+x(b)+'" y1="'+row+'" y2="'+row+'"/>';
        }
        if(index===0){
          const rows=Object.create(null),row=lower-3-3*i;
          for(const p of line.points){
            const tone=p.state;
            if(!['green','yellow','red','gray'].includes(tone))continue;
            (rows[tone]??=[]).push('M'+x(p.start).toFixed(1)+' '+row+'H'+x(p.end).toFixed(1));
          }
          for(const [tone,segments] of Object.entries(rows))svg+='<path class="net-bucket net-bucket-'+tone+'" d="'+segments.join('')+'"/>';
        }
      });
      for(const line of visible){
        const path=coordinates(line.points,p=>p[key],x,y,smooth);
        svg+='<path class="net-series" d="'+path+'" style="stroke:'+line.color+'"/>';
      }
      box.innerHTML='<svg width="'+width+'" height="'+height+'" viewBox="0 0 '+width+' '+height+'" aria-hidden="true">'+svg+
        '<line class="net-cross" y1="'+top+'" y2="'+lower+'" visibility="hidden"/><rect class="net-brush" y="'+top+'" height="'+(lower-top)+'" visibility="hidden"/></svg>'+
        '<div class="net-tooltip" hidden></div>';
      plots[index]={box,x,time,left,right,width,top,lower,points,markers,
        cross:box.querySelector('.net-cross'),brush:box.querySelector('.net-brush'),tip:box.querySelector('.net-tooltip')};
    }
    function redraw(){boxes.forEach(draw);if(hover)showHover(hover.index,hover.at);}
    function showHover(index,at){
      hover={index,at};
      for(const plot of plots){
        const px=plot.x(at);
        plot.cross.setAttribute('x1',px);plot.cross.setAttribute('x2',px);plot.cross.setAttribute('visibility','visible');
        plot.tip.hidden=true;
      }
      const plot=plots[index],points=plot.points;
      const point=points.length?points[clamp(Math.floor((at-points[0].start)/(points[0].end-points[0].start)),0,points.length-1)]:null;
      const marks=[...plot.markers].filter(([timestamp])=>Math.abs(plot.x(timestamp)-plot.x(at))<=5).map(([,mark])=>mark.label);
      plot.tip.innerHTML=tooltip(visible,point?.start??at,marks);
      plot.tip.hidden=false;
      const px=plot.x(at),tipWidth=plot.tip.offsetWidth;
      plot.tip.style.left=clamp(px+14+tipWidth>plot.width?px-tipWidth-12:px+14,4,plot.width-tipWidth-4)+'px';
    }
    function clearHover(){
      hover=null;
      for(const plot of plots){plot.cross.setAttribute('visibility','hidden');plot.tip.hidden=true;}
    }
    const events=new AbortController();
    boxes.forEach((box,index)=>{
      function pointer(e){const plot=plots[index], rect=box.getBoundingClientRect();return {plot,x:e.clientX-rect.left,y:e.clientY-rect.top};}
      function listen(name,fn){box.addEventListener(name,fn,{signal:events.signal});}
      listen('pointermove',e=>{
        const {plot,x,y}=pointer(e);
        if(!drag&&(x<plot.left||x>plot.width-plot.right||y<plot.top||y>plot.lower)){clearHover();return;}
        showHover(index,plot.time(x));
        if(drag?.index===index){
          const left=plot.x(Math.min(plot.time(x),drag.at)),right=plot.x(Math.max(plot.time(x),drag.at));
          plot.brush.setAttribute('x',left);plot.brush.setAttribute('width',right-left);plot.brush.setAttribute('visibility','visible');
        }
      });
      listen('pointerdown',e=>{
        const {plot,x,y}=pointer(e);
        if(e.button!==0||x<plot.left||x>plot.width-plot.right||y<plot.top||y>plot.lower)return;
        drag={index,at:plot.time(x)};box.setPointerCapture(e.pointerId);
      });
      listen('pointerup',e=>{
        if(drag?.index!==index)return;
        const {plot,x}=pointer(e),a=drag.at,b=plot.time(x);
        drag=null;plot.brush.setAttribute('visibility','hidden');
        if(Math.abs(plot.x(a)-plot.x(b))>=8)onZoom([Math.min(a,b),Math.max(a,b)]);
      });
      listen('pointercancel',()=>{drag=null;plots[index].brush.setAttribute('visibility','hidden');clearHover();});
      listen('pointerleave',()=>{if(!drag)clearHover();});
      listen('blur',clearHover);
      listen('keydown',e=>{
        if(e.key==='Escape'){clearHover();return;}
        if(!['ArrowLeft','ArrowRight'].includes(e.key))return;
        e.preventDefault();
        const step=plots[index].points[0]?.end-plots[index].points[0]?.start||span/20;
        showHover(index,clamp((hover?.at??start)+step*(e.key==='ArrowLeft'?-1:1),start,end));
      });
    });
    redraw();
    const observer=new ResizeObserver(redraw);
    boxes.forEach(box=>observer.observe(box));
    return ()=>{observer.disconnect();events.abort();};
  }
  root.NetworkStatusCharts={attach,beijing,resolutionLabel,spark,colors};
})(globalThis);
