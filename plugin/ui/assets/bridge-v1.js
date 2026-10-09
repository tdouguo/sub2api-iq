/*
 * Sub2API 插件 UI Bridge v1 客户端。
 *
 * 宿主把插件 ui/index.html 装进 sandbox="allow-scripts" 的 iframe（不透明来源），
 * URL fragment 携带 bridge_token。UI 与宿主之间只通过 postMessage 通信：
 *
 *   UI  -> 宿主: { source: "sub2api-plugin-ui",  bridge_token, type, request_id, ...payload }
 *   宿主 -> UI:  { source: "sub2api-plugin-host", bridge_token, type: "<type>.result", request_id, ok, config | result | error }
 *
 * 宿主对每个待答请求保留 30 秒；超时后到达的应答会被静默丢弃，因此本客户端默认 25 秒超时，
 * 且 config.save 超时后应重新 loadConfig 以核对真实状态（二次验证弹窗可能拖长耗时）。
 *
 * 无任何外部依赖，可直接 <script src="assets/bridge-v1.js"></script> 引入，
 * 暴露 window.Sub2ApiPluginBridge。
 */
(function (global) {
  'use strict';

  var HOST_SOURCE = 'sub2api-plugin-host';
  var UI_SOURCE = 'sub2api-plugin-ui';
  var DEFAULT_TIMEOUT_MS = 25000;
  var VERSION = 1;

  function BridgeError(message, data) {
    this.name = 'BridgeError';
    this.message = message || 'bridge request failed';
    this.data = data || null;
  }
  BridgeError.prototype = Object.create(Error.prototype);
  BridgeError.prototype.constructor = BridgeError;

  function BridgeTimeoutError(type) {
    this.name = 'BridgeTimeoutError';
    this.type = type;
    this.message = 'bridge request timed out: ' + type;
  }
  BridgeTimeoutError.prototype = Object.create(Error.prototype);
  BridgeTimeoutError.prototype.constructor = BridgeTimeoutError;

  function readTokenFromFragment() {
    var hash = global.location && global.location.hash ? global.location.hash.slice(1) : '';
    if (!hash) return '';
    var parts = hash.split('&');
    for (var i = 0; i < parts.length; i++) {
      var pair = parts[i].split('=');
      if (decodeURIComponent(pair[0]) === 'bridge_token') {
        return decodeURIComponent(pair.slice(1).join('='));
      }
    }
    return '';
  }

  function nextRequestID(seq) {
    return Date.now().toString(36) + '-' + seq.toString(36) + '-' + Math.random().toString(36).slice(2, 10);
  }

  /**
   * 创建 Bridge。options.bridgeToken 可覆盖从 fragment 读取的 Token；
   * options.timeoutMs 覆盖默认超时。
   */
  function create(options) {
    options = options || {};
    var token = options.bridgeToken || readTokenFromFragment();
    if (!token) {
      throw new Error('bridge_token missing from URL fragment');
    }
    var timeoutMs = options.timeoutMs || DEFAULT_TIMEOUT_MS;
    var pending = new Map();
    var disposed = false;
    var seq = 0;

    function onMessage(event) {
      if (disposed) return;
      if (event.source !== global.parent) return;
      var data = event.data;
      if (!data || typeof data !== 'object') return;
      if (data.source !== HOST_SOURCE || data.bridge_token !== token) return;
      if (typeof data.request_id !== 'string') return;
      var entry = pending.get(data.request_id);
      if (!entry) return;
      pending.delete(data.request_id);
      global.clearTimeout(entry.timer);
      // config.test 在 result.success=false 时 ok 也为 false，但仍携带 result，交给调用方判断。
      if (data.ok || data.result !== undefined) {
        entry.resolve(data);
      } else {
        entry.reject(new BridgeError(typeof data.error === 'string' ? data.error : 'request failed', data));
      }
    }

    function post(message) {
      var envelope = { source: UI_SOURCE, bridge_token: token };
      for (var key in message) {
        if (Object.prototype.hasOwnProperty.call(message, key)) envelope[key] = message[key];
      }
      // iframe 来源不透明，宿主同样以 "*" 发送并靠 Token + 来源窗口校验。
      global.parent.postMessage(envelope, '*');
    }

    function request(type, payload, customTimeout) {
      return new Promise(function (resolve, reject) {
        if (disposed) {
          reject(new Error('bridge disposed'));
          return;
        }
        var requestID = nextRequestID(++seq);
        var timer = global.setTimeout(function () {
          pending.delete(requestID);
          reject(new BridgeTimeoutError(type));
        }, customTimeout || timeoutMs);
        pending.set(requestID, { resolve: resolve, reject: reject, timer: timer });
        var message = { type: type, request_id: requestID };
        if (payload) {
          for (var key in payload) {
            if (Object.prototype.hasOwnProperty.call(payload, key)) message[key] = payload[key];
          }
        }
        post(message);
      });
    }

    global.addEventListener('message', onMessage);

    var bridge = {
      version: VERSION,
      token: token,
      /** 通知宿主页面已就绪（宿主据此隐藏加载遮罩）。 */
      ready: function () { post({ type: 'sub2api.plugin.ready' }); },
      /** 读取已保存配置，返回对象。 */
      loadConfig: function () { return request('config.load').then(function (r) { return r.config; }); },
      /** 保存配置（宿主会先让插件校验并应用），返回规范化后的配置。需要管理员二次验证。 */
      saveConfig: function (config, customTimeout) {
        return request('config.save', { config: config }, customTimeout).then(function (r) { return r.config; });
      },
      /** 测试已保存的配置，返回 { success, message, latency_ms, status_json }。需要管理员二次验证。 */
      testConfig: function (customTimeout) {
        return request('config.test', null, customTimeout).then(function (r) { return r.result; });
      },
      /** 读取运行时状态（Health），返回 { healthy, message, status_json }。只读，无需二次验证。 */
      status: function () { return request('plugin.status').then(function (r) { return r.result; }); },
      /** 调整 iframe 高度（宿主限制在 520–960 之间）。 */
      resize: function (height) { post({ type: 'ui.resize', height: height }); },
      /** 显示宿主通知；level 取 "success" | "error" | "info"。 */
      notify: function (level, message) { post({ type: 'ui.notify', level: level, message: String(message).slice(0, 500) }); },
      /** 解析 status_json / status 结果中的 JSON 字符串；失败返回 null。 */
      parseStatusJSON: function (raw) {
        if (!raw || typeof raw !== 'string') return null;
        try { return JSON.parse(raw); } catch (e) { return null; }
      },
      /** 页面卸载时调用，拒绝所有待答请求并移除监听。 */
      dispose: function () {
        if (disposed) return;
        disposed = true;
        global.removeEventListener('message', onMessage);
        pending.forEach(function (entry) {
          global.clearTimeout(entry.timer);
          entry.reject(new Error('bridge disposed'));
        });
        pending.clear();
      }
    };

    /**
     * 根据 document 高度自动调用 resize。返回停止函数。
     */
    bridge.autoResize = function (element) {
      var target = element || global.document.documentElement;
      var lastHeight = 0;
      function measure() {
        var height = Math.ceil(target.scrollHeight || target.getBoundingClientRect().height);
        if (height && height !== lastHeight) {
          lastHeight = height;
          bridge.resize(height + 16);
        }
      }
      measure();
      var observer = null;
      if (typeof global.ResizeObserver === 'function') {
        observer = new global.ResizeObserver(measure);
        observer.observe(target);
      }
      var interval = global.setInterval(measure, 1000);
      return function stop() {
        if (observer) observer.disconnect();
        global.clearInterval(interval);
      };
    };

    return bridge;
  }

  global.Sub2ApiPluginBridge = {
    VERSION: VERSION,
    create: create,
    BridgeError: BridgeError,
    BridgeTimeoutError: BridgeTimeoutError
  };
})(window);
