const test = require('node:test');
const assert = require('node:assert/strict');
const YouTube = require('../web/static/youtube-status.js');

const now = Date.now();
const regions = ['CN', 'MO', 'AL', 'RU'];
const us = {status: 'not_cn', region: 'US'};
const cn = {status: 'cn', sent_to_china: true, region: 'US'};
const agent = (youtube, extra = {}) => ({online: true, google_status: {
  supported: true, checked_at: now - 1000, next_due_at: now + 86400000, result: {youtube}, ...extra
}});
const display = a => YouTube.display(a, regions, now);

test('M11 and R52 show only the effective region and use the supplied list', () => {
  for (const [detected, manual, label, regionTone] of [
    [us, 'MO', '地区 MO', 'crit'],
    [cn, 'US', '地区 US', 'ok'],
    [cn, '', '地区 CN', 'crit'],
    [us, '', '地区 US', 'ok'],
    [{status: 'not_cn', region: 'AL'}, '', '地区 AL', 'crit'],
    [{status: 'not_cn', region: 'BS'}, '', '地区 BS', 'ok']
  ]) {
    const result = display(agent(detected, {manual_region: manual}));
    assert.equal(result.label, label);
    assert.equal(result.regionTone, regionTone);
    assert.equal(result.tone, 'observed');
    assert.doesNotMatch(result.label, /正常|解锁|送中|手动/);
  }
  assert.equal(YouTube.display(agent(us), ['US'], now).regionTone, 'crit');
  assert.equal(display(agent(us, {supported: false, manual_region: 'MO'})).regionTone, 'crit');
  assert.deepEqual(display(agent(us, {manual_region: 'MO'})), display(agent({status: 'not_cn', region: 'MO'})));
});

test('M11 missing, pending, stale, offline, paused and unsupported retain their labels and neutral color', () => {
  for (const [input, label] of [
    [agent(us, {result: null}), '尚未检测'],
    [agent(us, {pending: true}), '检测中'],
    [agent(us, {supported: false}), '不支持'],
    [agent(us, {stale: true}), '过期·US'],
    [{...agent(cn), online: false}, '旧·CN'],
    [{...agent(us), disabled_at: 1}, '旧·US'],
    [agent(cn, {next_due_at: now - 1}), '过期·CN'],
    [agent(us, {next_due_at: undefined, checked_at: now - 86400001}), '过期·US'],
    [agent({status: 'not_cn', region: 'ZZ'}), '未知']
  ]) {
    assert.equal(display(input).label, label);
    assert.equal(display(input).regionTone, 'unknown');
  }
});

test('L2 keeps fresh EU consent green and stale, unavailable or failed results neutral', () => {
  const consent = {status: 'unknown', error: {category: 'consent_page'}};
  assert.equal(display(agent(consent)).label, '地区 EU');
  assert.equal(display(agent(consent)).regionTone, 'ok');
  assert.equal(YouTube.view(agent(consent), now).known, false);
  for (const extra of [{pending: true}, {supported: false}, {stale: true}, {result: null}]) {
    assert.notEqual(display(agent(consent, extra)).label, '地区 EU');
    assert.equal(display(agent(consent, extra)).regionTone, 'unknown');
  }
  assert.equal(display(agent({...us, error: consent.error})).label, '地区 US');
  for (const category of ['challenge_page', 'region_conflict', 'timeout', 'rate_limited', 'tls_error',
    'invalid_configuration', 'missing_evidence', 'unexpected_page', 'unrecognized_response', 'china_signal_unverified']) {
    const input = agent({status: 'unknown', error: {category}});
    assert.equal(display(input).label, '未知');
    assert.equal(display(input).regionTone, 'unknown');
    assert.ok(YouTube.view(input, now).reason.length > 0);
  }
});

test('management describes manual and detected values, failure reasons and timestamps', () => {
  const summary = a => YouTube.summary(a, regions, code => ({MO: '澳门 MO', US: '美国 US', CN: '中国 CN'}[code]), () => '11-10 02:33');
  assert.equal(summary(agent(us, {manual_region: 'MO'})), '手动设置：澳门 MO（无广告地区） · 自动检测：美国 US · 上次检测 11-10 02:33');
  assert.equal(summary(agent(us)), '自动检测：美国 US · 上次检测 11-10 02:33');
  for (const premium_unavailable_notice of [true, false, undefined]) {
    assert.match(summary(agent({...cn, premium_unavailable_notice})), /中国 CN（无广告地区）.*页面地区 US（送中时不可信）/);
  }
  assert.doesNotMatch(summary(agent({...cn, region: 'CN'})), /页面地区/);
  assert.match(summary(agent({status: 'unknown', error: {category: 'timeout'}})), /自动检测：未知.*请求超时.*上次检测/);
  assert.match(summary(agent({status: 'unknown', error: {category: 'job_expired'}})), /不能据此确认已执行网络检测.*作业到期 11-10 02:33/);
});
