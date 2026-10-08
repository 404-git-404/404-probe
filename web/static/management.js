/* P2a field writes: reuse P1's single fetch + body deadline. */
(function (root) {
  'use strict';
  function validName(value) {
    const name = value.trim();
    const bytes = new TextEncoder().encode(name).length;
    return Boolean(name) && bytes <= 100 && !/[\r\n\0]/.test(name);
  }
  function write(path, {method = 'POST', payload, csrf, signal, fetcher = root.fetch,
    timeoutMs = 10000, unauthorized = () => {}}) {
    return root.AgentState.fetchJSON(path, {signal, timeoutMs, unauthorized,
      fetcher: async (url, options) => {
        const response = await fetcher(url, {...options, method,
          headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf},
          body: JSON.stringify(payload)});
        if (response.status === 401) return response;
        if (!response.ok) {
          let body;
          try { body = await response.json(); } catch (_) { /* status is authoritative */ }
          throw Object.assign(new Error(body?.error?.message || response.statusText || '请求失败'),
            {status: response.status});
        }
        return response.status === 204 ? {ok: true, status: 200, json: async () => null} : response;
      }});
  }
  // Same ISO subset as internal/protocol/country.go; no pseudo-region flags.
  const codes = 'AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW'.split(' ');
  function countries(intl = root.Intl) {
    let zh, en;
    try { zh = new intl.DisplayNames(['zh'], {type: 'region'}); en = new intl.DisplayNames(['en'], {type: 'region'}); } catch (_) { /* codes remain usable */ }
    return codes.map(code => ({code, zh: zh?.of(code) || code, en: en?.of(code) || code}));
  }
  function matches(query, current = '', catalog = countries()) {
    const needle = query.trim().toLocaleLowerCase();
    const result = catalog.filter(item => [item.code, item.zh, item.en].some(value => value.toLocaleLowerCase().includes(needle)));
    if (/^[A-Z]{2}$/.test(current) && !result.some(item => item.code === current)) {
      result.unshift(catalog.find(item => item.code === current) || {code: current, zh: current, en: current});
    }
    return result;
  }
  function countryPicker({search, select, clear, preview, status, onchange}) {
    const catalog = countries();
    let selected = '';
    function render() {
      select.replaceChildren();
      const automatic = root.document.createElement('option');
      automatic.value = ''; automatic.textContent = '使用 Agent 识别值'; select.append(automatic);
      const found = matches(search.value, selected, catalog);
      for (const item of found) {
        const option = root.document.createElement('option'); option.value = item.code;
        option.textContent = `${item.zh} · ${item.en} (${item.code})`; select.append(option);
      }
      select.value = selected;
      preview.replaceChildren();
      if (selected) {
        const flag = root.document.createElement('img'); flag.src = `/vendor/flag-icons/4x3/${selected.toLowerCase()}.svg`;
        flag.alt = selected; flag.width = 24; flag.height = 18;
        flag.addEventListener('error', () => { flag.hidden = true; }); preview.append(flag);
      }
      const needle = search.value.trim().toLocaleLowerCase();
      const count = catalog.filter(item => [item.code, item.zh, item.en].some(value => value.toLocaleLowerCase().includes(needle))).length;
      status.textContent = `${count ? `${count} 个匹配地区` : '无匹配地区；当前选择仍保留'}。${selected ? `手动覆盖 ${selected}` : '使用 Agent 识别值'}；保存套餐后生效。`;
    }
    search.addEventListener('input', render);
    select.addEventListener('change', () => { selected = select.value; render(); onchange(); });
    clear.addEventListener('click', () => { selected = ''; search.value = ''; render(); onchange(); });
    return {set(code) { selected = /^[A-Z]{2}$/.test(code || '') ? code : ''; search.value = ''; render(); }};
  }
  const api = {validName, write, countries, matches, countryPicker};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.Management = api;
})(globalThis);
