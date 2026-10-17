/* One observation model for the card and history. No requests or timers. */
(function(root){
  'use strict';
  const countryCodes=new Set('AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW'.split(' '));
  // YouTube Premium availability, from Google's official list
  // https://support.google.com/youtube/answer/6307365?hl=en (checked 2026-10-17, 119 regions).
  // Not on the list means unavailable. Notes on entries that are not a plain country:
  // Russia is listed as "temporarily unavailable" and is therefore left out; South Korea is
  // listed as "Paid memberships only" (Premium itself is a paid membership) and is kept.
  // Territories are only included when listed separately (AS GU MP PR VI GF GP RE PF TC KY BM AW).
  const premiumCheckedAt='2026-10-17';
  const premiumRegions=new Set(`
    DZ LB AS LY AR LI AW LT AU LU AT MY AZ MT BH MX BD MA BY NP BE NL BM NZ BO NI BA NG BR MK BG MP KH NO CA
    OM KY PK CL PA CO PG CR PY HR PE CY PH CZ PL DK PT DO PR EC QA EG RE SV RO EE FI SA FR SN GF RS PF SG GE
    SK DE SI GH ZA GR KR GP ES GU LK GT SE HN CH HK TW HU TZ IS TH IN TN ID TR IQ TC IE VI IL UG IT UA JM AE
    JP GB JO US KZ UY KE VE KW VN LA YE LV ZW
  `.trim().split(/\s+/));
  const reasons={timeout:'请求超时（包括正文读取）',canceled:'检测已取消',dns_error:'DNS 解析失败',tls_error:'TLS 证书校验失败',network_error:'网络请求失败',unsafe_address:'没有安全的公网 IPv4 目标',response_read_error:'响应正文读取失败',response_body_too_large:'响应正文超过上限',redirect_limit:'跳转次数超过上限',unexpected_redirect:'跳转超出允许范围',invalid_target:'检测目标无效',unrecognized_response:'响应无法提供可信地区证据',consent_page:'需要确认隐私同意',challenge_page:'收到验证或挑战页面',region_conflict:'可信配置中的地区互相冲突',invalid_region:'地区代码无效',invalid_configuration:'页面配置无效或重复',missing_evidence:'缺少可信页面地区证据',unexpected_page:'最终页面不是 Premium 检测页',response_truncated:'页面不完整',http_forbidden:'HTTP 403，不能据此认定地区封锁',rate_limited:'HTTP 429，检测被限流',http_restricted:'HTTP 451，原因不足以判定地区封锁',http_server_error:'检测站点服务异常',job_expired:'检测作业到期，未取得结果；不能据此确认已执行网络检测'};
  function view(agent,now=Date.now()){
    const g=agent?.google_status,y=g?.result?.youtube;
    const known=Boolean(y&&(y.status==='cn'||y.status==='not_cn')&&countryCodes.has(y.region));
    const expired=Boolean(g?.stale||(g?.next_due_at&&now>=g.next_due_at)||(!g?.next_due_at&&g?.checked_at&&now-g.checked_at>=86400000));
    const old=agent?.online===false||Boolean(agent?.disabled_at||agent?.revoked);
    let label,tone='unknown',state;
    if(!g?.supported){label='不支持';state='当前 Agent 不支持检测';}
    else if(g.pending){label='检测中';tone='pending';state='检测正在进行，上次记录不是本次结论';}
    else if(!g.result){label='尚未检测';state='尚未检测';}
    else if(old){label=`旧·${known?y.region:'未知'}`;tone='stale';state='Agent 离线或暂停，当前地区未知';}
    else if(expired){label=`过期·${known?y.region:'未知'}`;tone='stale';state='结果已过期，当前地区未知';}
    else if(known){label=`地区 ${y.region}`;tone='observed';state='本次页面地区已识别';}
    else {label='未知';state='本次结果未知';}
    const time=g?.checked_at?new Date(g.checked_at).toLocaleString():'无记录';
    const reason=reasons[y?.error?.category]||(known?'提取到页面配置中的地区标记':'尚无可靠地区证据');
    const premium=tone==='observed'?(premiumRegions.has(y.region)?'available':'unavailable'):null;
    const premiumNote=premium==='available'?'Premium 可用地区':premium==='unavailable'?`Premium 不可用地区（依据 YouTube 官方名单，核对日期 ${premiumCheckedAt}）`:'';
    const prior=g?.result?`；${g.pending||old||expired?'上次记录':'结果记录'}：${known?'页面地区 '+y.region:'未知'}，${y?.error?.category==='job_expired'?'作业到期时间':'结果记录时间'} ${time}`:'';
    const description=`YouTube：${state}${prior}；${reason}。${premiumNote?premiumNote+'。':''}通过 IPv4 HTTPS 读取 Premium 页面；页面地区可能来自默认值、偏好或启发式判断，不代表出口 IP 归属，未验证视频播放或 Premium 解锁。`;
    return {label,tone,premium,state,description,reason,known,expired:expired||old};
  }
  const api={view};if(typeof module!=='undefined'&&module.exports)module.exports=api;else root.YouTubeStatus=api;
})(globalThis);
