/* Exact presentation and wire compatibility for legacy traffic quotas. */
(function(root){
 'use strict';
 const factors={GB:1000000000n,TB:1000000000000n,GiB:1073741824n,TiB:1099511627776n};
 const syntax=/^(0|[1-9][0-9]{0,11})(\.[0-9]{1,3})?$/;
 function quantity(value,unit){const [whole,fraction='']=value.split('.'),scale=10n**BigInt(fraction.length),numerator=BigInt(whole+fraction)*factors[unit];return(numerator+scale/2n)/scale;}
 function decimal(n,places){let digits=n.toString().padStart(places+1,'0');if(!places)return digits;const whole=digits.slice(0,-places),fraction=digits.slice(-places).replace(/0+$/,'');return whole+(fraction?'.'+fraction:'');}
 function display(value,unit){
  const raw=String(value??'');if(!raw)return{value:'',unit:unit==='TB'?'TB':'GB'};
  if(unit!=='GiB'&&unit!=='TiB')return{value:raw,unit:unit||'GB'};
  if(!syntax.test(raw))throw Error('旧流量额度格式无效，无法精确换算');
  const target=unit==='GiB'?'GB':'TB',bytes=quantity(raw,unit);
  if(bytes<=0n||bytes>9223372036854775807n)throw Error('旧流量额度超出支持范围，无法精确换算');
  return{value:decimal(bytes,target==='GB'?9:12),unit:target};
 }
 function validate(value,unit,allowZero=false){
  if(!['GB','TB'].includes(unit)||!syntax.test(value))return '请输入十进制 GB/TB 数量，整数最多 12 位，小数最多 3 位。';
  const bytes=quantity(value,unit);
  if(bytes>9223372036854775807n||(!allowZero&&bytes===0n))return '流量额度必须大于 0，且数量不能超出支持范围。';
  return '';
 }
 function open(plan={}){
  const wire={value:String(plan.quota_value??''),unit:plan.quota_unit||''},shown=display(wire.value,wire.unit);
  return{shown,legacy:wire.unit==='GiB'||wire.unit==='TiB',wire};
 }
 function payload(session,fields,enabled){
  const result={...fields};if(!enabled)return result;
  const unchanged=fields.quota_value===session.shown.value&&fields.quota_unit===session.shown.unit;
  if(session.legacy&&unchanged){result.quota_value=session.wire.value;result.quota_unit=session.wire.unit;}
  else if(fields.quota_value){const error=validate(fields.quota_value,fields.quota_unit);if(error)throw Error('套餐流量：'+error);}
  if(fields.calibration_value){const error=validate(fields.calibration_value,fields.calibration_unit,true);if(error)throw Error('用量校准：'+error);}
  return result;
 }
 const api={display,validate,open,payload};if(typeof module==='object'&&module.exports)module.exports=api;else root.PlanUnits=api;
})(typeof globalThis!=='undefined'?globalThis:this);
