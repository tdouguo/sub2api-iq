/* 静态配置页：状态只读刷新，草稿仅存内存，配置通过官方 Bridge 保存。 */
(function () {
  'use strict';

  var form = document.getElementById('config-form');
  var fields = document.getElementById('config-fields');
  var message = document.getElementById('message');
  var errorSummary = document.getElementById('error-summary');
  var discardConfirm = document.getElementById('discard-confirm');
  var saveButton = document.getElementById('save');
  var testButton = document.getElementById('save-test');
  var reloadButton = document.getElementById('reload');
  var refreshButton = document.getElementById('refresh-status');
  var reconcileButton = document.getElementById('reconcile-config');
  var bridge;
  var busy = false;
  var statusBusy = false;
  var loaded = false;
  var dirty = false;
  var uncertain = false;
  var savedConfig;
  var savedSnapshot = '';
  var statusTime = '';
  var operation = '';
  var controls = Array.prototype.slice.call(fields.querySelectorAll('input, textarea'));

  function field(name) { return form.elements.namedItem(name); }
  function setText(id, text) { document.getElementById(id).textContent = text; }

  function showMessage(kind, text, focus) {
    message.textContent = text;
    message.className = 'message' + (kind ? ' ' + kind : '');
    message.hidden = !text;
    if (focus && text) message.focus();
  }

  function updateControls() {
    fields.disabled = busy || !loaded;
    saveButton.disabled = busy || !loaded || !dirty || uncertain;
    testButton.disabled = busy || !loaded || uncertain;
    reloadButton.disabled = busy || !loaded || !dirty || uncertain;
    refreshButton.disabled = !bridge || statusBusy;
    reconcileButton.hidden = !uncertain;
    reconcileButton.disabled = busy;
    document.getElementById('retry-load').disabled = busy;
    document.getElementById('confirm-discard').disabled = busy || uncertain;
    form.setAttribute('aria-busy', String(busy));
    saveButton.textContent = operation === 'saving' ? '保存中…' : '保存配置';
    testButton.textContent = operation === 'testing' ? '测试中…' : '保存并测试';
    document.getElementById('dirty-badge').hidden = !dirty;
    setText('save-state', busy ? (operation === 'testing' ? '正在测试已保存配置' : operation === 'saving' ? '正在保存，请完成宿主验证' : '正在读取配置') :
      uncertain ? '保存结果待核对' : !loaded ? '配置尚未加载' : dirty ? '当前修改尚未生效' : '与已保存配置一致');
  }

  function setBusy(value, name) {
    busy = value;
    operation = value ? name : '';
    updateControls();
  }

  function accountsToText(accounts) {
    return Object.keys(accounts || {}).sort().map(function (id) {
      var rule = accounts[id];
      return id + ' = ' + (rule.enabled ? 'on' : 'off') + ', ' + (typeof rule.sample_ratio === 'number' ? rule.sample_ratio : 1);
    }).join('\n');
  }

  function textToAccounts(text) {
    var accounts = {};
    String(text || '').split('\n').forEach(function (raw, index) {
      var line = raw.split('#')[0].trim();
      if (!line) return;
      var parts = line.split('=');
      var prefix = '第 ' + (index + 1) + ' 行：';
      if (parts.length !== 2) throw new Error(prefix + '请使用「账号 ID = on/off, 采样率」。');
      var id = parts[0].trim();
      if (!/^\d+$/.test(id)) throw new Error(prefix + '账号 ID 必须是十进制数字。');
      if (Object.prototype.hasOwnProperty.call(accounts, id)) throw new Error(prefix + '账号 ID 重复，请合并为一条规则。');
      var tail = parts[1].split(',');
      if (tail.length > 2) throw new Error(prefix + '每条规则只能包含一个采样率。');
      var state = tail[0].trim().toLowerCase();
      if (state !== 'on' && state !== 'off') throw new Error(prefix + '启用状态应为 on 或 off。');
      var ratio = tail.length < 2 || !tail[1].trim() ? 1 : Number(tail[1].trim());
      if (!Number.isFinite(ratio) || ratio < 0 || ratio > 1) throw new Error(prefix + '采样率必须在 0 到 1 之间。');
      accounts[id] = { enabled: state === 'on', sample_ratio: ratio };
    });
    return accounts;
  }

  function valuesFromConfig(config) {
    return {
      enabled: !!config.enabled,
      default_enabled: !!config.default_enabled,
      base_url: config.iq_api.base_url || '',
      api_key: config.iq_api.api_key || '',
      model: config.iq_api.model || '',
      timeout_seconds: String(config.iq_api.timeout_seconds),
      max_retries: String(config.iq_api.max_retries),
      check_host: !!config.loop_detection.check_host,
      accounts: accountsToText(config.accounts),
      worker_count: String(config.queue.worker_count),
      max_size: String(config.queue.max_size),
      drop_when_full: !!config.queue.drop_when_full,
      max_request_body_bytes: String(config.capture.max_request_body_bytes),
      max_response_body_bytes: String(config.capture.max_response_body_bytes),
      max_records_per_account: String(config.capture.max_records_per_account),
      record_ttl_hours: String(config.capture.record_ttl_hours)
    };
  }

  function snapshot(values) {
    return JSON.stringify(controls.map(function (control) {
      return [control.name, values ? values[control.name] : control.type === 'checkbox' ? control.checked : control.value];
    }));
  }

  function remember(config) {
    var values = valuesFromConfig(config);
    savedConfig = config;
    savedSnapshot = snapshot(values);
    loaded = true;
    return values;
  }

  function fill(config) {
    var values = remember(config);
    controls.forEach(function (control) {
      if (control.type === 'checkbox') control.checked = values[control.name];
      else control.value = values[control.name];
    });
    clearErrors();
    discardConfirm.hidden = true;
    updateDraft();
  }

  function updateDraft() {
    dirty = loaded && snapshot() !== savedSnapshot;
    field('base_url').required = field('enabled').checked;
    field('model').required = field('enabled').checked;
    var badge = document.getElementById('scope-badge');
    badge.textContent = field('enabled').checked ? '草稿：开启' : '草稿：关闭';
    badge.className = 'badge ' + (field('enabled').checked ? 'success' : 'neutral');
    var summary;
    if (!field('enabled').checked) summary = '当前草稿将关闭评测，账号规则会保留。';
    else {
      try {
        var accounts = textToAccounts(field('accounts').value);
        var active = Object.keys(accounts).filter(function (id) { return accounts[id].enabled && accounts[id].sample_ratio > 0; }).length;
        summary = field('default_enabled').checked ? '未列入规则的账号也会全量评测，请留意接口用量与费用。' :
          active ? '仅评测 ' + active + ' 个明确启用且采样率大于 0 的账号，按各自采样率执行。' : '尚无可评测账号：请添加启用规则，或开启未列入规则账号的评测。';
      } catch (error) { summary = '账号规则需要检查后才能保存。'; }
    }
    setText('scope-summary', summary);
    document.getElementById('scope-summary').className = 'callout' + (field('enabled').checked && field('default_enabled').checked ? ' warning' : '');
    document.getElementById('host-warning').hidden = field('check_host').checked;
    updateControls();
  }

  var FIELD_LABELS = {
    base_url: '接口根地址', model: '评分模型', accounts: '账号规则', timeout_seconds: '请求超时',
    max_retries: '失败重试次数', worker_count: '并发评分数', max_size: '队列容量',
    max_request_body_bytes: '请求体采集上限', max_response_body_bytes: '响应体采集上限',
    max_records_per_account: '每账号记录上限', record_ttl_hours: '记录过期时间'
  };

  function clearErrors() {
    controls.forEach(function (control) {
      control.removeAttribute('aria-invalid');
      var error = document.getElementById(control.name + '-error');
      if (error) { error.textContent = ''; error.hidden = true; }
    });
    document.getElementById('error-list').replaceChildren();
    errorSummary.hidden = true;
  }

  function validate(focus) {
    var errors = [];
    var accounts;
    var url = field('base_url').value.trim();
    if (field('enabled').checked && !url) errors.push(['base_url', '启用评测时，请填写接口根地址。']);
    else if (url) {
      try {
        var parsed = new URL(url);
        if (!/^https?:$/.test(parsed.protocol) || !parsed.host) throw new Error();
      } catch (error) { errors.push(['base_url', '请输入以 http:// 或 https:// 开头的完整地址。']); }
    }
    if (field('enabled').checked && !field('model').value.trim()) errors.push(['model', '启用评测时，请填写评分模型。']);
    controls.filter(function (control) { return control.type === 'number'; }).forEach(function (control) {
      var number = Number(control.value);
      if (!control.value.trim() || !Number.isInteger(number) || number < Number(control.min) || number > Number(control.max)) {
        errors.push([control.name, '请输入 ' + control.min + '–' + control.max + ' 之间的整数。']);
      }
    });
    try { accounts = textToAccounts(field('accounts').value); }
    catch (error) { errors.push(['accounts', error.message]); }
    clearErrors();
    errors.forEach(function (error) {
      var control = field(error[0]);
      control.setAttribute('aria-invalid', 'true');
      var inline = document.getElementById(error[0] + '-error');
      inline.textContent = error[1];
      inline.hidden = false;
      var li = document.createElement('li');
      var link = document.createElement('a');
      link.href = '#' + control.id;
      link.textContent = FIELD_LABELS[error[0]] + '：' + error[1];
      link.addEventListener('click', function (event) {
        event.preventDefault();
        var details = control.closest('details');
        if (details) details.open = true;
        control.focus();
      });
      li.appendChild(link);
      document.getElementById('error-list').appendChild(li);
      if (focus && control.closest('details')) control.closest('details').open = true;
    });
    errorSummary.hidden = !errors.length;
    if (errors.length && focus) errorSummary.focus();
    return errors.length ? null : collect(accounts);
  }

  function collect(accounts) {
    return {
      enabled: field('enabled').checked,
      default_enabled: field('default_enabled').checked,
      accounts: accounts,
      iq_api: {
        base_url: field('base_url').value.trim(), api_key: field('api_key').value.trim(), model: field('model').value.trim(),
        timeout_seconds: Number(field('timeout_seconds').value), max_retries: Number(field('max_retries').value)
      },
      // 防循环按 host 判断；check_model 为协议保留字段。
      loop_detection: { enabled: true, check_host: field('check_host').checked, check_model: false },
      queue: { worker_count: Number(field('worker_count').value), max_size: Number(field('max_size').value), drop_when_full: field('drop_when_full').checked },
      capture: {
        max_request_body_bytes: Number(field('max_request_body_bytes').value), max_response_body_bytes: Number(field('max_response_body_bytes').value),
        max_records_per_account: Number(field('max_records_per_account').value), record_ttl_hours: Number(field('record_ttl_hours').value)
      }
    };
  }

  var STATUS_LABELS = {
    configured: '配置已应用', enabled: '评测已启用', dropped: '丢弃任务', loop_skips: '防循环跳过',
    host_services: '宿主服务已连接', transports: '连接池条目', uptime_seconds: '运行时长', version: '插件版本', iq_api_model: '评分模型'
  };

  function renderStatus(result) {
    var detail = bridge.parseStatusJSON(result.status_json);
    if (!detail || typeof detail !== 'object' || Array.isArray(detail)) detail = {};
    var badge = document.getElementById('health-badge');
    badge.textContent = result.healthy ? '插件健康' : '插件状态异常';
    badge.className = 'badge ' + (result.healthy ? 'success' : 'error');
    setText('status-note', !result.healthy ? '插件报告异常，请在宿主插件管理中检查。' : detail.configured === false ? '尚未应用配置，请先保存评测配置。' :
      detail.enabled === true ? '评测已开启 · 页面可见时每 10 秒自动更新' : detail.enabled === false ? '评测已关闭 · 当前仅转发请求' : '已连接宿主 · 页面可见时每 10 秒自动更新');
    ['requests', 'evaluated', 'queued', 'failures'].forEach(function (key) {
      setText('metric-' + key, typeof detail[key] === 'number' && Number.isFinite(detail[key]) ? detail[key].toLocaleString('zh-CN') : '—');
    });
    var list = document.getElementById('status-list');
    list.replaceChildren();
    Object.keys(STATUS_LABELS).forEach(function (key) {
      if (detail[key] === undefined) return;
      var value = detail[key];
      if (typeof value === 'boolean') value = value ? '是' : '否';
      else if (key === 'uptime_seconds' && typeof value === 'number') {
        value = Math.floor(value / 3600) + ' 小时 ' + Math.floor(value % 3600 / 60) + ' 分钟';
      }
      var dt = document.createElement('dt');
      var dd = document.createElement('dd');
      dt.textContent = STATUS_LABELS[key];
      dd.textContent = value === '' || value === null ? '—' : String(value);
      list.append(dt, dd);
    });
    if (!list.children.length) {
      var dt = document.createElement('dt'); var dd = document.createElement('dd');
      dt.textContent = '详情'; dd.textContent = '宿主未返回详细指标'; list.append(dt, dd);
    }
    statusTime = new Date().toLocaleTimeString('zh-CN', { hour12: false });
    setText('status-updated', '更新于 ' + statusTime);
  }

  async function refreshStatus() {
    if (!bridge || statusBusy) return;
    statusBusy = true;
    refreshButton.textContent = '刷新中…';
    updateControls();
    try { renderStatus(await bridge.status()); }
    catch (error) {
      var badge = document.getElementById('health-badge');
      badge.textContent = '状态读取失败'; badge.className = 'badge warning';
      setText('status-note', statusTime ? '暂时无法更新，以下为上次读取的数据。可点击刷新重试。' : '无法读取状态，请检查宿主连接后重试。');
      setText('status-updated', statusTime ? '数据已过期 · 上次更新 ' + statusTime : '尚未更新');
    } finally { statusBusy = false; refreshButton.textContent = '刷新状态'; updateControls(); }
  }

  async function load() {
    if (busy) return;
    setBusy(true, 'loading');
    document.getElementById('retry-load').hidden = true;
    setText('connection-text', '正在加载已保存的配置…');
    try {
      fill(await bridge.loadConfig());
      document.getElementById('connection-message').hidden = true;
    } catch (error) {
      setText('connection-text', '配置读取失败，请检查宿主连接后重试。');
      document.getElementById('retry-load').hidden = false;
    } finally { setBusy(false); }
  }

  async function reconcile() {
    try {
      remember(await bridge.loadConfig());
      uncertain = false;
      updateDraft();
      showMessage('warning', dirty ? '已核对宿主配置，当前草稿仍保留且与已保存配置不同。请检查后再保存。' : '已核对宿主配置，当前表单与已保存配置一致。如需诊断，请再次点击「保存并测试」。', true);
    } catch (error) {
      showMessage('warning', '保存结果尚未确认，草稿已保留。请点击「重新核对保存结果」，确认后再继续保存或测试。', true);
    }
  }

  async function save(andTest) {
    if (busy || !loaded || uncertain) return;
    var config = validate(true);
    if (!config) { showMessage('', ''); return; }
    discardConfirm.hidden = true;
    var phase = 'saving';
    setBusy(true, phase);
    showMessage('', '正在保存，请完成宿主可能要求的二次验证…');
    try {
      fill(await bridge.saveConfig(config));
      showMessage('success', '配置已保存并应用。');
      if (andTest) {
        phase = 'testing';
        setBusy(true, phase);
        showMessage('', '配置已保存，正在测试评分接口…');
        var result = await bridge.testConfig();
        var latency = typeof result.latency_ms === 'number' && Number.isFinite(result.latency_ms) ? '（' + result.latency_ms + ' ms）' : '';
        // 诊断反馈不回显接口、密钥或宿主返回的原始错误。
        showMessage(result.success ? 'success' : 'error', result.success ? '配置已保存，评分接口测试通过' + latency + '。' : '配置已保存，但接口测试失败。请检查接口地址、密钥、模型和宿主网络。', true);
      }
      await refreshStatus();
    } catch (error) {
      if (phase === 'saving' && error instanceof Sub2ApiPluginBridge.BridgeTimeoutError) {
        uncertain = true;
        showMessage('warning', '保存响应超时，正在核对宿主配置。当前草稿会保留。');
        await reconcile();
      } else {
        showMessage('error', phase === 'testing' ? '配置已保存，但测试未完成。请检查宿主验证或连接后重试。' : '保存失败，当前修改已保留。请检查宿主提示及配置后重试。', true);
      }
    } finally { setBusy(false); }
  }

  form.addEventListener('input', function () {
    updateDraft();
    discardConfirm.hidden = true;
    if (!errorSummary.hidden) validate(false);
    if (!uncertain) showMessage('', '');
  });
  form.addEventListener('submit', function (event) { event.preventDefault(); save(false); });
  // 宿主 iframe 不授予 allow-forms；保存使用 Bridge，Enter 也走同一入口。
  form.addEventListener('keydown', function (event) {
    if (event.key === 'Enter' && !event.isComposing && event.target.matches('input:not([type="checkbox"])')) {
      event.preventDefault();
      if (!saveButton.disabled) save(false);
    }
  });
  saveButton.addEventListener('click', function () { save(false); });
  testButton.addEventListener('click', function () { save(true); });
  reloadButton.addEventListener('click', function () {
    if (busy || !dirty) return;
    discardConfirm.hidden = false;
    document.getElementById('cancel-discard').focus();
  });
  document.getElementById('cancel-discard').addEventListener('click', function () { discardConfirm.hidden = true; reloadButton.focus(); });
  document.getElementById('confirm-discard').addEventListener('click', function () {
    if (busy || uncertain) return;
    fill(savedConfig);
    showMessage('', '已恢复到已保存配置。');
    field('enabled').focus();
  });
  reconcileButton.addEventListener('click', async function () {
    if (busy) return;
    setBusy(true, 'loading');
    await reconcile();
    setBusy(false);
  });
  document.getElementById('retry-load').addEventListener('click', load);
  refreshButton.addEventListener('click', refreshStatus);

  try { bridge = Sub2ApiPluginBridge.create(); }
  catch (error) {
    setText('health-badge', '未连接宿主');
    setText('status-note', '请从 Sub2API 插件管理页面打开。');
    setText('connection-text', '此页面需要宿主连接，无法独立读取或保存配置。');
    updateControls();
    return;
  }
  var stopResize = bridge.autoResize(document.body);
  var poll = window.setInterval(function () {
    if (document.visibilityState === 'visible' && !busy) refreshStatus();
  }, 10000);
  window.addEventListener('pagehide', function () {
    window.clearInterval(poll);
    stopResize();
    bridge.dispose();
  });
  bridge.ready();
  load();
  refreshStatus();
})();
