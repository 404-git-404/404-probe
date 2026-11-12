/* One observation model for the card and history. No requests or timers. */
(function(root){
  'use strict';
  const countryCodes=new Set('AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW'.split(' '));
  const reasons={timeout:'请求超时（包括正文读取）',canceled:'检测已取消',dns_error:'DNS 解析失败',tls_error:'TLS 证书校验失败',network_error:'网络请求失败',unsafe_address:'没有安全的公网 IPv4 目标',response_read_error:'响应正文读取失败',response_body_too_large:'响应正文超过上限',redirect_limit:'跳转次数超过上限',unexpected_redirect:'跳转超出允许范围',invalid_target:'检测目标无效',unrecognized_response:'响应无法提供可信地区证据',consent_page:'需要确认隐私同意',challenge_page:'收到验证或挑战页面',region_conflict:'可信配置中的地区互相冲突',invalid_region:'地区代码无效',invalid_configuration:'页面配置无效或重复',china_signal_unverified:'页面出现中国区信号但无法确认',missing_evidence:'缺少可信页面地区证据',unexpected_page:'最终页面不是 Premium 检测页',response_truncated:'页面不完整',http_forbidden:'HTTP 403，不能据此认定地区封锁',rate_limited:'HTTP 429，检测被限流',http_restricted:'HTTP 451，原因不足以判定地区封锁',http_server_error:'检测站点服务异常',job_expired:'检测作业到期，未取得结果；不能据此确认已执行网络检测'};
  function view(agent,now=Date.now()){
    const g=agent?.google_status,y=g?.result?.youtube;
    // 送中: Google treats this IP as being in China. It is displayed as CN;
    // the page region is only a default in this case.
    const china=Boolean(y&&y.status==='cn');
    const known=Boolean(y&&y.status==='not_cn'&&countryCodes.has(y.region));
    const pageRegion=china&&y.region&&y.region!=='CN'&&countryCodes.has(y.region)?y.region:'';
    const consent=Boolean(y&&y.status==='unknown'&&y.error?.category==='consent_page');
    const expired=Boolean(g?.stale||(g?.next_due_at&&now>=g.next_due_at)||(!g?.next_due_at&&g?.checked_at&&now-g.checked_at>=86400000));
    const old=agent?.online===false||Boolean(agent?.disabled_at||agent?.revoked);
    let label,tone='unknown',state;
    if(!g?.supported){label='不支持';state='当前 Agent 不支持检测';}
    else if(g.pending){label='检测中';tone='pending';state='检测正在进行，上次记录不是本次结论';}
    else if(!g.result){label='尚未检测';state='尚未检测';}
    else if(old){label=`旧·${china?'CN':known?y.region:'未知'}`;tone='stale';state='Agent 离线或暂停，当前地区未知';}
    else if(expired){label=`过期·${china?'CN':known?y.region:'未知'}`;tone='stale';state='结果已过期，当前地区未知';}
    else if(china){label='地区 CN';tone='observed';state='本次检测显示 Google 把本机 IP 当作中国（送中）';}
    else if(known){label=`地区 ${y.region}`;tone='observed';state='本次页面地区已识别';}
    else if(consent){label='地区 EU';tone='observed';state='遇到欧盟隐私同意页，具体国家未知';}
    else {label='未知';state='本次结果未知';}
    const reason=china?(pageRegion?'页面地区 '+pageRegion+'（送中时不可信）':'Google 把本机 IP 当作中国处理（送中）'):!known&&reasons[y?.error?.category]||'';
    const code=china?'CN':known?y.region:consent?'EU':null;
    return {label,code,tone,state,reason,known,expired:expired||old};
  }
  // Card and list share region resolution and color.
  function display(agent,regions=[],now=Date.now()){
    const automatic=view(agent,now),manual=agent?.google_status?.manual_region;
    const code=manual||automatic.code;
    const tone=manual?'observed':automatic.tone;
    return {
      label:manual?'地区 '+manual:automatic.label,
      code,tone,
      regionTone:tone==='observed'?(regions.includes(code)?'crit':'ok'):'unknown'
    };
  }
  function summary(agent,regions=[],countryName=code=>code,formatTime=at=>new Date(at).toLocaleString()){
    const automatic=view(agent),manual=agent?.google_status?.manual_region;
    const result=automatic.tone==='observed'?countryName(automatic.code):automatic.label;
    const parts=[];
    if(manual)parts.push('手动设置：'+countryName(manual)+(regions.includes(manual)?'（无广告地区）':''));
    parts.push('自动检测：'+result+(automatic.tone==='observed'&&regions.includes(automatic.code)?'（无广告地区）':''));
    if(automatic.reason)parts.push(automatic.reason);
    const checked=agent?.google_status?.checked_at;
    const expiredJob=agent?.google_status?.result?.youtube?.error?.category==='job_expired';
    parts.push((expiredJob?'作业到期 ':'上次检测 ')+(checked?formatTime(checked):'无记录'));
    return parts.join(' · ');
  }
  function errorText(error){
    const messages={google_status_pending:'YouTube 地区检测正在进行，请等待结果',google_status_unsupported:'当前 Agent 不支持 YouTube 地区检测',agent_offline:'设备离线，无法进行 YouTube 地区检测',agent_disabled:'设备暂停，无法进行 YouTube 地区检测',agent_revoked:'设备已撤销，无法进行 YouTube 地区检测',queue_full:'任务队列已满，暂时无法进行 YouTube 地区检测'};
    const message=typeof error==='string'?error:error?.message;
    return (Object.hasOwn(messages,error?.code)?messages[error.code]:null)||String(message||'YouTube 地区检测请求失败').replace(/Google Status|Google 检测|YouTube 检测/g,'YouTube 地区检测');
  }
  const api={view,display,summary,errorText};if(typeof module!=='undefined'&&module.exports)module.exports=api;else root.YouTubeStatus=api;
})(globalThis);
