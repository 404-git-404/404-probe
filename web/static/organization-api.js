(function(root){
 'use strict';
 function create({csrf,fetcher=(...args)=>root.fetch(...args),read}){
  async function request(method,path,body,revision){
   const headers={'X-CSRF-Token':csrf()};if(method==='DELETE')headers['If-Match']='"'+revision+'"';else headers['Content-Type']='application/json';
   const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),10000);let response;
   try{response=await fetcher('/api/v1/web/'+path,{method,headers,...(method==='DELETE'?{}:{body:JSON.stringify(body)}),cache:'no-store',signal:controller.signal});}
   catch{throw Error('保存结果尚未确认，请重新读取后核对。');}finally{clearTimeout(timer);}
   let value={};try{value=await response.json();}catch{}
   if(response.status===401){root.location.assign('/login');throw Object.assign(Error('登录已过期，请重新登录'),{status:401});}
   if(!response.ok){const code=value.error?.code,messages={revision_conflict:'资料已被其他页面修改，已重新读取。请检查后再保存。',name_conflict:'这个名称已经存在，请选择已有项目或换一个名称。',agent_revoked:'这台设备已撤销，无法修改归属。',limit_reached:'项目数量已达到上限。',usd_read_only:'美元汇率固定为 1，不能修改。',not_found:'项目已删除，请重新读取。'};throw Object.assign(Error(messages[code]||(response.status===409?'资料有冲突，已重新读取。请检查后再保存。':'填写内容不正确或暂时无法保存，请检查后重试。')),{status:response.status,code});}
   return value;
  }
  const catalog=()=>read('/api/v1/web/dashboard');
  return {request,catalog};
 }
 root.OrganizationAPI={create};
})(globalThis);
