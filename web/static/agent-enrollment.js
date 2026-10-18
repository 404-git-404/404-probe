/* "Add Agent" dialog flow: choose one-click or manual install; for one-click, create a single-use
   30-minute registration token, show the install command with a copy button and a countdown, and
   allow revoking it. The command lives only in this controller's memory and in one text node: it is
   dropped when the dialog closes or resets, and it is never stored in the page or the browser. */
(function(root){
  'use strict';

  const pad=n=>String(n).padStart(2,'0');
  function countdownText(ms){
    const total=Math.max(0,Math.ceil(ms/1000));
    return `剩余 ${pad(Math.floor(total/60))}:${pad(total%60)}`;
  }

  async function readError(response){
    try{const body=await response.json();return body&&body.error?body.error:{};}catch(error){return {};}
  }

  function createController({els,csrf=()=>'',fetcher=(...a)=>root.fetch(...a),now=()=>Date.now(),
    setTimer=(f,ms)=>root.setInterval(f,ms),clearTimer=id=>root.clearInterval(id),
    clipboard=()=>root.navigator&&root.navigator.clipboard,available=()=>true,unavailableReason=()=>'',unauthorized=()=>{}}){
    const panes=['choice','manual','manualResult','oneclick','oneclickResult'];
    let state=null,timer=null,submitting=false,revoking=false,copyReset=null;

    function show(name){
      for(const pane of panes)els[pane].classList.toggle('hidden',pane!==name);
    }
    function stopTimer(){if(timer!==null){clearTimer(timer);timer=null;}if(copyReset!==null){clearTimer(copyReset);copyReset=null;}}
    function status(text){els.status.textContent=text||'';}
    function clearSecret(){
      els.command.textContent='';
      state=null;
    }
    function render(){
      if(!state){els.copy.disabled=true;els.revoke.disabled=true;return;}
      const remaining=state.expiresAt-now();
      if(state.ended){
        els.countdown.textContent=state.ended;
        els.copy.disabled=true;els.revoke.disabled=true;
        return;
      }
      if(remaining<=0){
        // Expired: the command is useless, so it is removed rather than left to be copied.
        els.command.textContent='';
        els.countdown.textContent='已过期，请重新生成';
        state.ended='已过期，请重新生成';
        els.copy.disabled=true;els.revoke.disabled=true;
        stopTimer();
        return;
      }
      els.countdown.textContent=countdownText(remaining);
      els.copy.disabled=false;
      els.revoke.disabled=revoking;
    }

    function reset(){
      stopTimer();clearSecret();
      submitting=false;revoking=false;
      els.command.textContent='';
      els.countdown.textContent='';
      status('');
      els.error.classList.add('hidden');
      els.name.value='';
      els.submit.disabled=false;
      els.copy.disabled=true;els.revoke.disabled=true;
      show('choice');
      const ok=Boolean(available());
      els.chooseOneClick.disabled=!ok;
      els.reason.textContent=ok?'':(unavailableReason()||'当前 Server 构建无法使用一键安装');
      els.reason.classList.toggle('hidden',ok);
    }

    function chooseOneClick(){
      if(els.chooseOneClick.disabled)return;
      els.name.value='';els.error.classList.add('hidden');els.submit.disabled=false;
      show('oneclick');
    }
    function chooseManual(){show('manual');}
    function back(){reset();}

    async function submit(){
      if(submitting||!csrf())return false;
      const name=els.name.value;
      submitting=true;els.submit.disabled=true;els.error.classList.add('hidden');
      try{
        const response=await fetcher('/api/v1/web/agent-enrollments',{
          method:'POST',cache:'no-store',
          headers:{'Content-Type':'application/json','X-CSRF-Token':csrf()},
          body:JSON.stringify({name}),
        });
        if(response.status===401){unauthorized();return false;}
        if(!response.ok){
          const error=await readError(response);
          els.error.textContent=error.code==='install_unavailable'?(error.message||'此构建无法使用一键安装'):
            error.code==='enrollment_limit'?'待使用的安装命令过多，请先作废或等待过期':'生成失败，请重试。';
          els.error.classList.remove('hidden');
          els.submit.disabled=false;
          return false;
        }
        const view=await response.json();
        state={id:view.enrollment_id,expiresAt:view.expires_at,ended:''};
        els.command.textContent=view.command;
        status('');
        show('oneclickResult');
        render();
        timer=setTimer(render,1000);
        return true;
      }catch(error){
        els.error.textContent='生成失败，请重试。';
        els.error.classList.remove('hidden');
        els.submit.disabled=false;
        return false;
      }finally{submitting=false;}
    }

    async function copy(){
      if(!state||state.ended)return false;
      const label='复制命令';
      try{
        await clipboard().writeText(els.command.textContent);
        els.copy.textContent='已复制';
      }catch(error){els.copy.textContent='复制失败';}
      if(copyReset!==null)clearTimer(copyReset);
      copyReset=setTimer(()=>{els.copy.textContent=label;clearTimer(copyReset);copyReset=null;},1200);
      return true;
    }

    async function revoke(){
      if(!state||state.ended||revoking||!csrf())return false;
      revoking=true;render();status('');
      const id=state.id;
      try{
        const response=await fetcher(`/api/v1/web/agent-enrollments/${encodeURIComponent(id)}/revoke`,{
          method:'POST',cache:'no-store',
          headers:{'Content-Type':'application/json','X-CSRF-Token':csrf()},
          body:'{}',
        });
        if(response.status===401){unauthorized();return false;}
        if(response.ok){
          els.command.textContent='';
          state.ended='已作废';
          status('这条命令已作废，无法再用于安装。');
          return true;
        }
        const error=await readError(response);
        status(error.code==='enrollment_used'?'这条命令已被使用，无法作废。':'作废失败，请重试。');
        if(error.code==='enrollment_used'){els.command.textContent='';state.ended='已使用';}
        return false;
      }catch(error){
        status('作废失败，请重试。');
        return false;
      }finally{revoking=false;if(state)render();else els.revoke.disabled=true;}
    }

    function close(){reset();}

    els.chooseOneClick.addEventListener('click',chooseOneClick);
    els.chooseManual.addEventListener('click',chooseManual);
    els.oneclickBack.addEventListener('click',back);
    els.form.addEventListener('submit',event=>{if(event&&event.preventDefault)event.preventDefault();return submit();});
    els.copy.addEventListener('click',copy);
    els.revoke.addEventListener('click',revoke);
    reset();
    return {reset,close,chooseOneClick,chooseManual,submit,copy,revoke,back,tick:render,get hasCommand(){return Boolean(state&&!state.ended);}};
  }

  const api={createController,countdownText};
  if(typeof module!=='undefined'&&module.exports)module.exports=api;else root.AgentEnrollment=api;
})(globalThis);
