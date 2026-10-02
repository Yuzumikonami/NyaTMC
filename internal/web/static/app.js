// NyaTMC Web 仪表盘前端。
// 纯原生 JS：不依赖任何 CDN、npm 包或构建链，图表用 canvas 手绘。
(function () {
  'use strict';

  // ---------------------------------------------------------------- 基础

  var qs = new URLSearchParams(location.search);
  var TOKEN = (qs.get('token') || '');
  try {
    if (TOKEN) {
      localStorage.setItem('nyatmc.token', TOKEN);
    } else {
      TOKEN = localStorage.getItem('nyatmc.token') || '';
    }
  } catch (e) { /* 隐私模式下 localStorage 不可用，忽略 */ }
  TOKEN = (TOKEN || '').trim();

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = String(text);
    return n;
  }
  function yn(v) {
    if (v === true) return '是';
    if (v === false) return '否';
    return '默认';
  }
  function dur(v) {
    if (typeof v !== 'number' || !isFinite(v) || v <= 0) return '—';
    var s = v / 1e9;
    if (s >= 86400) return (s / 86400).toFixed(1) + ' 天';
    if (s >= 3600) return (s / 3600).toFixed(1) + ' 小时';
    if (s >= 60) return (s / 60).toFixed(1) + ' 分钟';
    return s.toFixed(0) + ' 秒';
  }
  function fmtMB(v) {
    if (typeof v !== 'number' || v <= 0) return '—';
    if (v >= 1024) return (v / 1024).toFixed(2) + ' GB';
    return v + ' MB';
  }

  var state = {
    instances: [],
    current: '',
    status: null,
    writeEnabled: true,
    tokenRequired: false,
    refreshMs: 3000,
    tick: 0,
    tps: [],
    mem: [],
    logs: [],
    logPaused: false,
    logTimer: null,
    es: null,
    esTimer: null,
    esRetry: 1000,
    configLoaded: false,
    configRaw: ''
  };

  // ---------------------------------------------------------------- 提示条

  function toast(message, kind) {
    var area = $('toast-area');
    if (!area) return;
    var node = el('div', 'toast' + (kind ? ' ' + kind : ''), message);
    area.appendChild(node);
    setTimeout(function () {
      node.style.opacity = '0';
      node.style.transition = 'opacity 300ms';
      setTimeout(function () { if (node.parentNode) node.parentNode.removeChild(node); }, 320);
    }, kind === 'err' ? 7000 : 3600);
  }

  // ---------------------------------------------------------------- 接口调用

  function api(path, options) {
    var opts = options || {};
    var init = { method: opts.method || 'GET', headers: { 'Accept': 'application/json' } };
    if (TOKEN) init.headers['Authorization'] = 'Bearer ' + TOKEN;
    if (opts.body !== undefined && opts.body !== null) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(opts.body);
    }
    return fetch(path, init).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        if (text) { try { data = JSON.parse(text); } catch (e) { data = null; } }
        if (!res.ok || !data || data.ok === false) {
          var msg = (data && data.error) ? data.error : ('HTTP ' + res.status + ' ' + res.statusText);
          var err = new Error(msg);
          err.status = res.status;
          throw err;
        }
        return data;
      });
    });
  }

  function errText(err) {
    if (!err) return '未知错误';
    if (err.status === 401) return err.message + '\n（提示：可在页面地址后加 ?token=你的令牌，或用 nyatmc web serve --token 指定）';
    return err.message;
  }

  // ---------------------------------------------------------------- 实例切换

  function updateInstanceSelect() {
    var sel = $('instance-select');
    if (!sel) return;
    var names = state.instances.map(function (i) { return i.name; });
    var want = state.current || (names.length ? names[0] : '');
    var same = sel.options.length === names.length;
    if (same) {
      for (var i = 0; i < names.length; i++) {
        if (sel.options[i].value !== names[i]) { same = false; break; }
      }
    }
    if (!same) {
      sel.textContent = '';
      names.forEach(function (n) {
        var o = el('option', null, n);
        o.value = n;
        sel.appendChild(o);
      });
    }
    if (want && names.indexOf(want) >= 0 && sel.value !== want) sel.value = want;
    var schSel = $('scheduler-instance');
    if (schSel) {
      var prev = schSel.value;
      schSel.textContent = '';
      var any = el('option', null, '（默认/全部实例）');
      any.value = '';
      schSel.appendChild(any);
      names.forEach(function (n) {
        var o = el('option', null, n);
        o.value = n;
        schSel.appendChild(o);
      });
      if (prev && names.indexOf(prev) >= 0) schSel.value = prev;
    }
    if (!names.length && !state.current) {
      setBadge('unknown', '没有实例');
      hint('当前没有任何实例，先用 nyatmc create <名字> 新建。');
    }
  }

  // ---------------------------------------------------------------- 状态渲染

  function setBadge(kind, text) {
    var b = $('state-badge');
    if (!b) return;
    b.className = 'badge badge-' + (kind || 'unknown');
    b.textContent = text || '未知';
  }

  function hint(text) {
    var h = $('control-hint');
    if (h) h.textContent = text || '';
  }

  function stateKind(s) {
    if (s === 'running') return 'running';
    if (s === 'starting' || s === 'restarting') return 'starting';
    if (s === 'stopping') return 'stopping';
    if (s === 'crashed') return 'crashed';
    if (s === 'stopped') return 'stopped';
    return 'unknown';
  }

  function statCard(key, value, cls, wide) {
    var box = el('div', 'stat' + (wide ? ' wide' : ''));
    box.appendChild(el('span', 'k', key));
    box.appendChild(el('span', 'v' + (cls ? ' ' + cls : ''), value));
    return box;
  }

  function renderStats(st) {
    var grid = $('stats-grid');
    if (!grid || !st) return;
    grid.textContent = '';
    var kind = stateKind(st.state);
    var tps = st.tps_known ? Number(st.tps).toFixed(2) : '未知';
    var mem = st.memory_used_mb > 0
      ? fmtMB(st.memory_used_mb) + (st.memory_limit ? ' / ' + st.memory_limit : '')
      : (st.memory_limit || '—');
    var players = (st.players_now || 0) + (st.max_players ? ' / ' + st.max_players : '');
    var sup = st.supervisor_alive
      ? ('PID ' + (st.supervisor_pid || '—'))
      : '未运行（状态来自落盘文件）';

    grid.appendChild(statCard('状态', st.state_label || st.state || '未知',
      kind === 'running' ? 'ok' : (kind === 'crashed' ? 'danger' : (kind === 'unknown' || kind === 'stopped' ? '' : 'info'))));
    grid.appendChild(statCard('服务端 PID', st.pid > 0 ? st.pid : '—', st.pid > 0 ? 'info' : ''));
    grid.appendChild(statCard('守护进程', sup, st.supervisor_alive ? '' : 'warn'));
    grid.appendChild(statCard('运行时长', st.uptime_human || '—'));
    grid.appendChild(statCard('版本', st.version || st.type || '—'));
    grid.appendChild(statCard('内存', mem));
    grid.appendChild(statCard('TPS', tps, st.tps_known && st.tps < 19 ? 'warn' : ''));
    grid.appendChild(statCard('在线人数', players));
    grid.appendChild(statCard('看门狗', st.watchdog ? '已开启' : '已关闭', st.watchdog ? '' : 'warn'));
    grid.appendChild(statCard('重启次数', st.restarts || 0, st.restarts > 0 ? 'warn' : ''));
    grid.appendChild(statCard('最近退出码', st.last_exit_code || 0, st.last_exit_code ? 'danger' : ''));
    grid.appendChild(statCard('类型 / 目录', (st.type || '—') + ' · ' + (st.directory || '—')));
    if (st.last_error) grid.appendChild(statCard('最近错误', st.last_error, 'danger', true));
    if (st.note) grid.appendChild(statCard('备注', st.note, 'warn', true));
    if (st.stale) grid.appendChild(statCard('提示', '状态来自落盘文件，守护进程未响应，可能滞后', 'warn', true));

    var up = $('overview-updated');
    if (up) up.textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN');
  }

  function renderPlayers(st) {
    var box = $('players-list');
    var cnt = $('players-count');
    if (!box) return;
    box.textContent = '';
    var list = (st && st.players) || [];
    if (cnt) cnt.textContent = list.length ? list.length + ' 人' : '0 人';
    if (!list.length) {
      box.appendChild(el('span', 'muted', (st && st.state === 'running') ? '当前没有玩家在线。' : '实例未运行，没有在线玩家。'));
      return;
    }
    list.forEach(function (p) { box.appendChild(el('span', 'player', p)); });
  }

  function renderControlState(st) {
    var write = state.writeEnabled;
    var running = !!(st && st.state && st.state !== 'stopped' && st.state !== 'crashed' && st.state !== 'unknown');
    var stopped = !!(st && (st.state === 'stopped' || st.state === 'crashed' || st.state === 'unknown'));
    var busy = !!(st && (st.state === 'starting' || st.state === 'stopping' || st.state === 'restarting'));

    function set(id, enabled) {
      var b = $(id);
      if (b) b.disabled = !enabled || !write;
    }
    set('btn-start', stopped && !busy);
    set('btn-stop', running);
    set('btn-restart', running && !busy);
    set('btn-kill', running);
    set('btn-cmd', running);
    set('btn-backup-create', !!state.current);
    set('btn-backup-prune', !!state.current);

    var cmd = $('cmd-input');
    if (cmd) cmd.disabled = !write;

    if (!write) {
      hint('web.enable_write = false：写操作已禁用');
    } else if (!state.current) {
      hint('请先选择实例');
    } else if (st && st.stale) {
      hint('守护进程未响应，状态可能不是最新');
    } else {
      hint('');
    }
  }

  function applyStatus(st) {
    if (!st) return;
    state.status = st;
    setBadge(stateKind(st.state), st.state_label || st.state || '未知');
    renderStats(st);
    renderPlayers(st);
    renderControlState(st);
    sample(st);
  }

  // ---------------------------------------------------------------- 图表

  function pushSample(arr, v) {
    arr.push(typeof v === 'number' && isFinite(v) ? v : null);
    while (arr.length > 60) arr.shift();
  }

  function sample(st) {
    pushSample(state.tps, st.tps_known ? Number(st.tps) : null);
    pushSample(state.mem, st.memory_used_mb > 0 ? Number(st.memory_used_mb) : null);
    drawCharts();
    var t = $('chart-tps-now');
    if (t) t.textContent = st.tps_known ? Number(st.tps).toFixed(2) : '无数据（服务端未输出 TPS）';
    var m = $('chart-mem-now');
    if (m) m.textContent = st.memory_used_mb > 0 ? fmtMB(st.memory_used_mb) : '无数据';
  }

  function drawCharts() {
    drawChart($('chart-tps'), state.tps, { color: '#6cc4ff', min: 0, max: 20, unit: 'TPS', fixed: 0 });
    drawChart($('chart-mem'), state.mem, { color: '#a78bfa', min: 0, max: null, unit: 'MB', fixed: 0 });
  }

  function drawChart(canvas, series, opts) {
    if (!canvas || !canvas.getContext) return;
    var dpr = window.devicePixelRatio || 1;
    var cssW = canvas.clientWidth || canvas.parentNode.clientWidth || 320;
    var cssH = parseInt(canvas.getAttribute('data-h') || '120', 10);
    canvas.width = Math.max(120, Math.round(cssW * dpr));
    canvas.height = Math.round(cssH * dpr);
    canvas.style.height = cssH + 'px';
    var ctx = canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cssW, cssH);

    var padL = 42, padR = 8, padT = 8, padB = 16;
    var iw = Math.max(10, cssW - padL - padR);
    var ih = Math.max(10, cssH - padT - padB);

    var vals = series.filter(function (v) { return typeof v === 'number' && isFinite(v); });
    var min = (opts.min === null || opts.min === undefined) ? (vals.length ? Math.min.apply(null, vals) : 0) : opts.min;
    var max = (opts.max === null || opts.max === undefined) ? (vals.length ? Math.max.apply(null, vals) : 1) : opts.max;
    if (!isFinite(min)) min = 0;
    if (!isFinite(max) || max <= min) max = min + 1;
    // 留一点顶部余量，曲线不至于贴边
    max = max + (max - min) * 0.08;

    ctx.strokeStyle = 'rgba(255,255,255,0.08)';
    ctx.fillStyle = '#8d97ab';
    ctx.font = '11px ui-monospace, monospace';
    ctx.lineWidth = 1;
    for (var g = 0; g <= 4; g++) {
      var y = padT + (ih * g) / 4;
      ctx.beginPath();
      ctx.moveTo(padL, Math.round(y) + 0.5);
      ctx.lineTo(padL + iw, Math.round(y) + 0.5);
      ctx.stroke();
      var lv = max - ((max - min) * g) / 4;
      ctx.fillText(lv.toFixed(opts.fixed || 0), 4, y + 4);
    }

    var n = series.length;
    if (n === 0) {
      ctx.fillStyle = '#8d97ab';
      ctx.fillText('（暂无采样数据）', padL + 6, padT + ih / 2);
      return;
    }
    function px(i) { return padL + (n <= 1 ? iw : (iw * i) / (n - 1)); }
    function py(v) { return padT + ih - ((v - min) / (max - min)) * ih; }

    // 面积
    ctx.beginPath();
    var started = false;
    var firstX = null, lastX = null;
    for (var i = 0; i < n; i++) {
      var v = series[i];
      if (typeof v !== 'number' || !isFinite(v)) { started = false; continue; }
      if (!started) { ctx.moveTo(px(i), py(v)); started = true; if (firstX === null) firstX = px(i); }
      else ctx.lineTo(px(i), py(v));
      lastX = px(i);
    }
    if (firstX !== null && lastX !== null && lastX > firstX) {
      ctx.lineTo(lastX, padT + ih);
      ctx.lineTo(firstX, padT + ih);
      ctx.closePath();
      ctx.fillStyle = opts.color.replace(')', ', 0.14)').replace('rgb(', 'rgba(');
      ctx.fillStyle = hexA(opts.color, 0.14);
      ctx.fill();
    }

    // 折线
    ctx.beginPath();
    ctx.strokeStyle = opts.color;
    ctx.lineWidth = 1.8;
    ctx.lineJoin = 'round';
    started = false;
    for (var j = 0; j < n; j++) {
      var vv = series[j];
      if (typeof vv !== 'number' || !isFinite(vv)) { started = false; continue; }
      if (!started) { ctx.moveTo(px(j), py(vv)); started = true; }
      else ctx.lineTo(px(j), py(vv));
    }
    ctx.stroke();

    // 最后一个点
    for (var k = n - 1; k >= 0; k--) {
      var lv2 = series[k];
      if (typeof lv2 === 'number' && isFinite(lv2)) {
        ctx.beginPath();
        ctx.arc(px(k), py(lv2), 2.6, 0, Math.PI * 2);
        ctx.fillStyle = opts.color;
        ctx.fill();
        break;
      }
    }
  }

  function hexA(hex, alpha) {
    var m = /^#([0-9a-f]{6})$/i.exec(hex);
    if (!m) return hex;
    var num = parseInt(m[1], 16);
    return 'rgba(' + ((num >> 16) & 255) + ',' + ((num >> 8) & 255) + ',' + (num & 255) + ',' + alpha + ')';
  }

  // ---------------------------------------------------------------- 日志

  function logLimit() {
    var input = $('log-limit');
    var n = input ? parseInt(input.value, 10) : 500;
    if (!isFinite(n) || n < 50) n = 50;
    if (n > 2000) n = 2000;
    return n;
  }

  function logAppend(text) {
    if (text === undefined || text === null) return;
    state.logs.push(String(text));
    var limit = logLimit();
    if (state.logs.length > limit) state.logs.splice(0, state.logs.length - limit);
    if (state.logPaused) return;
    scheduleLogRender();
  }

  function scheduleLogRender() {
    if (state.logTimer) return;
    state.logTimer = setTimeout(function () {
      state.logTimer = null;
      renderLogs();
    }, 200);
  }

  function classify(text) {
    var t = text.toUpperCase();
    if (t.indexOf('/ERROR') >= 0 || t.indexOf('] ERROR') >= 0 || t.indexOf('ERROR]') >= 0 || t.indexOf('EXCEPTION') >= 0 || t.indexOf('FATAL') >= 0) return 'k-err';
    if (t.indexOf('/WARN') >= 0 || t.indexOf('] WARN') >= 0 || t.indexOf('WARN]') >= 0) return 'k-warn';
    if (text.indexOf('[nyatmc]') === 0) return 'k-sys';
    return '';
  }

  function lineNode(text, filter) {
    var cls = classify(text);
    var node = el('span', 'log-line' + (cls ? ' ' + cls : ''));
    if (!filter) {
      node.textContent = text;
      return node;
    }
    var lower = text.toLowerCase();
    var f = filter.toLowerCase();
    var idx = 0;
    while (true) {
      var hit = lower.indexOf(f, idx);
      if (hit < 0) {
        node.appendChild(document.createTextNode(text.slice(idx)));
        break;
      }
      if (hit > idx) node.appendChild(document.createTextNode(text.slice(idx, hit)));
      node.appendChild(el('mark', null, text.slice(hit, hit + f.length)));
      idx = hit + f.length;
    }
    return node;
  }

  function renderLogs() {
    var view = $('log-view');
    if (!view) return;
    var filter = ($('log-filter') && $('log-filter').value || '').trim();
    var atBottom = view.scrollTop + view.clientHeight >= view.scrollHeight - 24;

    var frag = document.createDocumentFragment();
    var shown = 0;
    for (var i = 0; i < state.logs.length; i++) {
      var text = state.logs[i];
      if (filter && text.toLowerCase().indexOf(filter.toLowerCase()) < 0) continue;
      frag.appendChild(lineNode(text, filter));
      shown++;
    }
    view.textContent = '';
    if (!shown) {
      view.appendChild(el('span', 'log-line k-sys', filter ? '（没有匹配「' + filter + '」的日志）' : '（暂无日志）'));
    } else {
      view.appendChild(frag);
    }
    var bottom = $('btn-log-bottom');
    if (atBottom) {
      view.scrollTop = view.scrollHeight;
      if (bottom) bottom.hidden = true;
    } else if (bottom) {
      bottom.hidden = false;
    }
  }

  function setSse(kind, text) {
    var b = $('sse-state');
    if (!b) return;
    b.className = 'badge badge-' + kind;
    b.textContent = text;
  }

  function closeStream() {
    if (state.es) {
      try { state.es.close(); } catch (e) { /* ignore */ }
      state.es = null;
    }
    if (state.esTimer) {
      clearTimeout(state.esTimer);
      state.esTimer = null;
    }
  }

  function openStream() {
    closeStream();
    if (!state.current) return;
    if (typeof EventSource === 'undefined') {
      setSse('error', '浏览器不支持 SSE');
      return;
    }
    var url = '/api/instances/' + encodeURIComponent(state.current) + '/logs/stream?lines=300';
    if (TOKEN) url += '&token=' + encodeURIComponent(TOKEN);
    setSse('idle', '连接中…');
    var es;
    try { es = new EventSource(url); } catch (e) { setSse('error', '连接失败'); return; }
    state.es = es;

    es.addEventListener('open', function () {
      state.esRetry = 1000;
      setSse('open', '已连接');
    });
    es.addEventListener('hello', function (ev) {
      try {
        var d = JSON.parse(ev.data);
        if (d.refresh_seconds) state.refreshMs = Math.max(1, d.refresh_seconds) * 1000;
      } catch (e) { /* ignore */ }
    });
    es.addEventListener('log', function (ev) {
      try {
        var d = JSON.parse(ev.data);
        if (d.history && state.logs.length > 0) return; // 重连时不重复补历史
        logAppend(d.text);
      } catch (e) { /* ignore */ }
    });
    es.addEventListener('status', function (ev) {
      try { applyStatus(JSON.parse(ev.data)); } catch (e) { /* ignore */ }
    });
    es.addEventListener('exit', function (ev) {
      try {
        var d = JSON.parse(ev.data);
        logAppend('[nyatmc] 服务端进程退出：' + (d.text || ''));
      } catch (e) { /* ignore */ }
    });
    es.addEventListener('notice', function (ev) {
      try {
        var d = JSON.parse(ev.data);
        setSse('idle', '空闲');
        logAppend('[nyatmc] ' + d.text);
      } catch (e) { /* ignore */ }
    });
    es.addEventListener('error', function () {
      setSse('error', '已断开，重连中');
      closeStream();
      var delay = Math.min(state.esRetry, 30000);
      state.esRetry = Math.min(state.esRetry * 2, 30000);
      state.esTimer = setTimeout(openStream, delay);
    });
  }

  // ---------------------------------------------------------------- 备份

  function loadBackups() {
    if (!state.current) return Promise.resolve();
    return api('/api/instances/' + encodeURIComponent(state.current) + '/backups').then(function (res) {
      renderBackups(res.data);
    }).catch(function (err) {
      var body = $('backup-body');
      if (body) {
        body.textContent = '';
        var tr = el('tr');
        var td = el('td', 'muted', '加载失败：' + errText(err));
        td.colSpan = 5;
        tr.appendChild(td);
        body.appendChild(tr);
      }
    });
  }

  function renderBackups(data) {
    var body = $('backup-body');
    if (!body) return;
    body.textContent = '';
    var items = (data && data.backups) || [];
    if (!items.length) {
      var tr0 = el('tr');
      var td0 = el('td', 'muted', '还没有任何备份，点「立即备份」创建第一份。');
      td0.colSpan = 5;
      tr0.appendChild(td0);
      body.appendChild(tr0);
    }
    items.forEach(function (b, idx) {
      var tr = el('tr');
      var tdName = el('td', 'wrap', b.name);
      tr.appendChild(tdName);
      tr.appendChild(el('td', null, b.label || '—'));
      tr.appendChild(el('td', null, b.size_human || '—'));
      var tdTime = el('td', null, (b.created_at || '—') + (b.age_human && b.age_human !== '-' ? '（' + b.age_human + '前）' : ''));
      tr.appendChild(tdTime);
      var tdAct = el('td');
      var btn = el('button', 'btn btn-sm btn-warn', '回滚');
      btn.type = 'button';
      btn.addEventListener('click', function () { restoreBackup(b); });
      tdAct.appendChild(btn);
      tr.appendChild(tdAct);
      body.appendChild(tr);
      if (idx === 0) tr.title = '最新备份';
    });
    var meta = $('backup-meta');
    if (meta) {
      meta.textContent = '共 ' + items.length + ' 份 · 目录 ' + (data.dir || '—') +
        ' · 保留 ' + (data.keep || 0) + ' 份' +
        (data.max_age && data.max_age !== '0s' ? ' / 最长 ' + data.max_age : '') +
        ' · 格式 ' + (data.format || '—') +
        ' · 回滚前自动备份：' + (data.before_restore ? '开' : '关');
    }
  }

  function restoreBackup(b) {
    if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
    var running = state.status && state.status.state === 'running';
    var msg = '确定要回滚到备份「' + b.name + '」吗？\n\n' +
      '· 回滚前会自动备份当前状态（若配置允许）\n' +
      '· 回滚会覆盖服务器目录里的同名文件\n' +
      (running ? '· 实例正在运行，请先停止实例（本页面不会强制回滚）\n' : '');
    if (running) { toast(msg, 'warn'); return; }
    if (!window.confirm(msg)) return;
    toast('正在回滚，请稍候…', 'warn');
    api('/api/instances/' + encodeURIComponent(state.current) + '/backups/restore', {
      method: 'POST',
      body: { name: b.name, backup_current: true }
    }).then(function (res) {
      toast(res.data.message || '回滚完成', 'ok');
      loadBackups();
    }).catch(function (err) {
      toast('回滚失败：' + errText(err), 'err');
    });
  }

  // ---------------------------------------------------------------- 配置

  function loadConfig(keepText) {
    return api('/api/config' + (state.current ? ('?instance=' + encodeURIComponent(state.current)) : ''))
      .then(function (res) {
        var d = res.data;
        state.configRaw = d.toml || '';
        if (!keepText) {
          var ta = $('config-text');
          if (ta) ta.value = state.configRaw;
        }
        var p = $('config-path');
        if (p) p.textContent = d.path || '—';
        renderStructured(d.resolved || {});
        renderWarnings(d.warnings || []);
        showConfigError('');
        state.configLoaded = true;
      }).catch(function (err) {
        showConfigError('读取配置失败：' + errText(err));
      });
  }

  function showConfigError(text) {
    var box = $('config-error');
    if (!box) return;
    if (!text) { box.hidden = true; box.textContent = ''; return; }
    box.hidden = false;
    box.textContent = text;
  }

  function renderWarnings(list) {
    var box = $('config-warnings');
    if (!box) return;
    box.textContent = '';
    (list || []).forEach(function (w) {
      box.appendChild(el('div', 'w', '⚠ ' + w));
    });
  }

  function kvSection(parent, title, rows) {
    parent.appendChild(el('div', 'sec', title));
    rows.forEach(function (pair) {
      var row = el('div', 'row');
      row.appendChild(el('span', 'k', pair[0]));
      row.appendChild(el('span', 'v', pair[1]));
      parent.appendChild(row);
    });
  }

  function renderStructured(r) {
    var box = $('config-structured');
    if (!box) return;
    box.textContent = '';
    var g = r.general || {}, s = r.server || {}, dm = r.daemon || {}, bk = r.backup || {};
    var lg = r.log || {}, sc = r.scheduler || {}, wb = r.web || {}, tn = r.tunnel || {}, dl = r.downloader || {};

    kvSection(box, '通用', [
      ['默认实例', g.default_instance || '—'],
      ['语言', g.language || '—'],
      ['彩色输出', yn(g.color)],
      ['守护进程随启', yn(g.auto_start)]
    ]);
    kvSection(box, '服务端', [
      ['类型 / 版本', (s.type || '—') + ' / ' + (s.minecraft_version || '—')],
      ['内存', (s.memory || '—') + (s.min_memory ? '（最小 ' + s.min_memory + '）' : '')],
      ['端口 / 人数上限', (s.port || '—') + ' / ' + (s.max_players || '—')],
      ['正版验证', yn(s.online_mode)],
      ['已同意 EULA', yn(s.eula)],
      ['启动前自动下载', yn(s.auto_download)],
      ['服务端 jar', s.jar_name || '—'],
      ['视距', s.view_distance != null ? s.view_distance : '—']
    ]);
    kvSection(box, '守护进程', [
      ['看门狗', yn(dm.watchdog)],
      ['最大重启次数', dm.max_restarts != null ? dm.max_restarts : 0],
      ['优雅关闭超时', dur(dm.stop_timeout)],
      ['启动等待超时', dur(dm.startup_timeout)],
      ['捕获控制台', yn(dm.capture_console)]
    ]);
    kvSection(box, '备份', [
      ['格式 / 保留份数', (bk.format || '—') + ' / ' + (bk.keep || 0)],
      ['最长保留', dur(bk.max_age)],
      ['回滚前自动备份', yn(bk.before_restore)],
      ['包含条目', (bk.include || []).length + ' 项'],
      ['排除条目', (bk.exclude || []).length + ' 项']
    ]);
    kvSection(box, '日志', [
      ['轮转阈值 / 保留', (typeof lg.rotate_size === 'number' ? (lg.rotate_size / 1048576).toFixed(0) + ' MB' : '—') + ' / ' + (lg.keep_files || 0) + ' 份'],
      ['压缩归档', yn(lg.compress)],
      ['重启切割', yn(lg.rotate_restart)],
      ['控制台缓冲行数', lg.console_lines || 0]
    ]);
    kvSection(box, 'Web', [
      ['监听地址', wb.listen || '—'],
      ['令牌', wb.token ? '已设置' : '未设置（仅本机可访问）'],
      ['允许写操作', yn(wb.enable_write)],
      ['刷新间隔', (wb.refresh_seconds || 3) + ' 秒'],
      ['日志上限', wb.max_log_lines || 2000]
    ]);
    kvSection(box, '调度', [
      ['启用', yn(sc.enabled)],
      ['时区', sc.timezone || '本机时区'],
      ['任务数', (sc.jobs || []).length]
    ]);
    kvSection(box, '穿透 / 下载', [
      ['提供方', tn.provider || '—'],
      ['随 Web 自动启动', yn(tn.auto_start_with_web)],
      ['下载镜像', dl.mirror ? dl.mirror : '官方源'],
      ['下载超时', dur(dl.timeout)]
    ]);
  }

  // 保存前的轻量检查：明显写坏的 TOML 直接拦下，避免来回一趟。
  function basicTomlCheck(text) {
    if (!text.trim()) return '配置内容不能为空';
    var lines = text.split(/\r?\n/);
    var seen = {};
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i].trim();
      if (!line || line.charAt(0) === '#') continue;
      if (line.charAt(0) === '[') {
        var end = line.lastIndexOf(']');
        if (end < 0) return '第 ' + (i + 1) + ' 行：段名缺少右括号 ]';
        var name = line.slice(line.indexOf('[') + 1, end).trim();
        if (!name) return '第 ' + (i + 1) + ' 行：段名不能为空';
        if (line.indexOf('[[') !== 0) {
          if (seen[name]) return '第 ' + (i + 1) + ' 行：段 [' + name + '] 重复定义';
          seen[name] = true;
        }
      }
      // 去掉注释后再数引号，避免注释里的引号误报
      var body = line;
      var hash = -1;
      var inStr = false;
      for (var c = 0; c < body.length; c++) {
        if (body.charAt(c) === '"') inStr = !inStr;
        else if (body.charAt(c) === '#' && !inStr) { hash = c; break; }
      }
      if (hash >= 0) body = body.slice(0, hash);
      var quotes = (body.match(/"/g) || []).length;
      if (quotes % 2 !== 0) return '第 ' + (i + 1) + ' 行：双引号没有成对';
      if (/[^\\]\\$/.test(body.trim())) return '第 ' + (i + 1) + ' 行：疑似不完整的转义';
      if (body.indexOf('=') < 0 && !/^\[/.test(body.trim())) {
        return '第 ' + (i + 1) + ' 行：不是合法的键值对或段名';
      }
    }
    return '';
  }

  function saveConfig() {
    var ta = $('config-text');
    if (!ta) return;
    var text = ta.value;
    var bad = basicTomlCheck(text);
    if (bad) {
      showConfigError('本地检查未通过：' + bad);
      toast('本地检查未通过，未提交保存', 'err');
      return;
    }
    if (!state.writeEnabled) {
      showConfigError('web.enable_write = false，已禁止通过 Web 修改配置');
      return;
    }
    showConfigError('');
    api('/api/config', { method: 'PUT', body: { toml: text } }).then(function (res) {
      toast(res.data.message || '配置已保存', 'ok');
      state.configRaw = text;
      renderStructured(res.data.resolved || {});
      renderWarnings(res.data.warnings || []);
      loadBackups();
      return refreshAll();
    }).catch(function (err) {
      showConfigError('保存失败：' + errText(err));
      toast('配置保存失败', 'err');
    });
  }

  // ---------------------------------------------------------------- 调度任务

  function loadScheduler() {
    return api('/api/scheduler/jobs').then(function (res) {
      renderScheduler(res.data);
    }).catch(function (err) {
      var body = $('scheduler-body');
      if (body) {
        body.textContent = '';
        var tr = el('tr');
        var td = el('td', 'muted', '加载失败：' + errText(err));
        td.colSpan = 7;
        tr.appendChild(td);
        body.appendChild(tr);
      }
    });
  }

  function renderScheduler(data) {
    var body = $('scheduler-body');
    if (!body) return;
    body.textContent = '';
    var jobs = (data && data.jobs) || [];
    if (!jobs.length) {
      var tr0 = el('tr');
      var td0 = el('td', 'muted', '还没有调度任务，用下面的表单新增。');
      td0.colSpan = 7;
      tr0.appendChild(td0);
      body.appendChild(tr0);
    }
    jobs.forEach(function (j) {
      var tr = el('tr');
      tr.appendChild(el('td', null, j.name));
      tr.appendChild(el('td', 'mono', j.schedule));
      tr.appendChild(el('td', null, j.action + (j.command ? '（' + j.command + '）' : '')));
      tr.appendChild(el('td', null, j.instance || '默认'));
      tr.appendChild(el('td', null, j.next_human ? (j.next_human + (j.next_run ? '（' + j.next_run.replace('T', ' ').slice(0, 19) + '）' : '')) : (j.parse_error || '—')));
      var tdState = el('td');
      tdState.appendChild(el('span', 'badge ' + (j.enabled ? 'badge-running' : 'badge-stopped'), j.enabled ? '已启用' : '已停用'));
      tr.appendChild(tdState);
      var tdAct = el('td');
      var btn = el('button', 'btn btn-sm btn-danger', '删除');
      btn.type = 'button';
      btn.addEventListener('click', function () {
        if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
        if (!window.confirm('确定删除调度任务「' + j.name + '」吗？')) return;
        api('/api/scheduler/jobs/' + encodeURIComponent(j.name), { method: 'DELETE' }).then(function (res) {
          toast(res.data.message || '已删除', 'ok');
          loadScheduler();
        }).catch(function (err) { toast('删除失败：' + errText(err), 'err'); });
      });
      tdAct.appendChild(btn);
      tr.appendChild(tdAct);
      body.appendChild(tr);
    });
    var meta = $('scheduler-meta');
    if (meta) {
      meta.textContent = '调度器：' + (data.enabled ? '已启用' : '已停用') +
        ' · 时区 ' + (data.timezone || data.effective_zone || '本机') +
        ' · 共 ' + jobs.length + ' 个任务';
    }
  }

  // ---------------------------------------------------------------- 控制动作

  function action(path, body, okMsg, confirmMsg) {
    if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
    if (!state.current) { toast('请先选择实例', 'warn'); return; }
    if (confirmMsg && !window.confirm(confirmMsg)) return;
    var btnIds = ['btn-start', 'btn-stop', 'btn-restart', 'btn-kill'];
    btnIds.forEach(function (id) { var b = $(id); if (b) b.disabled = true; });
    api('/api/instances/' + encodeURIComponent(state.current) + path, { method: 'POST', body: body || {} })
      .then(function (res) {
        toast((res.data && res.data.message) || okMsg, 'ok');
        echo('> ' + ((res.data && res.data.message) || okMsg));
        return refreshAll();
      })
      .catch(function (err) {
        toast(errText(err), 'err');
        echo('! ' + errText(err));
        return refreshAll();
      })
      .then(function () {
        if (state.status) renderControlState(state.status);
      });
  }

  function echo(text) {
    var box = $('cmd-echo');
    if (!box) return;
    var lines = box.textContent.split('\n');
    lines.push('[' + new Date().toLocaleTimeString('zh-CN') + '] ' + text);
    while (lines.length > 30) lines.shift();
    box.textContent = lines.join('\n');
    box.scrollTop = box.scrollHeight;
  }

  function sendCommand() {
    var input = $('cmd-input');
    if (!input) return;
    var line = input.value.trim();
    if (!line) return;
    if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
    if (!state.current) { toast('请先选择实例', 'warn'); return; }
    api('/api/instances/' + encodeURIComponent(state.current) + '/command', { method: 'POST', body: { line: line } })
      .then(function (res) {
        echo('> ' + line);
        input.value = '';
        toast((res.data && res.data.message) || '指令已发送', 'ok');
      })
      .catch(function (err) {
        echo('! ' + errText(err));
        toast(errText(err), 'err');
      });
  }

  // ---------------------------------------------------------------- 轮询

  function refreshAll() {
    return api('/api/overview').then(function (res) {
      state.instances = res.data.instances || [];
      updateInstanceSelect();
      if (!state.current && state.instances.length) {
        selectInstance(state.instances[0].name);
        return;
      }
      var cur = null;
      for (var i = 0; i < state.instances.length; i++) {
        if (state.instances[i].name === state.current) { cur = state.instances[i]; break; }
      }
      if (cur) {
        applyStatus(cur);
      } else if (state.instances.length === 0) {
        setBadge('unknown', '没有实例');
      }
      state.tick++;
      if (state.tick % 5 === 0) loadBackups();
    }).catch(function (err) {
      setBadge('unknown', '连接失败');
      if (err.status === 401) toast(errText(err), 'err');
    });
  }

  function selectInstance(name) {
    if (!name || name === state.current) return;
    state.current = name;
    state.logs = [];
    state.tps = [];
    state.mem = [];
    state.tick = 0;
    var view = $('log-view');
    if (view) view.textContent = '';
    renderLogs();
    openStream();
    refreshInstanceDetail();
    loadBackups();
    if (state.configLoaded) loadConfig(false);
  }

  function refreshInstanceDetail() {
    if (!state.current) return Promise.resolve();
    return api('/api/instances/' + encodeURIComponent(state.current)).then(function (res) {
      var d = res.data || {};
      if (d.status) applyStatus(d.status);
    }).catch(function (err) {
      if (err.status === 404) {
        toast('实例已不存在，正在刷新列表', 'warn');
        state.current = '';
      }
    });
  }

  // ---------------------------------------------------------------- 穿透

  function tunnelStatus() {
    return api('/api/tunnel/status').then(function (res) {
      return res.data || {};
    });
  }

  // 一键启停：运行中则停止，未运行则启动（默认把仪表盘自身暴露到公网）。
  function toggleTunnel() {
    tunnelStatus().then(function (d) {
      if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
      if (d.running) {
        api('/api/tunnel/stop', { method: 'POST', body: {} }).then(function (res) {
          toast((res.data && res.data.message) || '隧道已停止', 'ok');
        }).catch(function (err) { toast(errText(err), 'err'); });
        return;
      }
      toast('正在启动 Cloudflare 隧道（最多等待 30 秒）…', 'warn');
      api('/api/tunnel/start', { method: 'POST', body: {} }).then(function (res) {
        var st = res.data || {};
        if (st.url) {
          toast('公网地址：' + st.url, 'ok');
        } else {
          toast(st.message || '隧道已启动，公网地址建立中', 'warn');
        }
      }).catch(function (err) { toast(errText(err), 'err'); });
    }).catch(function (err) {
      toast('穿透状态查询失败：' + errText(err), 'err');
    });
  }

  // ---------------------------------------------------------------- 初始化

  function bind() {
    var sel = $('instance-select');
    if (sel) sel.addEventListener('change', function () { selectInstance(sel.value); });

    var reload = $('btn-reload');
    if (reload) reload.addEventListener('click', function () { refreshAll(); loadBackups(); loadScheduler(); loadConfig(true); });

    var tunnel = $('btn-tunnel');
    if (tunnel) tunnel.addEventListener('click', toggleTunnel);

    var start = $('btn-start');
    if (start) start.addEventListener('click', function () {
      action('/start', { wait: false }, '实例已提交启动');
    });

    var stop = $('btn-stop');
    if (stop) stop.addEventListener('click', function () {
      var name = state.current;
      action('/stop', { force: false }, '实例已停止',
        '确定要优雅停止实例「' + name + '」吗？\n\nnyatmc 会先向服务端发送 stop 指令，超时后才强制结束进程。');
    });

    var kill = $('btn-kill');
    if (kill) kill.addEventListener('click', function () {
      var name = state.current;
      action('/stop', { force: true }, '已强制结束实例',
        '确定要「强制结束」实例「' + name + '」吗？\n\n这会直接杀掉进程，世界可能来不及完整落盘，建议优先使用优雅停止。');
    });

    var restart = $('btn-restart');
    if (restart) restart.addEventListener('click', function () {
      action('/restart', { force: false }, '实例已提交重启');
    });

    var cmd = $('btn-cmd');
    if (cmd) cmd.addEventListener('click', sendCommand);
    var cmdInput = $('cmd-input');
    if (cmdInput) {
      cmdInput.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter') { ev.preventDefault(); sendCommand(); }
      });
    }

    var filter = $('log-filter');
    if (filter) {
      filter.addEventListener('input', function () { renderLogs(); });
      filter.addEventListener('change', function () { renderLogs(); });
    }
    var limit = $('log-limit');
    if (limit) limit.addEventListener('change', function () { renderLogs(); });

    var pause = $('btn-log-pause');
    if (pause) pause.addEventListener('click', function () {
      state.logPaused = !state.logPaused;
      pause.textContent = state.logPaused ? '继续' : '暂停';
      if (!state.logPaused) renderLogs();
      toast(state.logPaused ? '已暂停日志刷新（后台仍在接收）' : '已继续日志刷新', 'ok');
    });

    var clear = $('btn-log-clear');
    if (clear) clear.addEventListener('click', function () {
      state.logs = [];
      renderLogs();
    });

    var bottom = $('btn-log-bottom');
    if (bottom) bottom.addEventListener('click', function () {
      var view = $('log-view');
      if (view) view.scrollTop = view.scrollHeight;
      bottom.hidden = true;
    });

    var view = $('log-view');
    if (view) {
      view.addEventListener('scroll', function () {
        var b = $('btn-log-bottom');
        if (!b) return;
        var atBottom = view.scrollTop + view.clientHeight >= view.scrollHeight - 24;
        b.hidden = atBottom;
      });
    }

    var bCreate = $('btn-backup-create');
    if (bCreate) bCreate.addEventListener('click', function () {
      if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
      if (!state.current) { toast('请先选择实例', 'warn'); return; }
      var labelInput = $('backup-label');
      var label = labelInput ? labelInput.value.trim() : '';
      bCreate.disabled = true;
      toast('正在备份，大世界的打包可能需要一会儿…', 'warn');
      api('/api/instances/' + encodeURIComponent(state.current) + '/backups', { method: 'POST', body: { label: label } })
        .then(function (res) {
          toast(res.data.message || '备份完成', 'ok');
          if (labelInput) labelInput.value = '';
          loadBackups();
        })
        .catch(function (err) { toast('备份失败：' + errText(err), 'err'); })
        .then(function () { bCreate.disabled = false; });
    });

    var bPrune = $('btn-backup-prune');
    if (bPrune) bPrune.addEventListener('click', function () {
      if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
      var def = (state.status && state.status.name) ? '' : '';
      var raw = window.prompt('保留最近多少份备份？更旧的会被删除。\n（留空表示沿用配置里的 backup.keep）', def);
      if (raw === null) return;
      var body = {};
      if (raw.trim() !== '') {
        var n = parseInt(raw, 10);
        if (!isFinite(n) || n < 0) { toast('请输入非负整数', 'err'); return; }
        body.keep = n;
      }
      bPrune.disabled = true;
      api('/api/instances/' + encodeURIComponent(state.current) + '/backups', { method: 'DELETE', body: body })
        .then(function (res) { toast(res.data.message || '已清理', 'ok'); loadBackups(); })
        .catch(function (err) { toast('清理失败：' + errText(err), 'err'); })
        .then(function () { bPrune.disabled = false; });
    });

    var cReload = $('btn-config-reload');
    if (cReload) cReload.addEventListener('click', function () { loadConfig(false); toast('已重新载入配置', 'ok'); });
    var cSave = $('btn-config-save');
    if (cSave) cSave.addEventListener('click', saveConfig);

    var form = $('scheduler-form');
    if (form) form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      if (!state.writeEnabled) { toast('web.enable_write = false，写操作已禁用', 'warn'); return; }
      var fd = new FormData(form);
      var body = {
        name: (fd.get('name') || '').toString().trim(),
        schedule: (fd.get('schedule') || '').toString().trim(),
        action: (fd.get('action') || '').toString().trim(),
        instance: (fd.get('instance') || '').toString().trim(),
        command: (fd.get('command') || '').toString().trim()
      };
      if (!body.name || !body.schedule) { toast('任务名与 cron 表达式都不能为空', 'err'); return; }
      api('/api/scheduler/jobs', { method: 'POST', body: body }).then(function (res) {
        toast(res.data.message || '已保存', 'ok');
        form.reset();
        loadScheduler();
      }).catch(function (err) { toast('新增任务失败：' + errText(err), 'err'); });
    });

    window.addEventListener('resize', function () { drawCharts(); });

    // 切回前台时立刻刷新一次。
    document.addEventListener('visibilitychange', function () {
      if (!document.hidden) refreshAll();
    });
  }

  function boot() {
    bind();
    renderLogs();
    drawCharts();
    api('/api/health').then(function (res) {
      var d = res.data || {};
      state.writeEnabled = d.write_enabled !== false;
      state.tokenRequired = !!d.token_required;
      var v = $('app-version');
      if (v) v.textContent = 'v' + (d.version || 'dev') + (state.writeEnabled ? '' : ' · 只读模式');
      if (!state.writeEnabled) toast('当前为只读模式（web.enable_write = false），写操作已禁用', 'warn');
      if (d.instances === 0) toast('还没有任何实例，先用 nyatmc create <名字> 新建', 'warn');
    }).catch(function (err) {
      toast('健康检查失败：' + errText(err), 'err');
    }).then(function () {
      return refreshAll();
    }).then(function () {
      loadScheduler();
      loadConfig(false);
      setInterval(function () {
        if (document.hidden) return;
        refreshAll();
      }, Math.max(1000, state.refreshMs));
      setInterval(function () {
        if (document.hidden) return;
        refreshInstanceDetail();
      }, 15000);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
