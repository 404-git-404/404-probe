const assert = require('node:assert/strict');
const {test, before, after} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });

test('renewal URL is validated and links follow the actual renewal deadline', async () => {
  const payment = 'https://billing.example.invalid/renew?plan=synthetic';
  const target = ID(0);
  let agent;
  const today = new Date().toISOString().slice(0,10);
  const future = new Date(Date.now() + 366 * 86400000).toISOString().slice(0,10);
  const normal = {from_date:today,to_date:future,field:'renewal_date'};
  const session = await open(browser, {n:5,handle: async ({req,url,body,json}) => {
    if (url.pathname === `/api/v1/web/agents/${target}/plan` && req.method === 'PUT') {
      agent.plan = {...agent.plan,...body};
      json({...agent.plan,renewal:agent.renewal});
      return true;
    }
    if (url.pathname.endsWith('/renewal/preview')) {
      json({...normal,eligible:true,expected_revision:'a'.repeat(32),period:'yearly',timezone:'UTC',anchor_source:'current_date',new_overdue:false,due_today:false,unchanged_field:'expiry_date'});
      return true;
    }
    if (url.pathname.endsWith('/renewal/apply')) {
      agent.plan.renewal_date = normal.to_date;
      agent.renewal = {field:'renewal_date',type:'upcoming',date:future,days:366,timezone:'UTC'};
      json({replayed:false,current_revision:'b'.repeat(32),current_plan:agent.plan,renewal:agent.renewal,undo:{available:true,operation_id:body.request_id},operation:{...normal,request_id:body.request_id,kind:'apply',applied_at:Date.now(),undo_until:Date.now()+300000}});
      return true;
    }
    return false;
  }});
  try {
    const {page, f} = session;
    agent = f.state.items[0];
    for(const [i,a] of f.state.items.entries()) {
      const days = [0,7,8,-1,3][i];
      const date = new Date(Date.now() + days*86400000).toISOString().slice(0,10);
      a.plan = {...a.plan,renewal_date:i===4?'':date,renewal_url:i===3?'javascript:alert(1)':payment,expiry_date:i===4?date:'',timezone:'UTC'};
      a.renewal = {field:i===4?'expiry_date':'renewal_date',type:days<0?'overdue':days<=7?'due_soon':'upcoming',date,days,timezone:'UTC'};
    }
    await page.evaluate(() => dashboardData.reload());
    await page.waitForFunction(() => !renderFrame);
    const card = page.locator(`.agent-card[data-agent-id="${target}"]`);
    await card.locator('.edit-plan').click();
    const field = page.locator('#plan-renewal-url');
    assert.equal(await field.inputValue(), payment);
    await field.fill('ftp://example.invalid/plan');
    assert.equal(await field.getAttribute('aria-invalid'), 'true');
    assert.match(await page.locator('#plan-renewal-url-hint').textContent(), /只支持 http/);
    await field.fill('https://alice:fixture@example.invalid/plan');
    assert.match(await page.locator('#plan-renewal-url-hint').textContent(), /网址里不能带账号和密码/);
    await field.fill(payment);
    assert.equal(await page.locator('#plan-dialog a').count(), 0);
    await page.locator('#plan-save').click();
    await page.waitForFunction(() => !document.querySelector('#plan-dialog').open);
    await page.locator('[data-view=list]').click();
    await page.waitForFunction(() => !renderFrame && document.querySelector('.banner-row'));
    const links = page.locator('.banner-renewal-link:visible');
    assert.equal(await links.count(), 2);
    const row = page.locator(`.banner-row[data-agent-id="${target}"]`);
    const go = row.locator('.banner-renewal-link');
    const rowHeight = await row.evaluate(el => el.getBoundingClientRect().height);
    assert.equal(await go.getAttribute('href'), payment);
    assert.equal(await go.getAttribute('target'), '_blank');
    assert.equal(await go.getAttribute('rel'), 'noopener noreferrer');
    await row.locator('.banner-name').click();
    const details = page.locator('#device-details .details-renewal-url');
    assert.equal(await details.textContent(), payment);
    assert.equal(await details.getAttribute('title'), payment);
    assert.equal(await details.evaluate(el => el.closest('a')), null);
    await page.locator('#device-details [data-focus-key=detail-renewal]').click();
    await page.locator('#renewal-dates').waitFor();
    assert.equal(await page.locator('#renewal-go').getAttribute('href'), payment);
    assert.equal(await page.locator('#renewal-go').getAttribute('rel'), 'noopener noreferrer');
    assert.equal(await page.locator('#renewal-detail').textContent(), '');
    await page.locator('#renewal-confirm').click();
    await page.waitForFunction(id => agents.get(id).plan.renewal_date !== document.querySelector('#renewal-from').textContent, target);
    await page.locator('#renewal-dismiss').click();
    await go.waitFor({state:'hidden'});
    assert.equal(await row.evaluate(el => el.getBoundingClientRect().height), rowHeight);
    assert.equal(await links.count(), 1);
    assert.deepEqual(session.errors, []);
  } finally { await session.close(); }
});
