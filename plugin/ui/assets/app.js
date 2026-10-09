/* 插件配置页逻辑：通过官方 UI Bridge 读写配置、保存后测试、轮询只读状态。 */
(function () {
  'use strict';

  var bridge = Sub2ApiPluginBridge.create();
  var form = document.getElementById('config-form');
  var message = document.getElementById('message');
  var dirtyBadge = document.getElementById('dirty-badge');
  var statusList = document.getElementById('status-list');
  var busy = false;

  function showMessage(kind, text) {
    message.textContent = text;
    message.className = 'message' + (kind ? ' ' + kind : '');
    message.hidden = !text;
  }

  function setBusy(value) {
    busy = value;
    Array.prototype.forEach.call(form.querySelectorAll('button'), function (button) {
      button.disabled = value;
    });
  }

  /* 账号规则在界面上用「每行一条」的文本表示，比 JSON 直观得多：42 = on, 1.0 */
  function accountsToText(accounts) {
    var keys = Object.keys(accounts || {}).sort();
    return keys.map(function (key) {
      var rule = accounts[key] || {};
      var ratio = typeof rule.sample_ratio === 'number' ? rule.sample_ratio : 1;
      return key + ' = ' + (rule.enabled ? 'on' : 'off') + ', ' + ratio;
    }).join('\n');
  }

  function textToAccounts(text) {
    var accounts = {};
    String(text || '').split('\n').forEach(function (line) {
      line = line.trim();
      if (!line || line.charAt(0) === '#') return;
      var parts = line.split('=');
      if (parts.length !== 2) throw new Error('账号规则格式应为 "account_id = on|off, 采样率"：' + line);
      var id = parts[0].trim();
      if (!/^\d+$/.test(id)) throw new Error('账号 ID 必须是数字：' + line);
      var tail = parts[1].split(',');
      var state = tail[0].trim().toLowerCase();
      if (state !== 'on' && state !== 'off') throw new Error('启用状态应为 on 或 off：' + line);
      var ratio = 1;
      if (tail.length > 1 && tail[1].trim() !== '') {
        ratio = Number(tail[1].trim());
        if (isNaN(ratio) || ratio < 0 || ratio > 1) throw new Error('采样率必须在 0 到 1 之间：' + line);
      }
      accounts[id] = { enabled: state === 'on', sample_ratio: ratio };
    });
    return accounts;
  }

  function fill(config) {
    form.enabled.checked = !!config.enabled;
    form.default_enabled.checked = !!config.default_enabled;
    form.base_url.value = config.iq_api.base_url || '';
    form.api_key.value = config.iq_api.api_key || '';
    form.model.value = config.iq_api.model || '';
    form.timeout_seconds.value = config.iq_api.timeout_seconds;
    form.max_retries.value = config.iq_api.max_retries;
    form.check_host.checked = !!config.loop_detection.check_host;
    form.accounts.value = accountsToText(config.accounts);
    form.worker_count.value = config.queue.worker_count;
    form.max_size.value = config.queue.max_size;
    form.drop_when_full.checked = !!config.queue.drop_when_full;
    form.max_request_body_bytes.value = config.capture.max_request_body_bytes;
    form.max_response_body_bytes.value = config.capture.max_response_body_bytes;
    form.max_records_per_account.value = config.capture.max_records_per_account;
    form.record_ttl_hours.value = config.capture.record_ttl_hours;
    dirtyBadge.hidden = true;
  }

  function collect() {
    return {
      enabled: form.enabled.checked,
      default_enabled: form.default_enabled.checked,
      accounts: textToAccounts(form.accounts.value),
      iq_api: {
        base_url: form.base_url.value.trim(),
        api_key: form.api_key.value.trim(),
        model: form.model.value.trim(),
        timeout_seconds: Number(form.timeout_seconds.value),
        max_retries: Number(form.max_retries.value)
      },
      loop_detection: {
        // check_model 恒为 false：比对 model 需要预读请求体，
        // 那会让宿主把「拨号失败」误判为已发出而放弃换号重试。
        enabled: true,
        check_host: form.check_host.checked,
        check_model: false
      },
      queue: {
        worker_count: Number(form.worker_count.value),
        max_size: Number(form.max_size.value),
        drop_when_full: form.drop_when_full.checked
      },
      capture: {
        max_request_body_bytes: Number(form.max_request_body_bytes.value),
        max_response_body_bytes: Number(form.max_response_body_bytes.value),
        max_records_per_account: Number(form.max_records_per_account.value),
        record_ttl_hours: Number(form.record_ttl_hours.value)
      }
    };
  }

  var STATUS_LABELS = {
    configured: '已配置',
    enabled: '评测开关',
    requests: '转发请求数',
    evaluated: '完成评测数',
    failures: '失败数',
    dropped: '丢弃任务数',
    loop_skips: '防循环跳过数',
    queued: '队列排队数',
    host_services: '宿主服务',
    transports: '连接池条目',
    uptime_seconds: '运行时长（秒）',
    version: '插件版本',
    iq_api_base_url: '评测接口',
    iq_api_model: '评分模型'
  };

  function renderStatus(result) {
    var rows = [['健康', result.healthy ? '正常' : '异常'], ['消息', result.message || '-']];
    var detail = bridge.parseStatusJSON(result.status_json);
    if (detail) {
      Object.keys(detail).forEach(function (key) {
        rows.push([STATUS_LABELS[key] || key, String(detail[key])]);
      });
    }
    statusList.innerHTML = '';
    rows.forEach(function (row) {
      var dt = document.createElement('dt');
      var dd = document.createElement('dd');
      dt.textContent = row[0];
      dd.textContent = row[1];
      statusList.appendChild(dt);
      statusList.appendChild(dd);
    });
  }

  function refreshStatus() {
    return bridge.status().then(renderStatus).catch(function (error) {
      renderStatus({ healthy: false, message: '状态读取失败：' + error.message });
    });
  }

  function load() {
    return bridge.loadConfig().then(function (config) {
      fill(config);
      showMessage('', '');
    }).catch(function (error) {
      showMessage('error', '读取配置失败：' + error.message);
    });
  }

  function save() {
    var config;
    try {
      config = collect();
    } catch (error) {
      showMessage('error', error.message);
      return Promise.reject(error);
    }
    setBusy(true);
    showMessage('', '保存中…');
    return bridge.saveConfig(config).then(function (normalized) {
      fill(normalized);
      showMessage('success', '已保存并应用。');
    }).catch(function (error) {
      if (error instanceof Sub2ApiPluginBridge.BridgeTimeoutError) {
        showMessage('error', '保存结果未在限时内返回（可能在等待二次验证）。已重新读取当前配置，请核对后再决定是否重试。');
        return load().then(function () { throw error; });
      }
      showMessage('error', '保存失败：' + error.message);
      throw error;
    }).finally(function () { setBusy(false); });
  }

  function test() {
    setBusy(true);
    showMessage('', '测试中…');
    return bridge.testConfig().then(function (result) {
      var text = (result.success ? '测试通过' : '测试失败') + '：' + (result.message || '') +
        (result.latency_ms ? '（' + result.latency_ms + ' ms）' : '');
      showMessage(result.success ? 'success' : 'error', text);
      bridge.notify(result.success ? 'success' : 'error', text);
      return refreshStatus();
    }).catch(function (error) {
      showMessage('error', '测试失败：' + error.message);
    }).finally(function () { setBusy(false); });
  }

  form.addEventListener('input', function () { dirtyBadge.hidden = false; });
  form.addEventListener('submit', function (event) {
    event.preventDefault();
    if (!busy) save().catch(function () {});
  });
  document.getElementById('save-test').addEventListener('click', function () {
    if (!busy) save().then(test).catch(function () {});
  });
  document.getElementById('reload').addEventListener('click', function () {
    if (!busy) load();
  });
  document.getElementById('refresh-status').addEventListener('click', function () {
    if (!busy) refreshStatus();
  });

  var stopResize = bridge.autoResize(document.body);
  var poll = window.setInterval(function () {
    if (document.visibilityState === 'visible' && !busy) refreshStatus();
  }, 10000);
  window.addEventListener('beforeunload', function () {
    window.clearInterval(poll);
    stopResize();
    bridge.dispose();
  });

  bridge.ready();
  load().then(refreshStatus);
})();
