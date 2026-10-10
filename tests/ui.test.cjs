// 通过真实 Bridge 与 sandbox iframe 验证配置页；宿主和评分响应均为本地夹具。
const { test, before, after, beforeEach, afterEach } = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..');
let server, browser, context, page, origin, ui;
const pageErrors = [];

function hostFixture() {
  const config = {
    enabled: false, default_enabled: false, accounts: {},
    iq_api: { base_url: '', api_key: '', model: 'swift-1.5-iq3_xxs', timeout_seconds: 30, max_retries: 2 },
    loop_detection: { enabled: true, check_host: true, check_model: false },
    queue: { worker_count: 4, max_size: 256, drop_when_full: true },
    capture: { max_request_body_bytes: 262144, max_response_body_bytes: 262144, max_records_per_account: 200, record_ttl_hours: 0 }
  };
  window.mock = {
    config, calls: [], saveMode: 'success', loadMode: 'success', statusMode: 'success', testMode: 'success', delay: 0,
    detail: { configured: true, enabled: false, requests: 12846, evaluated: 1920, queued: 3, failures: 2, dropped: 0, loop_skips: 0, host_services: true, transports: 4, uptime_seconds: 3661, version: '0.0.1', iq_api_model: 'swift-1.5-iq3_xxs' }
  };
  window.addEventListener('message', function (event) {
    const request = event.data;
    if (event.source !== document.querySelector('iframe').contentWindow || request.source !== 'sub2api-plugin-ui') return;
    if (!request.request_id) return;
    const mock = window.mock;
    mock.calls.push({ type: request.type, config: request.config });
    const response = { source: 'sub2api-plugin-host', bridge_token: request.bridge_token, request_id: request.request_id, type: request.type + '.result', ok: true };
    if (request.type === 'config.load') {
      if (mock.loadMode === 'failure') { response.ok = false; response.error = 'fixture load failure'; }
      else response.config = mock.config;
    } else if (request.type === 'config.save') {
      if (mock.saveMode === 'failure') { response.ok = false; response.error = 'fixture save failure'; }
      else {
        if (mock.saveMode !== 'timeout-unapplied') {
          mock.config = request.config;
          mock.config.iq_api.base_url = mock.config.iq_api.base_url.trim();
          mock.detail.enabled = mock.config.enabled;
        }
        if (mock.saveMode.startsWith('timeout')) return;
        response.config = mock.config;
      }
    } else if (request.type === 'config.test') {
      response.result = { success: mock.testMode === 'success', message: 'fixture diagnostic', latency_ms: 125 };
      response.ok = response.result.success;
    } else if (request.type === 'plugin.status') {
      if (mock.statusMode === 'failure') { response.ok = false; response.error = 'fixture status failure'; }
      else response.result = { healthy: true, message: 'ok', status_json: JSON.stringify(mock.detail) };
    }
    setTimeout(() => event.source.postMessage(response, '*'), request.type === 'config.save' || request.type === 'config.test' ? mock.delay : 0);
  });
}

before(async () => {
  server = http.createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/host') {
      res.setHeader('Content-Type', 'text/html; charset=utf-8');
      res.end('<!doctype html><html><head><title>测试宿主</title></head><body style="margin:0"><script src="/host.js"></script><iframe title="IQ 插件" sandbox="allow-scripts" style="border:0;width:100%;height:960px;display:block" src="/ui/index.html#bridge_token=ui-test-fixture"></iframe></body></html>');
    } else if (pathname === '/host.js') {
      res.setHeader('Content-Type', 'application/javascript');
      res.end('(' + hostFixture.toString() + ')();');
    } else if (/^\/ui\/(index\.html|assets\/(app\.css|app\.js|bridge-v1\.js))$/.test(pathname)) {
      res.setHeader('Content-Type', pathname.endsWith('.css') ? 'text/css' : pathname.endsWith('.js') ? 'application/javascript' : 'text/html; charset=utf-8');
      res.setHeader('Content-Security-Policy', "default-src 'none'; script-src " + origin + "; style-src " + origin + "; img-src " + origin + "; base-uri 'none'; form-action 'none'");
      res.end(await fs.readFile(path.join(root, 'plugin', pathname.slice(1))));
    } else { res.writeHead(404); res.end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  origin = 'http://127.0.0.1:' + server.address().port;
  browser = await chromium.launch({ headless: true, executablePath: process.env.UI_CHROMIUM_PATH || undefined });
});

after(async () => {
  if (browser) await browser.close();
  if (server) await new Promise(resolve => server.close(resolve));
});

beforeEach(async () => {
  pageErrors.length = 0;
  context = await browser.newContext({ viewport: { width: 1100, height: 960 } });
  context.setDefaultTimeout(5000);
  // 缩短 Bridge 等待窗口，测试超时恢复而不实际等待 25 秒。
  await context.addInitScript(() => {
    const original = window.setTimeout;
    window.setTimeout = function (fn, ms, ...args) { return original(fn, ms === 25000 ? 800 : ms, ...args); };
  });
  page = await context.newPage();
  page.on('pageerror', error => pageErrors.push(error.message));
  await page.goto(origin + '/host');
  ui = page.frameLocator('iframe');
  await ui.locator('#save-state').filter({ hasText: '与已保存配置一致' }).waitFor();
});

afterEach(async () => {
  await context.close();
  assert.deepEqual(pageErrors, [], '页面不应产生未处理异常');
});

async function calls(type) { return page.evaluate(type => window.mock.calls.filter(call => call.type === type), type); }
async function expectText(selector, text) { await ui.locator(selector).filter({ hasText: text }).waitFor(); }
async function openAdvanced() { await ui.locator('#advanced-settings > summary').click(); }

test('载入真实状态，保存按钮按草稿变化启用，撤销输入恢复干净状态', async () => {
  assert.equal(await ui.locator('#metric-requests').textContent(), '12,846');
  assert.equal(await ui.locator('#save').isDisabled(), true);
  assert.equal(await ui.locator('#save-test').isEnabled(), true);
  await ui.locator('#model').fill('another-model');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), true);
  await ui.locator('#model').fill('swift-1.5-iq3_xxs');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), false);
  assert.equal(await ui.locator('#save').isDisabled(), true);
  await ui.locator('.status-details summary').click();
  assert.match(await ui.locator('#status-list').textContent(), /丢弃任务0/);
});

test('保存和保存并测试均拦截必填、范围及整数错误，并聚焦摘要', async () => {
  await ui.locator('#enabled').check();
  await ui.locator('#save-test').click();
  assert.equal((await calls('config.save')).length, 0);
  assert.equal(await ui.locator('#base_url').getAttribute('aria-invalid'), 'true');
  assert.equal(await ui.locator('#error-summary').evaluate(el => el === document.activeElement), true);
  await ui.locator('#base_url').fill('https://judge.example.test/v1');
  await openAdvanced();
  await ui.locator('#timeout_seconds').fill('601');
  await ui.locator('#save').click();
  assert.equal((await calls('config.save')).length, 0);
  await ui.locator('#error-list a').filter({ hasText: '请求超时' }).click();
  assert.equal(await ui.locator('#timeout_seconds').evaluate(el => el === document.activeElement), true);
  await ui.locator('#timeout_seconds').fill('30');
  await ui.locator('#worker_count').fill('1.5');
  await ui.locator('#save-test').click();
  assert.equal((await calls('config.save')).length, 0);
  assert.match(await ui.locator('#worker_count-error').textContent(), /整数/);
});

test('规则支持行内注释，拒绝重复账号和多余字段，保持原配置契约', async () => {
  await ui.locator('#accounts').fill('42 = on, 0.2 # 20%\n43 = off');
  await ui.locator('#save').click();
  await expectText('#message', '已保存并应用');
  const saved = (await calls('config.save'))[0].config;
  assert.deepEqual(saved.accounts, { 42: { enabled: true, sample_ratio: .2 }, 43: { enabled: false, sample_ratio: 1 } });
  assert.deepEqual(saved.loop_detection, { enabled: true, check_host: true, check_model: false });
  await ui.locator('#accounts').fill('42 = on\n42 = off');
  await ui.locator('#save-test').click();
  assert.match(await ui.locator('#accounts-error').textContent(), /重复/);
  await ui.locator('#accounts').fill('42 = on, 1, 0.5');
  await ui.locator('#save-test').click();
  assert.match(await ui.locator('#accounts-error').textContent(), /一个采样率/);
  assert.equal((await calls('config.save')).length, 1);
});

test('草稿范围摘要区分总开关、默认策略和零采样率', async () => {
  await ui.locator('#enabled').check();
  await expectText('#scope-summary', '尚无可评测账号');
  await ui.locator('#accounts').fill('42 = on, 0\n43 = off\n44 = on, 0.1');
  await expectText('#scope-summary', '仅评测 1 个');
  await ui.locator('#default_enabled').check();
  await expectText('#scope-summary', '接口用量与费用');
  await ui.locator('#check_host').uncheck();
  assert.equal(await ui.locator('#host-warning').isVisible(), true);
});

test('放弃修改需要确认，取消保留草稿，确认恢复已保存配置', async () => {
  await ui.locator('#model').fill('draft-model');
  await ui.locator('#reload').click();
  await ui.locator('#cancel-discard').click();
  assert.equal(await ui.locator('#model').inputValue(), 'draft-model');
  await ui.locator('#reload').click();
  await ui.locator('#confirm-discard').click();
  assert.equal(await ui.locator('#model').inputValue(), 'swift-1.5-iq3_xxs');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), false);
});

test('保存与测试串行且全程锁定表单，诊断失败保留已保存状态', async () => {
  await page.evaluate(() => { window.mock.delay = 200; window.mock.testMode = 'failure'; });
  await ui.locator('#base_url').fill('https://judge.example.test/v1');
  await ui.locator('#save-test').click();
  assert.equal(await ui.locator('#model').isDisabled(), true);
  assert.equal(await ui.locator('#save-test').isDisabled(), true);
  await expectText('#message', '接口测试失败');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), false);
  const sequence = await page.evaluate(() => window.mock.calls.filter(call => ['config.save', 'config.test'].includes(call.type)).map(call => call.type));
  assert.deepEqual(sequence, ['config.save', 'config.test']);
});

test('保存失败保留输入，且不发送测试请求', async () => {
  await page.evaluate(() => { window.mock.saveMode = 'failure'; });
  await ui.locator('#model').fill('draft-model');
  await ui.locator('#save-test').click();
  await expectText('#message', '保存失败');
  assert.equal(await ui.locator('#model').inputValue(), 'draft-model');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), true);
  assert.equal((await calls('config.test')).length, 0);
});

test('受限 iframe 中按 Enter 保存，文本域中的 Enter 保持换行', async () => {
  await ui.locator('#model').fill('keyboard-model');
  await ui.locator('#model').press('Enter');
  await expectText('#message', '已保存并应用');
  assert.equal((await calls('config.save')).length, 1);
  await ui.locator('#accounts').fill('42 = on');
  await ui.locator('#accounts').press('End');
  await ui.locator('#accounts').press('Enter');
  assert.equal((await calls('config.save')).length, 1);
  assert.match(await ui.locator('#accounts').inputValue(), /\n$/);
});

test('初次加载失败禁用编辑，重试成功后恢复', async () => {
  await page.route('**/host.js', route => route.fulfill({
    contentType: 'application/javascript',
    body: '(' + hostFixture.toString() + ')(); window.mock.loadMode = "failure";'
  }));
  await page.reload();
  await expectText('#connection-text', '配置读取失败');
  assert.equal(await ui.locator('#model').isDisabled(), true);
  assert.equal(await ui.locator('#save-test').isDisabled(), true);
  await page.evaluate(() => { window.mock.loadMode = 'success'; });
  await ui.locator('#retry-load').click();
  await expectText('#save-state', '与已保存配置一致');
  assert.equal(await ui.locator('#model').isEnabled(), true);
});

test('保存超时且未应用：核对宿主结果但保留草稿及警告', async () => {
  await page.evaluate(() => { window.mock.saveMode = 'timeout-unapplied'; });
  await ui.locator('#model').fill('draft-model');
  await ui.locator('#save-test').click();
  await expectText('#message', '与已保存配置不同');
  assert.equal(await ui.locator('#model').inputValue(), 'draft-model');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), true);
  assert.equal((await calls('config.test')).length, 0);
});

test('保存超时且已应用：核对成功后确认一致，不自动重复保存或测试', async () => {
  await page.evaluate(() => { window.mock.saveMode = 'timeout-applied'; });
  await ui.locator('#model').fill('new-model');
  await ui.locator('#save-test').click();
  await expectText('#message', '当前表单与已保存配置一致');
  assert.equal(await ui.locator('#dirty-badge').isVisible(), false);
  assert.equal((await calls('config.save')).length, 1);
  assert.equal((await calls('config.test')).length, 0);
});

test('超时后核对失败时阻止继续保存，恢复连接后可再次核对', async () => {
  await page.evaluate(() => { window.mock.saveMode = 'timeout-unapplied'; window.mock.loadMode = 'failure'; });
  await ui.locator('#model').fill('draft-model');
  await ui.locator('#save').click();
  await expectText('#message', '保存结果尚未确认');
  assert.equal(await ui.locator('#save').isDisabled(), true);
  assert.equal(await ui.locator('#save-test').isDisabled(), true);
  await page.evaluate(() => { window.mock.loadMode = 'success'; });
  await ui.locator('#reconcile-config').click();
  await expectText('#message', '与已保存配置不同');
  assert.equal(await ui.locator('#model').inputValue(), 'draft-model');
  assert.equal(await ui.locator('#save').isEnabled(), true);
});

test('状态失败保留旧指标并标注过期，重复刷新只产生一个请求', async () => {
  await page.evaluate(() => { window.mock.statusMode = 'failure'; });
  const count = (await calls('plugin.status')).length;
  await ui.locator('#refresh-status').evaluate(el => { el.click(); el.click(); });
  await expectText('#health-badge', '状态读取失败');
  assert.equal((await calls('plugin.status')).length, count + 1);
  assert.equal(await ui.locator('#metric-requests').textContent(), '12,846');
  assert.match(await ui.locator('#status-updated').textContent(), /过期/);
});

test('直接打开时提示宿主入口，表单不可提交', async () => {
  await page.goto(origin + '/ui/index.html');
  assert.equal(await page.locator('#health-badge').textContent(), '未连接宿主');
  assert.equal(await page.locator('#save-test').isDisabled(), true);
  assert.match(await page.locator('#connection-message').textContent(), /无法独立读取或保存/);
});

test('明暗主题、窄屏和文本放大没有水平溢出，所有字段有标签', async () => {
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' });
    for (const width of [320, 375, 768, 1024, 1440]) {
      await page.setViewportSize({ width, height: 960 });
      const overflow = await ui.locator('html').evaluate(el => el.scrollWidth > el.clientWidth);
      assert.equal(overflow, false, `${colorScheme} / ${width}px 不应横向溢出`);
    }
  }
  const unlabeled = await ui.locator('input, textarea').evaluateAll(elements => elements.filter(el => !el.labels.length).map(el => el.id));
  assert.deepEqual(unlabeled, []);
  await page.setViewportSize({ width: 375, height: 960 });
  await ui.locator('body').evaluate(el => { el.style.fontSize = '28px'; });
  assert.equal(await ui.locator('html').evaluate(el => el.scrollWidth > el.clientWidth), false);
  assert.equal(await ui.locator('#enabled').evaluate(el => getComputedStyle(el, '::after').transitionDuration), '0s');
});

// 可选截图仅写入调用方指定的临时目录。
test('桌面与移动端视觉检查截图', { skip: !process.env.UI_SCREENSHOT_DIR }, async () => {
  await fs.mkdir(process.env.UI_SCREENSHOT_DIR, { recursive: true });
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' });
  const desktopHeight = await ui.locator('body').evaluate(el => el.scrollHeight + 64);
  await page.locator('iframe').evaluate((el, height) => { el.style.height = height + 'px'; }, desktopHeight);
  await page.screenshot({ path: path.join(process.env.UI_SCREENSHOT_DIR, 'iq-desktop-light.png'), fullPage: true });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.screenshot({ path: path.join(process.env.UI_SCREENSHOT_DIR, 'iq-desktop-dark.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 960 });
  await page.emulateMedia({ colorScheme: 'light' });
  const mobileHeight = await ui.locator('body').evaluate(el => el.scrollHeight + 64);
  await page.locator('iframe').evaluate((el, height) => { el.style.height = height + 'px'; }, mobileHeight);
  await page.screenshot({ path: path.join(process.env.UI_SCREENSHOT_DIR, 'iq-mobile-light.png'), fullPage: true });
});
