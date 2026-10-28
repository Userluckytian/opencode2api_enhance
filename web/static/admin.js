'use strict';

/* token 自助餐厅 · 管理后台
   对接后端 API：
   GET  api/admin/overview          概览（含 7 日趋势、Key 状态分布）
   GET  api/admin/keys               Key 列表（含 expired/exhausted/status）
   GET  api/admin/logs?date=&key_id=&ip=   日志明细
   GET  api/admin/logs/aggregate?date=      按 IP 汇总
   GET  api/admin/ip-policies        默认策略 / 覆盖 / 实时窗口用量
   PUT  api/admin/ip-policies/defaults
   PUT  api/admin/ip-policies/{ip}   覆盖策略（null=继承）
   DEL  api/admin/ip-policies/{ip}
   POST api/admin/ip-policies/{ip}/reset
   POST api/admin/ask                AI 问答
*/

let token = localStorage.getItem('token') || '';
let keyCache = [];
let keyState = { q: '', status: 'all', page: 1, pageSize: 20 };
let revealed = new Set();
let logState = { date: todayStr(), key: '', ip: '', mode: 'detail' };
let ipState = { defaults: {}, overrides: [], usage: [] };
let askState = { messages: [], busy: false, model: '', models: [], modelOpen: false, modelQuery: '' };
let defaultQuota = 0; // 全局默认额度（唯一来源：config.token_quota）
let lastKnownQuota = 0; // 页面加载时的额度，用于判断本次保存是否改了额度

function el(id) { return document.getElementById(id); }
function esc(s) {
  return String(s === null || s === undefined ? '' : s).replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}
function todayStr(d) {
  const t = d || new Date();
  const p = n => String(n).padStart(2, '0');
  return t.getFullYear() + '-' + p(t.getMonth() + 1) + '-' + p(t.getDate());
}
function fmtNum(n) { return Number(n || 0).toLocaleString('zh-CN'); }
function fmtTokens(n) {
  n = Number(n || 0);
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿';
  if (n >= 1e4) return (n / 1e4).toFixed(n >= 1e6 ? 0 : 1) + '万';
  return String(n);
}
function fmtTime(s) { return s ? new Date(s).toLocaleString('zh-CN', { hour12: false }) : '-'; }
function fmtDateTime(s) { return s ? new Date(s).toLocaleString('zh-CN', { hour12: false }) : '-'; }
// fmtResetAt 把窗口重置时间显示成「今天 08:00 / 次日 08:00」，比完整时间戳好读。
function fmtResetAt(s) {
  if (!s) return '-';
  const d = new Date(s);
  const hh = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  const now = new Date();
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const days = Math.round((new Date(d.getFullYear(), d.getMonth(), d.getDate()) - startOfToday) / 86400000);
  if (days === 0) return '今天 ' + hh;
  if (days === 1) return '次日 ' + hh;
  return (d.getMonth() + 1) + '月' + d.getDate() + '日 ' + hh;
}
function pct(a, b) { return b > 0 ? Math.min(100, Math.round(a / b * 100)) : 0; }
function wan2tokens(v) { return Math.round(Number(v || 0) * 10000); }
function tokens2wan(v) { return v === null || v === undefined ? '' : String(Math.round(Number(v) / 10000 * 100) / 100); }

async function api(path, method = 'GET', body) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = 'Bearer ' + token;
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await res.json().catch(() => ({}));
  if (res.status === 401 && path.indexOf('/login') < 0) {
    logout();
    return { ok: false, status: 401, data: { error: '登录已失效，请重新登录' } };
  }
  return { ok: res.ok, status: res.status, data };
}
function fail(data) {
  toast((data && data.error) || '请求失败', 'err');
  if (data && data.error === 'unauthorized') logout();
}

/* ---------- toast / modal ---------- */
function toast(msg, type) {
  const box = el('toasts');
  const d = document.createElement('div');
  d.className = 'toast' + (type ? ' ' + type : '');
  d.textContent = msg;
  box.appendChild(d);
  setTimeout(() => { d.style.opacity = '0'; setTimeout(() => d.remove(), 250); }, 2600);
}
function openModal(html) {
  el('modalBox').innerHTML = html;
  el('modalRoot').classList.add('open');
}
function closeModal() { el('modalRoot').classList.remove('open'); }
function confirmBox(title, text, okText, danger) {
  return new Promise(resolve => {
    openModal('<h3>' + esc(title) + '</h3><div class="hint" style="color:#475569">' + esc(text) + '</div>' +
      '<div class="actions"><button class="btn" id="mCancel">取消</button>' +
      '<button class="btn ' + (danger ? 'danger' : 'primary') + '" id="mOk">' + esc(okText || '确定') + '</button></div>');
    el('mCancel').onclick = () => { closeModal(); resolve(false); };
    el('mOk').onclick = () => { closeModal(); resolve(true); };
  });
}

/* ---------- 登录 ---------- */
function claimPageURL() {
  // 反代带路径前缀时（如 /token-buffet/admin.html）也要能回到同前缀下的领取页。
  const prefix = location.pathname.replace(/\/?admin(\.html)?\/?$/, '');
  return prefix ? prefix.replace(/\/*$/, '/') : '/';
}
function bindBackLinks() {
  const url = claimPageURL();
  document.querySelectorAll('[data-claim-link]').forEach(a => { a.href = url; });
}
async function doLogin(pw) {
  const res = await fetch('api/admin/login', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: pw })
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) return (data.error || '登录失败');
  token = data.token;
  localStorage.setItem('token', token);
  return null;
}
function showLogin() {
  el('appView').style.display = 'none';
  el('loginView').style.display = 'flex';
  el('loginPw').value = '';
  setTimeout(() => el('loginPw').focus(), 30);
}
function logout() {
  token = '';
  localStorage.removeItem('token');
  showLogin();
}
function enter() {
  el('loginView').style.display = 'none';
  el('appView').style.display = 'block';
  switchView('overview');
}

el('loginForm').addEventListener('submit', async ev => {
  ev.preventDefault();
  const btn = ev.target.querySelector('button');
  btn.disabled = true;
  el('loginErr').textContent = '';
  const err = await doLogin(el('loginPw').value);
  btn.disabled = false;
  if (err) { el('loginErr').textContent = err; return; }
  enter();
});
el('logout').addEventListener('click', logout);

/* ---------- 导航 ---------- */
const VIEW_LOADERS = {
  overview: loadOverview, keys: loadKeys, logs: loadLogs,
  ips: loadIPs, upstreams: loadUpstreams, models: loadModels,
  settings: loadSettings, ask: loadAsk
};
function switchView(name) {
  document.querySelectorAll('aside nav button').forEach(b => b.classList.toggle('active', b.dataset.view === name));
  document.querySelectorAll('.view').forEach(v => v.classList.toggle('active', v.id === name));
  if (VIEW_LOADERS[name]) VIEW_LOADERS[name]();
  // 窄屏侧栏是横向滚动的，切页后把当前项滚进可视区
  const cur = document.querySelector('aside nav button.active');
  if (cur && cur.scrollIntoView) {
    try { cur.scrollIntoView({ block: 'nearest', inline: 'center' }); } catch (e) { /* 忽略 */ }
  }
}
document.querySelectorAll('aside nav button').forEach(b => b.addEventListener('click', () => switchView(b.dataset.view)));

/* ---------- 概览 ---------- */
async function loadOverview() {
  const [ov, agg, ips, st] = await Promise.all([
    api('api/admin/overview'), api('api/admin/logs/aggregate'), api('api/admin/ip-policies'),
    api('api/admin/settings')
  ]);
  if (st.ok) defaultQuota = Number(st.data.token_quota || 0);
  if (!ov.ok) return fail(ov.data);
  if (ips.ok) ipState = { defaults: ips.data.defaults || {}, overrides: ips.data.overrides || [], usage: ips.data.usage || [] };
  const d = ov.data;
  const ks = d.keys || {};
  const trend = d.trend || [];
  const maxReq = Math.max(1, ...trend.map(t => t.requests));
  const maxTok = Math.max(1, ...trend.map(t => t.tokens));
  const bars = trend.map(t =>
    '<div class="col" title="' + esc(t.date + ' 请求 ' + t.requests + ' · token ' + t.tokens) + '">' +
    '<div class="bars"><i class="req" style="height:' + Math.round(t.requests / maxReq * 100) + '%"></i>' +
    '<i style="height:' + Math.round(t.tokens / maxTok * 100) + '%"></i></div>' +
    '<div class="lab">' + esc(t.date.slice(5)) + '</div></div>').join('');
  const topIP = (agg.ok && agg.data.ips ? agg.data.ips : []).slice(0, 8);
  const ipRows = topIP.length ? topIP.map(g =>
    '<div class="list-ip"><span class="mono">' + esc(g.ip) + '</span>' +
    '<span class="muted">' + fmtNum(g.requests) + ' 次 · ' + fmtTokens(g.total_tokens) + ' token' +
    (g.error_rate > 0 ? ' · <span style="color:#b91c1c">错误 ' + Math.round(g.error_rate * 100) + '%</span>' : '') +
    '</span></div>').join('') : '<div class="empty">今日暂无数据</div>';

  el('overview').innerHTML =
    '<div class="page-head"><h2>概览</h2><div class="row"><span class="muted" style="font-size:12px">更新于 ' + new Date().toLocaleTimeString('zh-CN', { hour12: false }) + '</span>' +
    '<button class="btn tiny" onclick="loadOverview()">刷新</button></div></div>' +
    '<div class="grid c4">' +
    statCard('今日 token', fmtTokens(d.tokens_today), fmtNum(d.requests_today) + ' 次请求') +
    statCard('活跃 IP', fmtNum(d.active_ips_today), '今日出现') +
    statCard('策略拦截', fmtNum(d.blocked_today), '窗口/封禁命中') +
    statCard('错误请求', fmtNum(d.error_today), '状态码 ≥ 400') +
    '</div>' +
    '<div class="split" style="margin-top:14px;">' +
    '<div class="card"><h3>最近 7 天</h3><div class="legend"><span><i style="background:#0f766e"></i>请求数</span><span><i style="background:#99f6e4"></i>token</span></div>' +
    '<div class="chart">' + bars + '</div></div>' +
    '<div class="card"><h3>Key 状态</h3>' +
    '<div class="kv"><span>可用</span><b>' + fmtNum(ks.active || 0) + '</b></div>' +
    '<div class="kv"><span>已过期</span><b>' + fmtNum(ks.expired || 0) + '</b></div>' +
    '<div class="kv"><span>已禁用</span><b>' + fmtNum(ks.disabled || 0) + '</b></div>' +
    '<div class="kv"><span>额度用尽</span><b>' + fmtNum(ks.exhausted || 0) + '</b></div>' +
    '<div class="kv"><span>总数</span><b>' + fmtNum(d.total_keys || 0) + '</b></div>' +
    '<div class="row" style="margin-top:12px"><button class="btn tiny" onclick="switchView(\'keys\')">去 Key 管理</button>' +
    '<button class="btn tiny" onclick="switchView(\'ips\')">去 IP 管理</button></div>' +
    '</div></div>' +
    '<div class="split" style="margin-top:14px;">' +
    '<div class="card"><h3>今日 IP 用量 Top</h3>' + ipRows + '</div>' +
    '<div class="card"><h3>配额顾问</h3>' +
    '<label class="f">每人每周预算（万 token）</label>' +
    '<input id="advW" type="number" min="0" value="2000" oninput="renderAdvisor()">' +
    '<div class="kv" style="margin-top:10px"><span>日均建议（按 90% 利用率）</span><b id="advD">-</b></div>' +
    '<div class="kv"><span>单 IP 日窗口建议（按 6 个活跃 IP）</span><b id="advWin">-</b></div>' +
    '<div class="kv"><span>建议输出限速</span><b>60 token/秒</b></div>' +
    '<div class="kv"><span>当前默认额度（每个 Key 每日）</span><b>' + (defaultQuota ? fmtTokens(defaultQuota) + ' token' : '未设置') + '</b></div>' +
    '<div class="hint" id="advCmp" style="margin-top:10px"></div>' +
    '<button class="btn tiny" onclick="switchView(\'ask\')">问 AI 数据分析</button>' +
    '</div></div>';
  renderAdvisor();
}
function statCard(k, v, sub) {
  return '<div class="card stat"><span class="k">' + esc(k) + '</span><div class="v">' + esc(v) + '</div><div class="sub">' + esc(sub) + '</div></div>';
}
function renderAdvisor() {
  const w = Number(el('advW') ? el('advW').value : 0);
  if (!w || w <= 0) return;
  const week = w * 10000;
  const day = week * 0.9 / 7;
  const win = day / 6;
  el('advD').textContent = fmtTokens(Math.round(day)) + ' token/天';
  el('advWin').textContent = fmtTokens(Math.round(win)) + ' token/IP·天';
  const cur = ipState.defaults || {};
  const curTok = Number(cur.window_tokens || 0);
  const cmp = el('advCmp');
  if (!curTok) {
    cmp.textContent = '当前默认策略未限制 token 额度。';
  } else if (curTok >= win) {
    cmp.innerHTML = '当前默认窗口上限 <b>' + fmtTokens(curTok) + '</b> token/窗口，高于建议值，实际消耗可能超出预算。';
  } else {
    cmp.innerHTML = '当前默认窗口上限 <b>' + fmtTokens(curTok) + '</b> token/窗口，低于建议值，用户可能被提前拦截。';
  }
}

/* ---------- Key 管理 ---------- */
async function loadKeys() {
  const { ok, data } = await api('api/admin/keys');
  if (!ok) return fail(data);
  keyCache = data.keys || [];
  renderKeys();
}
function keyStatus(k) {
  if (k.disabled) return ['gray', '已禁用'];
  if (k.expired) return ['err', '已过期'];
  if (k.exhausted) return ['warn', '额度用尽'];
  return ['ok', '正常'];
}
function filteredKeys() {
  const q = keyState.q.trim().toLowerCase();
  return keyCache.filter(k => {
    if (keyState.status !== 'all' && k.status !== keyState.status) return false;
    if (!q) return true;
    return (k.key || '').toLowerCase().includes(q) || (k.model || '').toLowerCase().includes(q) || (k.id || '').toLowerCase().includes(q);
  });
}
function renderKeys() {
  const counts = { all: keyCache.length };
  keyCache.forEach(k => { counts[k.status] = (counts[k.status] || 0) + 1; });
  const list = filteredKeys();
  const pages = Math.max(1, Math.ceil(list.length / keyState.pageSize));
  if (keyState.page > pages) keyState.page = pages;
  const slice = list.slice((keyState.page - 1) * keyState.pageSize, keyState.page * keyState.pageSize);
  const rows = slice.map(k => {
    const [cls, txt] = keyStatus(k);
    const shown = revealed.has(k.id) ? esc(k.key) : 'sk-••••••••' + esc((k.key || '').slice(-4));
    return '<tr>' +
      '<td><input type="checkbox" class="key-cb" value="' + esc(k.id) + '"></td>' +
      '<td><span class="mono">' + shown + '</span> ' +
      '<button class="icon-btn" title="显示/隐藏" onclick="toggleReveal(\'' + esc(k.id) + '\')">👁</button>' +
      '<button class="icon-btn" title="复制" onclick="copyKey(\'' + esc(k.id) + '\')">⧉</button></td>' +
      '<td>' + (k.all_models ? '<span class="badge blue">全部模型</span>' : esc(k.model)) + '</td>' +
      '<td class="mono">' + esc(k.ip || '-') + '</td>' +
      '<td><span class="badge ' + cls + '">' + txt + '</span></td>' +
      '<td style="min-width:140px"><div class="bar ' + (k.exhausted ? 'err' : (pct(k.token_used, k.quota) > 80 ? 'warn' : '')) + '"><i style="width:' + pct(k.token_used, k.quota) + '%"></i></div>' +
      '<div class="muted" style="font-size:12px">' + fmtTokens(k.token_used) + ' / ' + fmtTokens(k.quota) + '</div></td>' +
      '<td>' + esc(fmtTime(k.created_at)) + '</td>' +
      '<td class="muted" style="font-size:12px">每日 08:00 重置' +
      (k.expires_at ? '<br>到期 ' + esc(fmtTime(k.expires_at)) : '<br>永不过期') + '</td>' +
      '<td><button class="btn tiny" onclick="toggleKey(\'' + esc(k.id) + '\',' + (!k.disabled) + ')">' + (k.disabled ? '启用' : '禁用') + '</button> ' +
      '<button class="btn tiny danger" onclick="delKey(\'' + esc(k.id) + '\')">删除</button></td></tr>';
  }).join('');
  el('keys').innerHTML =
    '<div class="page-head"><h2>Key 管理</h2><div class="row form-grid">' +
    '<input id="keyQ" placeholder="搜索 Key / 模型 / ID" value="' + esc(keyState.q) + '" oninput="onKeySearch(this.value)">' +
    '<select id="keyStatus" onchange="onKeyStatus(this.value)">' +
    opt('all', '全部 (' + counts.all + ')', keyState.status) +
    opt('active', '正常 (' + (counts.active || 0) + ')', keyState.status) +
    opt('expired', '已过期 (' + (counts.expired || 0) + ')', keyState.status) +
    opt('exhausted', '额度用尽 (' + (counts.exhausted || 0) + ')', keyState.status) +
    opt('disabled', '已禁用 (' + (counts.disabled || 0) + ')', keyState.status) +
    '</select>' +
    '<button class="btn tiny" onclick="selectAllKeys()">全选本页</button>' +
    '<button class="btn tiny" onclick="batchDeleteKeys()">删除所选</button>' +
    '<button class="btn tiny" onclick="cleanupExpired()">清理过期</button>' +
    '</div></div>' +    '<div class="card table-wrap"><table><thead><tr><th></th><th>Key</th><th>模型</th><th>IP</th><th>状态</th><th>已用/额度</th><th>领取时间</th><th>额度重置</th><th>操作</th></tr></thead>' +
    '<tbody>' + (rows || '<tr><td colspan="9" class="empty">没有匹配的 Key</td></tr>') + '</tbody></table></div>' +
    '<div class="row" style="margin-top:12px"><span class="muted">共 ' + list.length + ' 条，第 ' + keyState.page + '/' + pages + ' 页</span><span class="spacer"></span>' +
    '<button class="btn tiny" onclick="gotoKeyPage(' + (keyState.page - 1) + ')">上一页</button>' +
    '<button class="btn tiny" onclick="gotoKeyPage(' + (keyState.page + 1) + ')">下一页</button></div>';
}
function opt(v, label, cur) { return '<option value="' + esc(v) + '"' + (cur === v ? ' selected' : '') + '>' + esc(label) + '</option>'; }
function onKeySearch(v) { keyState.q = v; keyState.page = 1; renderKeys(); const i = el('keyQ'); if (i) { i.focus(); i.setSelectionRange(v.length, v.length); } }
function onKeyStatus(v) { keyState.status = v; keyState.page = 1; renderKeys(); }
function gotoKeyPage(p) { if (p < 1) return; keyState.page = p; renderKeys(); }
function selectAllKeys() {
  document.querySelectorAll('.key-cb').forEach(cb => { cb.checked = !cb.checked; });
}
function selectedKeyIDs() {
  return [...document.querySelectorAll('.key-cb:checked')].map(cb => cb.value);
}
function toggleReveal(id) {
  if (revealed.has(id)) revealed.delete(id); else revealed.add(id);
  renderKeys();
}
function copyKey(id) {
  const k = keyCache.find(x => x.id === id);
  if (!k) return;
  navigator.clipboard.writeText(k.key).then(() => toast('已复制 Key', 'ok')).catch(() => toast('复制失败', 'err'));
}
async function toggleKey(id, disable) {
  const { ok, data } = await api('api/admin/keys/' + encodeURIComponent(id) + '/' + (disable ? 'disable' : 'enable'), 'POST', {});
  if (!ok) return fail(data);
  toast(disable ? '已禁用' : '已启用', 'ok');
  loadKeys();
}
async function delKey(id) {
  if (!await confirmBox('删除 Key', '删除后该 Key 立即失效且不可恢复，确认删除？', '删除', true)) return;
  const { ok, data } = await api('api/admin/keys/' + encodeURIComponent(id), 'DELETE');
  if (!ok) return fail(data);
  toast('已删除', 'ok');
  loadKeys();
}
async function batchDeleteKeys() {
  const ids = selectedKeyIDs();
  if (!ids.length) return toast('请先勾选要删除的 Key');
  if (!await confirmBox('批量删除', '将删除 ' + ids.length + ' 个 Key，确认？', '删除', true)) return;
  let done = 0;
  for (const id of ids) {
    const { ok } = await api('api/admin/keys/' + encodeURIComponent(id), 'DELETE');
    if (ok) done++;
  }
  toast('已删除 ' + done + ' 个 Key', 'ok');
  loadKeys();
}
async function cleanupExpired() {
  const ids = keyCache.filter(k => k.expired).map(k => k.id);
  if (!ids.length) return toast('没有已过期的 Key');
  if (!await confirmBox('清理过期 Key', '将删除 ' + ids.length + ' 个已过期 Key，确认？', '清理', true)) return;
  let done = 0;
  for (const id of ids) {
    const { ok } = await api('api/admin/keys/' + encodeURIComponent(id), 'DELETE');
    if (ok) done++;
  }
  toast('已清理 ' + done + ' 个过期 Key', 'ok');
  loadKeys();
}

/* ---------- 调用日志 ---------- */
async function loadLogs() {
  const keyOpts = keyCache.length ? keyCache : (await api('api/admin/keys')).data.keys || [];
  keyCache = keyCache.length ? keyCache : keyOpts;
  const params = new URLSearchParams();
  if (logState.date) params.set('date', logState.date);
  if (logState.key) params.set('key_id', logState.key);
  if (logState.ip) params.set('ip', logState.ip);
  const { ok, data } = await api('api/admin/logs?' + params.toString());
  if (!ok) return fail(data);
  const logs = data.logs || [];
  const keySel = '<select id="logKey" onchange="onLogKey(this.value)"><option value="">全部 Key</option>' +
    keyCache.map(k => '<option value="' + esc(k.id) + '"' + (logState.key === k.id ? ' selected' : '') + '>' +
      esc(k.model + ' · ' + (k.key || '').slice(-6)) + '</option>').join('') + '</select>';
  const rows = logs.slice().reverse().map(e =>
    '<tr><td>' + esc(fmtDateTime(e.time)) + '</td>' +
    '<td class="mono">' + esc(e.ip || '未知') + '</td>' +
    '<td class="mono">' + esc((e.key_id || '').slice(0, 8) || '-') + '</td>' +
    '<td>' + esc(e.model || '-') + '</td>' +
    '<td>' + fmtNum(e.prompt_tokens) + '</td>' +
    '<td>' + fmtNum(e.completion_tokens) + '</td>' +
    '<td>' + fmtNum(e.total) + '</td>' +
    '<td><span class="badge ' + (e.status >= 400 ? 'err' : 'ok') + '">' + esc(e.status) + '</span></td>' +
    '<td>' + fmtNum(e.ms) + 'ms</td>' +
    '<td class="muted" style="font-size:12px">' + esc(e.error || '') + '</td></tr>').join('');
  const totals = logs.reduce((a, e) => ({
    req: a.req + 1, tok: a.tok + (e.total || 0), err: a.err + (e.status >= 400 ? 1 : 0)
  }), { req: 0, tok: 0, err: 0 });
  el('logs').innerHTML =
    '<div class="page-head"><h2>调用日志</h2><div class="seg">' +
    '<button class="' + (logState.mode === 'detail' ? 'on' : '') + '" onclick="setLogMode(\'detail\')">明细</button>' +
    '<button class="' + (logState.mode === 'agg' ? 'on' : '') + '" onclick="setLogMode(\'agg\')">按 IP 汇总</button>' +
    '</div></div>' +
    '<div class="card" style="margin-bottom:14px"><div class="row form-grid">' +
    '<input id="logDate" type="date" value="' + esc(logState.date) + '" onchange="onLogDate(this.value)">' +
    keySel +
    '<input id="logIp" placeholder="按 IP 过滤" value="' + esc(logState.ip) + '" oninput="onLogIp(this.value)">' +
    '<div class="row full" style="gap:8px">' +
    '<button class="btn primary" onclick="loadLogs()">查询</button>' +
    '<button class="btn" onclick="resetLogFilter()">重置</button>' +
    '<span class="spacer"></span><span class="muted">' + fmtNum(totals.req) + ' 次 · ' + fmtTokens(totals.tok) + ' token · 错误 ' + fmtNum(totals.err) + '</span></div>' +
    '</div></div>' +
    '<div class="card table-wrap" id="logBody"></div>';
  if (logState.mode === 'detail') {
    el('logBody').innerHTML = '<table><thead><tr><th>时间</th><th>IP</th><th>Key</th><th>模型</th><th>输入</th><th>输出</th><th>总计</th><th>状态</th><th>耗时</th><th>备注</th></tr></thead>' +
      '<tbody>' + (rows || '<tr><td colspan="10" class="empty">该筛选条件下暂无记录</td></tr>') + '</tbody></table>';
  } else {
    await loadAggregate();
  }
}
function onLogDate(v) { logState.date = v; loadLogs(); }
function onLogKey(v) { logState.key = v; loadLogs(); }
function onLogIp(v) { logState.ip = v.trim(); }
function setLogMode(m) { logState.mode = m; loadLogs(); }
function resetLogFilter() { logState = { date: todayStr(), key: '', ip: '', mode: logState.mode }; loadLogs(); }
async function loadAggregate() {
  const params = new URLSearchParams();
  if (logState.date) params.set('date', logState.date);
  const { ok, data } = await api('api/admin/logs/aggregate?' + params.toString());
  if (!ok) return fail(data);
  const ips = data.ips || [];
  const t = data.totals || {};
  const rows = ips.map(g =>
    '<tr><td class="mono">' + esc(g.ip) + '</td>' +
    '<td>' + fmtNum(g.requests) + '</td>' +
    '<td>' + fmtTokens(g.total_tokens) + '</td>' +
    '<td>' + fmtNum(g.keys) + '</td>' +
    '<td>' + (g.errors ? '<span style="color:#b91c1c">' + Math.round(g.error_rate * 100) + '%</span>' : '0%') + '</td>' +
    '<td>' + fmtNum(g.blocked) + '</td>' +
    '<td>' + esc(fmtDateTime(g.last_seen)) + '</td>' +
    '<td><button class="btn tiny" onclick="viewIPLogs(\'' + esc(g.ip) + '\')">查看明细</button></td></tr>').join('');
  el('logBody').innerHTML =
    '<div class="row" style="margin-bottom:10px"><span class="muted">汇总 ' + fmtNum(t.requests) + ' 次请求 · ' + fmtTokens(t.tokens) + ' token · ' +
    fmtNum(t.unique_ip) + ' 个 IP · 错误 ' + fmtNum(t.errors) + ' · 拦截 ' + fmtNum(t.blocked) + '</span></div>' +
    '<table><thead><tr><th>IP</th><th>请求数</th><th>总 token</th><th>Key 数</th><th>错误率</th><th>被拦截</th><th>最近活跃</th><th>操作</th></tr></thead>' +
    '<tbody>' + (rows || '<tr><td colspan="8" class="empty">该日期暂无记录</td></tr>') + '</tbody></table>';
}
function viewIPLogs(ip) {
  logState.ip = ip;
  logState.mode = 'detail';
  loadLogs();
}

/* ---------- IP 管理 ---------- */
let claimRule = { allow_cidrs: [], one_key_per_ip: true, reset_hour: 8, claimed_count: 0 };
async function loadIPs() {
  const [pol, claim] = await Promise.all([api('api/admin/ip-policies'), api('api/admin/claim-policy')]);
  if (!pol.ok) return fail(pol.data);
  ipState = { defaults: pol.data.defaults || {}, overrides: pol.data.overrides || [], usage: pol.data.usage || [] };
  if (claim.ok) {
    const r = claim.data.rule || {};
    claimRule = {
      allow_cidrs: r.allow_cidrs || [],
      one_key_per_ip: r.one_key_per_ip !== false,
      claimed_count: (claim.data.claimed || []).length
    };
  }
  renderIPs();
}
async function saveClaimPolicy() {
  const cidrs = el('claimCidrs').value.split(/[,\s]+/).filter(Boolean);
  const { ok, data } = await api('api/admin/claim-policy', 'PUT', {
    allow_cidrs: cidrs,
    one_key_per_ip: el('claimOneKey').checked
  });
  if (!ok) return fail(data);
  toast('领取规则已保存', 'ok');
  loadIPs();
}
function ipRows() {
  const map = new Map();
  (ipState.usage || []).forEach(u => map.set(u.ip, Object.assign({}, u, { override: null })));
  (ipState.overrides || []).forEach(o => {
    const cur = map.get(o.ip) || { ip: o.ip, tokens: 0, requests: 0, blocked: 0, active_ms: 0, policy: {}, last_seen: '' };
    cur.override = o;
    cur.overridden = true;
    if (!cur.policy || !cur.policy.source) cur.policy = Object.assign({}, cur.policy, { source: 'override' });
    map.set(o.ip, cur);
  });
  return [...map.values()].sort((a, b) => (b.tokens || 0) - (a.tokens || 0) || (b.requests || 0) - (a.requests || 0));
}
function ipStatus(r) {
  const p = r.policy || {};
  if (p.disabled || (r.override && r.override.disabled)) return ['err', '已封禁'];
  const limTok = Number(p.window_tokens || 0);
  if (limTok > 0 && (r.tokens || 0) >= limTok) return ['warn', '额度用尽'];
  const limMin = Number(p.max_active_minutes || 0);
  if (limMin > 0 && (r.active_ms || 0) >= limMin * 60000) return ['warn', '时长用尽'];
  return ['ok', '正常'];
}
function renderIPs() {
  const d = ipState.defaults || {};
  const rows = ipRows();
  const body = rows.map(r => {
    const [cls, txt] = ipStatus(r);
    const p = r.policy || {};
    const limTok = Number(p.window_tokens || 0);
    const limMin = Number(p.max_active_minutes || 0);
    const usedMin = Math.round((r.active_ms || 0) / 60000);
    const src = (r.override || (p.source === 'override')) ? '单独设置' : '继承默认';
    const note = r.override && r.override.note ? r.override.note : '';
    return '<tr>' +
      '<td class="mono">' + esc(r.ip) + (note ? '<div class="muted" style="font-size:12px">' + esc(note) + '</div>' : '') + '</td>' +
      '<td><span class="badge ' + cls + '">' + txt + '</span></td>' +
      '<td style="min-width:130px"><div class="bar ' + (limTok && r.tokens >= limTok ? 'err' : '') + '"><i style="width:' + (limTok ? pct(r.tokens, limTok) : 0) + '%"></i></div>' +
      '<div class="muted" style="font-size:12px">' + fmtTokens(r.tokens || 0) + ' / ' + (limTok ? fmtTokens(limTok) : '不限') + '</div></td>' +
      '<td>' + usedMin + ' / ' + (limMin ? limMin : '不限') + ' 分钟</td>' +
      '<td>' + fmtNum(r.requests || 0) + ' 次<span class="muted"> · 拦 ' + fmtNum(r.blocked || 0) + '</span></td>' +
      '<td>' + (p.output_tps ? p.output_tps + ' token/s' : '不限') + '</td>' +
      '<td>' + esc(src) + '</td>' +
      '<td class="muted" style="font-size:12px">' + esc(fmtDateTime(r.last_seen)) +
      (r.reset_at ? '<br>重置 ' + esc(fmtResetAt(r.reset_at)) : '') + '</td>' +
      '<td><button class="btn tiny" onclick="openIPEditor(\'' + esc(r.ip) + '\')">编辑</button> ' +
      '<button class="btn tiny" onclick="resetIPWindow(\'' + esc(r.ip) + '\')">重置</button> ' +
      '<button class="btn tiny" onclick="toggleIPBan(\'' + esc(r.ip) + '\')">' + (ipStatus(r)[0] === 'err' ? '解封' : '封禁') + '</button>' +
      (r.override ? ' <button class="btn tiny danger" onclick="deleteIPOverride(\'' + esc(r.ip) + '\')">删策略</button>' : '') +
      '</td></tr>';
  }).join('');
  el('ips').innerHTML =
    '<div class="page-head"><h2>IP 管理</h2><div class="row">' +
    '<button class="btn tiny" onclick="loadIPs()">刷新</button>' +
    '<button class="btn primary tiny" onclick="openIPEditor(\'\')">新增 IP 策略</button></div></div>' +
    '<div class="card" style="margin-bottom:14px"><h3>默认策略（所有 IP 生效，可被单独设置覆盖）</h3>' +
    '<div class="row form-grid" style="align-items:flex-end">' +
    '<div><label class="f">窗口 token 上限（万）</label><input id="defTokens" type="number" min="0" value="' + tokens2wan(d.window_tokens) + '"></div>' +
    '<div><label class="f">窗口长度（分钟）</label><input id="defWindow" type="number" min="0" value="' + esc(d.window_minutes) + '">' +
    '<div class="muted" style="font-size:11px;margin-top:3px">1440 = 一天（08:00→次日 08:00）</div></div>' +
    '<div><label class="f">活跃时长上限（分钟）</label><input id="defActive" type="number" min="0" value="' + esc(d.max_active_minutes) + '"></div>' +
    '<div><label class="f">输出限速（token/秒）</label><input id="defTps" type="number" min="0" value="' + esc(d.output_tps) + '"></div>' +
    '<button class="btn primary full" onclick="saveIPDefaults()">保存默认策略</button></div>' +
    '<div class="hint" style="margin-top:10px">这些是<b>按 IP</b> 统计的防滥用限制（跨该 IP 的所有 Key 汇总）。' +
    '窗口按每日 ' + esc(String(claimRule.reset_hour || 8).padStart(2, '0')) + ':00 边界对齐，到点自动清零。' +
    '<br><b>窗口 token 上限</b>：该 IP 一个窗口内的总消耗上限（输入+输出累加）。填 0 = 不限 —— ' +
    '额度通常由「参数设置 → 默认额度（每个 Key）」统一控制，这里一般保持 0，避免两处额度错配。' +
    '<br><b>窗口长度</b>：决定额度多久清零一次。1440 分钟 = 一天（08:00 → 次日 08:00）。' +
    '<br><b>活跃时长上限</b>：该 IP 一天内<b>所有请求实际耗时的累加</b>上限（不是从第一次调用起的墙钟时间）。' +
    '用于防止脚本挂着慢速流式请求占住连接 —— 这种请求 token 涨得慢但连接一直占着。' +
    '<br><b>输出限速</b>：每秒最多输出多少 token（令牌桶，只管输出不管输入），防止单人把上游打满。' +
    '<br>触发限制时会返回具体原因与重置时间；领取 Key 页面不展示这些规则。</div></div>' +
    '<div class="card" style="margin-bottom:14px"><h3>领取规则（对使用者公开）</h3>' +
    '<div class="row form-grid" style="align-items:flex-end">' +
    '<div style="grid-column:span 2"><label class="f">允许领取的来源网段（逗号分隔，留空 = 全部拒绝）</label>' +
    '<input id="claimCidrs" value="' + esc((claimRule.allow_cidrs || []).join(', ')) + '" placeholder="192.168.12.0/24, 10.0.0.0/8"></div>' +
    '<div style="grid-column:span 2"><label class="f" style="display:flex;gap:8px;align-items:center;margin-top:0">' +
    '<input type="checkbox" id="claimOneKey"' + (claimRule.one_key_per_ip ? ' checked' : '') + '> 每个 IP 固定 1 个 Key（重复领取拿回同一个）</label></div>' +
    '<button class="btn primary" onclick="saveClaimPolicy()">保存领取规则</button></div>' +
    '<div class="hint" style="margin-top:10px">网段白名单用于挡住走代理/外网进来的人：不在名单内的来源直接拒绝领取。' +
    '<br><b>留空 = 全部拒绝</b>（fail-closed）；确实要放开所有来源请显式填 <code>0.0.0.0/0, ::/0</code>。' +
    '任何一项格式非法都会被拒绝保存，且原白名单保持不变。' +
    '额度与重置时刻不在这里配 —— 额度统一用「参数设置」里的默认额度（每日 08:00 重置）。' +
    '今日已领取 ' + (claimRule.claimed_count || 0) + ' 个 IP。</div></div>' +
    '<div class="card table-wrap"><table><thead><tr><th>IP</th><th>状态</th><th>窗口 token</th><th>活跃时长</th><th>请求/拦截</th><th>限速</th><th>策略来源</th><th>最近活跃</th><th>操作</th></tr></thead>' +
    '<tbody>' + (body || '<tr><td colspan="9" class="empty">暂无 IP 记录</td></tr>') + '</tbody></table></div>';
}
async function saveIPDefaults() {
  const body = {
    window_tokens: wan2tokens(el('defTokens').value),
    window_minutes: Number(el('defWindow').value || 0),
    max_active_minutes: Number(el('defActive').value || 0),
    output_tps: Number(el('defTps').value || 0)
  };
  const { ok, data } = await api('api/admin/ip-policies/defaults', 'PUT', body);
  if (!ok) return fail(data);
  toast('默认策略已保存', 'ok');
  loadIPs();
}
function overrideBody(ip, patch) {
  const cur = (ipState.overrides || []).find(o => o.ip === ip) || {};
  const body = {
    window_tokens: cur.window_tokens == null ? null : cur.window_tokens,
    window_minutes: cur.window_minutes == null ? null : cur.window_minutes,
    max_active_minutes: cur.max_active_minutes == null ? null : cur.max_active_minutes,
    output_tps: cur.output_tps == null ? null : cur.output_tps,
    disabled: !!cur.disabled,
    note: cur.note || ''
  };
  return Object.assign(body, patch || {});
}
function openIPEditor(ip) {
  const cur = ip ? (ipState.overrides || []).find(o => o.ip === ip) : null;
  const usage = ip ? (ipState.usage || []).find(u => u.ip === ip) : null;
  const def = ipState.defaults || {};
  const val = (v, fallback) => v == null ? '' : String(v);
  openModal(
    '<h3>' + (ip ? '编辑 IP 策略 · ' + esc(ip) : '新增 IP 策略') + '</h3>' +
    '<div class="hint">' + (ip ? '留空表示继承默认策略。' : '为指定 IP 单独设置额度与限速。') + '</div>' +
    '<label class="f">IP 地址</label><input id="ipTarget" value="' + esc(ip || '') + '"' + (ip ? ' disabled' : '') + ' placeholder="如 203.0.113.7">' +
    '<label class="f">窗口 token 上限（万，留空=继承）</label><input id="ipTokens" type="number" min="0" value="' + esc(val(cur && cur.window_tokens, null)) + '">' +
    '<label class="f">窗口长度（分钟，留空=继承）</label><input id="ipWindow" type="number" min="0" value="' + esc(val(cur && cur.window_minutes, null)) + '">' +
    '<label class="f">活跃时长上限（分钟，留空=继承）</label><input id="ipActive" type="number" min="0" value="' + esc(val(cur && cur.max_active_minutes, null)) + '">' +
    '<label class="f">输出限速 token/秒（留空=继承）</label><input id="ipTps" type="number" min="0" value="' + esc(val(cur && cur.output_tps, null)) + '">' +
    '<label class="f" style="display:flex;gap:8px;align-items:center;margin-top:14px">' +
    '<input type="checkbox" id="ipDisabled"' + (cur && cur.disabled ? ' checked' : '') + '> 封禁该 IP</label>' +
    '<label class="f">备注</label><input id="ipNote" value="' + esc((cur && cur.note) || '') + '" placeholder="如：外包同学专用">' +
    (usage ? '<div class="hint" style="margin-top:10px">当前窗口已用 ' + fmtTokens(usage.tokens || 0) + ' token，' + Math.round((usage.active_ms || 0) / 60000) + ' 分钟活跃</div>' : '') +
    '<div class="hint" style="margin-top:8px">默认策略：' + fmtTokens(def.window_tokens) + ' token / ' + esc(def.window_minutes) + ' 分钟 / 活跃 ' + esc(def.max_active_minutes) + ' 分钟 / ' + esc(def.output_tps) + ' token/s</div>' +
    '<div class="actions"><button class="btn" onclick="closeModal()">取消</button><button class="btn primary" onclick="saveIPOverride()">保存</button></div>');
}
async function saveIPOverride() {
  const ip = (el('ipTarget').value || '').trim();
  if (!ip) return toast('请填写 IP 地址', 'err');
  const num = id => {
    const v = el(id).value.trim();
    if (v === '') return null;
    const n = Number(v);
    if (isNaN(n) || n < 0) return null;
    return n;
  };
  const patch = {
    window_tokens: num('ipTokens') === null ? null : wan2tokens(num('ipTokens')),
    window_minutes: num('ipWindow'),
    max_active_minutes: num('ipActive'),
    output_tps: num('ipTps'),
    disabled: el('ipDisabled').checked,
    note: el('ipNote').value.trim()
  };
  const { ok, data } = await api('api/admin/ip-policies/' + encodeURIComponent(ip), 'PUT', overrideBody(ip, patch));
  if (!ok) return fail(data);
  closeModal();
  toast('策略已保存', 'ok');
  loadIPs();
}
async function toggleIPBan(ip) {
  const cur = (ipState.overrides || []).find(o => o.ip === ip);
  const next = !(cur && cur.disabled);
  const { ok, data } = await api('api/admin/ip-policies/' + encodeURIComponent(ip), 'PUT', overrideBody(ip, { disabled: next }));
  if (!ok) return fail(data);
  toast(next ? '已封禁 ' + ip : '已解封 ' + ip, 'ok');
  loadIPs();
}
async function resetIPWindow(ip) {
  if (!await confirmBox('重置窗口', '将清零 ' + ip + ' 在当前窗口的 token 与活跃时长，确认？', '重置')) return;
  const { ok, data } = await api('api/admin/ip-policies/' + encodeURIComponent(ip) + '/reset', 'POST', {});
  if (!ok) return fail(data);
  toast('已重置窗口', 'ok');
  loadIPs();
}
async function deleteIPOverride(ip) {
  if (!await confirmBox('删除策略', '删除后 ' + ip + ' 恢复继承默认策略，确认？', '删除', true)) return;
  const { ok, data } = await api('api/admin/ip-policies/' + encodeURIComponent(ip), 'DELETE');
  if (!ok) return fail(data);
  toast('已删除策略', 'ok');
  loadIPs();
}

/* ---------- 模型管理（按上游分组） ---------- */
async function loadModels() {
  const { ok, data } = await api('api/admin/models');
  if (!ok) return fail(data);
  let html = '<div class="page-head"><h2>模型管理</h2><button class="btn primary tiny" onclick="saveModels()">保存白名单</button></div>';
  if (data.warning) html += '<div class="card" style="border-color:#fecaca;margin-bottom:12px;color:#b91c1c">' + esc(data.warning) + '</div>';
  const allowed = data.allowed || [];
  const groups = data.upstreams || [];
  if (!groups.length) {
    html += '<div class="card"><div class="empty">尚未配置上游，请先到「上游管理」新增</div></div>';
    el('models').innerHTML = html;
    return;
  }
  // 每个上游一张卡片；勾选项的 value 就是用户实际要填的 model 字符串（含前缀）。
  groups.forEach(g => {
    const prefix = g.prefix || '';
    html += '<div class="card"><h3>' + esc(g.name || g.id) + ' <span class="muted" style="font-weight:400">（id: ' + esc(g.id) +
      (prefix ? '，模型名前缀 <code>' + esc(prefix) + '</code>' : '，默认上游（无前缀）') + '）</span></h3>';
    if (g.warning) html += '<div class="hint" style="color:#b91c1c">' + esc(g.warning) + '</div>';
    if (!g.models || !g.models.length) {
      html += '<div class="empty">该上游暂无可用模型</div>';
    } else {
      html += '<div class="row" style="gap:16px;flex-wrap:wrap">' + g.models.map(m => {
        const full = prefix + m;
        return '<label style="display:flex;gap:6px;align-items:center;font-weight:400">' +
          '<input type="checkbox" class="model-cb" value="' + esc(full) + '"' +
          (allowed.includes(full) ? ' checked' : '') + '> ' + esc(full) + '</label>';
      }).join('') + '</div>';
    }
    html += '</div>';
  });
  el('models').innerHTML = html;
}
async function saveModels() {
  const allowed = [...document.querySelectorAll('.model-cb:checked')].map(b => b.value);
  const { ok, data } = await api('api/admin/models', 'POST', { allowed_models: allowed });
  if (!ok) return fail(data);
  toast('模型白名单已保存', 'ok');
}

/* ---------- 上游管理 ---------- */
let upstreamCache = [];
async function loadUpstreams() {
  const { ok, data } = await api('api/admin/upstreams');
  if (!ok) return fail(data);
  upstreamCache = data.upstreams || [];
  const defaultID = data.default_upstream_id || '';
  let html = '<div class="page-head"><h2>上游管理</h2>' +
    '<button class="btn primary tiny" onclick="editUpstream(null)">新增上游</button></div>';
  html += '<div class="card"><div class="hint">请求的 <code>model</code> 以「上游 ID/」开头时转发到该上游（前缀会被剥离）；' +
    '不带前缀或第一段不是上游 ID 时走默认上游。默认上游：<b>' + esc(defaultID || '未设置') + '</b>。</div></div>';
  if (!upstreamCache.length) {
    html += '<div class="card"><div class="empty">还没有上游，点击右上角「新增上游」开始</div></div>';
    el('upstreams').innerHTML = html;
    return;
  }
  html += '<div class="card"><table><thead><tr>' +
    '<th>ID</th><th>名称</th><th>Base URL</th><th>密钥</th><th>状态</th><th>操作</th>' +
    '</tr></thead><tbody>';
  upstreamCache.forEach(u => {
    const isDefault = u.id === defaultID;
    html += '<tr>' +
      '<td><code>' + esc(u.id) + '</code>' + (isDefault ? ' <span class="badge ok">默认</span>' : '') + '</td>' +
      '<td>' + esc(u.name || '') + '</td>' +
      '<td style="word-break:break-all">' + esc(u.base_url) + '</td>' +
      '<td>' + (u.api_key_set ? esc(u.api_key_masked) : '<span class="muted">未设置</span>') + '</td>' +
      '<td>' + (u.enabled ? '<span class="badge ok">启用</span>' : '<span class="badge">停用</span>') + '</td>' +
      '<td class="row" style="gap:6px">' +
        '<button class="btn tiny" onclick="editUpstream(\'' + esc(u.id) + '\')">编辑</button>' +
        '<button class="btn tiny" onclick="testUpstream(\'' + esc(u.id) + '\')">测试</button>' +
        '<button class="btn tiny" onclick="toggleUpstream(\'' + esc(u.id) + '\',' + (!u.enabled) + ')">' +
          (u.enabled ? '停用' : '启用') + '</button>' +
        '<button class="btn tiny danger" onclick="deleteUpstream(\'' + esc(u.id) + '\')">删除</button>' +
      '</td></tr>';
  });
  html += '</tbody></table></div>';
  el('upstreams').innerHTML = html;
}

function editUpstream(id) {
  const u = id ? upstreamCache.find(x => x.id === id) : null;
  const isEdit = !!u;
  openModal(
    '<h3>' + (isEdit ? '编辑上游' : '新增上游') + '</h3>' +
    '<label class="f">ID（仅字母数字与 - _，用作模型名前缀）</label>' +
    '<input id="upId" value="' + esc(u ? u.id : '') + '" ' + (isEdit ? 'readonly' : '') + ' placeholder="如 backup">' +
    '<label class="f">名称</label>' +
    '<input id="upName" value="' + esc(u ? (u.name || '') : '') + '" placeholder="如 备用上游">' +
    '<label class="f">Base URL</label>' +
    '<input id="upURL" value="' + esc(u ? u.base_url : '') + '" placeholder="http://127.0.0.1:18080/v1">' +
    '<label class="f">API Key<span class="muted" style="font-weight:400"> ' +
      (isEdit ? '（已配置：' + esc(u.api_key_masked || '未设置') + '，留空则保持不变）' : '') + '</span></label>' +
    '<input id="upKey" type="password" value="" placeholder="' + (isEdit ? '输入新密钥以更换' : '上游密钥') + '">' +
    '<label class="f" style="display:flex;gap:6px;align-items:center;font-weight:400">' +
      '<input id="upEnabled" type="checkbox" ' + ((!u || u.enabled) ? 'checked' : '') + '> 启用</label>' +
    '<div class="actions"><button class="btn" onclick="closeModal()">取消</button>' +
    '<button class="btn primary" onclick="saveUpstream(' + (isEdit ? 'true' : 'false') + ')">保存</button></div>'
  );
}

async function saveUpstream(isEdit) {
  const id = el('upId').value.trim();
  const payload = {
    name: el('upName').value.trim(),
    base_url: el('upURL').value.trim(),
    api_key: el('upKey').value.trim(),
    enabled: el('upEnabled').checked
  };
  let res;
  if (isEdit) {
    res = await api('api/admin/upstreams/' + encodeURIComponent(id), 'PUT', payload);
  } else {
    payload.id = id;
    res = await api('api/admin/upstreams', 'POST', payload);
  }
  if (!res.ok) return fail(res.data);
  closeModal();
  toast(isEdit ? '上游已更新' : '上游已新增', 'ok');
  loadUpstreams();
}

async function toggleUpstream(id, enabled) {
  const { ok, data } = await api('api/admin/upstreams/' + encodeURIComponent(id), 'PUT', { enabled });
  if (!ok) return fail(data);
  toast(enabled ? '已启用' : '已停用', 'ok');
  loadUpstreams();
}

async function deleteUpstream(id) {
  if (!await confirmBox('删除上游', '删除后使用「' + id + '/」前缀的请求将回落到默认上游，确认？', '删除', true)) return;
  const { ok, data } = await api('api/admin/upstreams/' + encodeURIComponent(id), 'DELETE');
  if (!ok) return fail(data);
  toast('已删除', 'ok');
  loadUpstreams();
}

async function testUpstream(id) {
  toast('正在测试…');
  const { ok, data } = await api('api/admin/upstreams/' + encodeURIComponent(id) + '/test', 'POST');
  if (!ok) return fail(data);
  if (data.ok) toast('连接正常，可用模型 ' + data.model_count + ' 个', 'ok');
  else toast(data.warning || '连接失败', 'err');
}

/* ---------- 参数设置 ---------- */
async function loadSettings() {
  const { ok, data } = await api('api/admin/settings');
  if (!ok) return fail(data);
  lastKnownQuota = Number(data.token_quota || 0);
  el('settings').innerHTML =
    '<div class="page-head"><h2>参数设置</h2></div>' +
    '<div class="grid c2">' +
    '<div class="card"><h3>额度与访问</h3>' +
    '<div class="hint">上游地址与密钥已移至「上游管理」页，可配置多个上游并按 model 前缀路由。' +
    '当前默认上游：<b>' + esc(data.default_upstream_id || '未设置') + '</b>' +
    '（共 ' + ((data.upstreams || []).length) + ' 个）。</div>' +
    '<button class="btn tiny" onclick="switchView(\'upstreams\')">前往上游管理</button>' +
    '<label class="f">对外访问地址（领 Key 时下发给使用者，留空自动推断）</label>' +
    '<input id="publicBase" value="' + esc(data.public_base_url || '') + '" placeholder="如 http://192.168.1.20:23081">' +
    '<div class="hint">留空时：反代场景取 X-Forwarded-Host；本机直连会把 127.0.0.1 换成本机内网 IP（当前探测到 ' + esc(data.lan_ip || '无') + '）。</div>' +
    '<label class="f">Key 有效期（天，0 = 永不过期）</label>' +
    '<input id="ttl" type="number" min="0" value="' + esc(data.ttl_days) + '">' +
    '<div class="hint" style="margin-top:2px">填 0 时 Key 长期有效，用量由「每日 08:00 自动重置的额度」控制（推荐）。</div>' +
    '<label class="f">默认额度（每个 Key 每日 token 上限，全局唯一来源）</label>' +
    '<input id="quota" type="number" value="' + esc(data.token_quota) + '">' +
    '<div class="hint" style="margin-top:2px">所有 Key 每日 08:00 重置后都恢复到这个额度；领取页只显示「默认额度」，不显示具体数值。' +
    '<br><b>修改后次日 08:00 生效</b>：当天在用的 Key 不受影响（已用掉的额度不作废），新领的 Key 立即按新额度发放。</div>' +
    '<label class="f">单 Key 每分钟请求上限</label><input id="rate" type="number" value="' + esc(data.rate_limit_per_min) + '">' +
    '<div class="row" style="margin-top:14px"><button class="btn primary" onclick="saveSettings()">保存参数</button></div>' +
    '</div>' +
    '<div class="card"><h3>管理员密码</h3>' +
    '<div class="hint">修改密码后，除当前浏览器外的其它登录会话会立即失效。</div>' +
    '<div class="hint">强度要求：至少 8 位（建议 12 位以上），需包含大写字母、小写字母、数字、符号中的至少 3 类，不能是纯数字、纯字母、同一字符重复或常见弱口令，也不能与当前密码相同或过于相似。</div>' +
    '<label class="f">新密码（至少 8 位，建议 12 位以上）</label><input id="newpw" type="password" placeholder="输入新密码">' +
    '<div class="row" style="margin-top:12px"><button class="btn primary" onclick="savePassword()">修改密码</button></div>' +
    '<h3 style="margin-top:22px">IP 策略</h3>' +
    '<div class="hint">默认策略与单 IP 覆盖在「IP 管理」页配置。</div>' +
    '<button class="btn" onclick="switchView(\'ips\')">前往 IP 管理</button>' +
    '</div></div>';
}
async function saveSettings() {
  // 上游地址/密钥不再经本接口提交（已拆到「上游管理」）。
  const { ok, data } = await api('api/admin/settings', 'PUT', {
    public_base_url: el('publicBase') ? el('publicBase').value.trim() : '',
    ttl_days: Number(el('ttl').value),
    token_quota: Number(el('quota').value),
    rate_limit_per_min: Number(el('rate').value)
  });
  if (!ok) return fail(data);
  const quotaChanged = Number(el('quota').value) !== lastKnownQuota;
  toast(quotaChanged ? '参数已保存（额度将于次日 08:00 生效）' : '参数已保存', 'ok');
  loadSettings();
}
async function savePassword() {
  const pw = el('newpw').value;
  if (!pw || pw.length < 8) return toast('密码至少 8 位（建议 12 位以上，需包含大写字母、小写字母、数字、符号中的至少 3 类）', 'err');
  const { ok, data } = await api('api/admin/password', 'PUT', { password: pw });
  if (!ok) return fail(data);
  el('newpw').value = '';
  toast('密码已修改，其它会话已失效', 'ok');
}

/* ---------- AI 问答 ---------- */
const ASK_SUGGESTIONS = [
  '今天哪个 IP 用得最多？有没有异常？',
  '按每人每周 2000 万 token 算，建议的单 IP 日窗口上限设多少？',
  '最近 7 天的错误主要来自哪里？',
  '现在的限速和窗口设置够用吗？需要调整吗？'
];
async function loadAsk() {
  // 每次进入都确保骨架存在：登录过期会让 api() 触发 logout()，DOM 可能已被重建
  if (!el('chatBox')) {
    el('ask').innerHTML =
      '<div class="page-head"><h2>AI 问答</h2>' +
        '<div class="ask-model" id="askModelBar"></div>' +
        '<span class="muted" style="font-size:12px">基于本服务实际统计数据回答</span></div>' +
      '<div class="card"><div class="chat" id="chatBox"></div>' +
      '<div class="chips" id="askChips">' + ASK_SUGGESTIONS.map(s => '<button class="chip" onclick="askQuick(\'' + esc(s) + '\')">' + esc(s) + '</button>').join('') + '</div>' +
      '<div class="chat-in"><input id="chatIn" placeholder="例如：今天哪个 IP 用得最多？" onkeydown="if(event.key===\'Enter\')sendAsk()">' +
      '<button class="btn primary" onclick="sendAsk()">发送</button></div></div>';
    el('ask').dataset.ready = '1';
    renderAsk();
  }
  loadAskModels();
}

// loadAskModels 拉取全部已启用上游的模型，供 AI 问答选择。
// 数据源复用「模型管理」的分组接口（每个上游实际提供的模型清单）。
async function loadAskModels() {
  const { ok, data } = await api('api/admin/models');
  if (!ok) { renderAskModelBar(); return; }
  const list = [];
  (data.upstreams || []).forEach(g => {
    if (!g.enabled) return;
    (g.models || []).forEach(m => list.push((g.prefix || '') + m));
  });
  askState.models = list;
  // 未选过或已失效时，回退到白名单第一个，再不行取列表第一个
  if (!askState.model || !list.includes(askState.model)) {
    const allowed = data.allowed || [];
    askState.model = allowed.find(a => list.includes(a)) || list[0] || '';
  }
  renderAskModelBar();
}

function renderAskModelBar() {
  const bar = el('askModelBar');
  if (!bar) return;
  const total = askState.models.length;
  const q = askState.modelQuery.trim().toLowerCase();
  const shown = q ? askState.models.filter(m => m.toLowerCase().includes(q)) : askState.models;
  let html = '<div class="ask-model-row">' +
    '<span class="ask-model-label">模型</span>' +
    '<button type="button" class="ask-model-pick" onclick="toggleAskModel()">' +
      '<span class="mono">' + esc(askState.model || '未选择') + '</span>' +
      '<span class="ask-caret">' + (askState.modelOpen ? '▴' : '▾') + '</span>' +
    '</button>' +
    '<span class="muted" style="font-size:11.5px">共 ' + total + ' 个可选</span>' +
    '</div>';
  if (askState.modelOpen) {
    html += '<div class="ask-model-pop">' +
      '<input id="askModelSearch" class="ask-model-search" placeholder="搜索模型…" value="' + esc(askState.modelQuery) + '" oninput="onAskModelSearch(this.value)">' +
      '<div class="ask-model-list">' +
      (shown.length
        ? shown.map(m => '<button type="button" class="ask-model-item' + (m === askState.model ? ' active' : '') + '" onclick="pickAskModel(\'' + esc(m).replace(/'/g, '&#39;') + '\')">' + esc(m) + '</button>').join('')
        : '<div class="empty" style="padding:10px">没有匹配的模型</div>') +
      '</div></div>';
  }
  bar.innerHTML = html;
  if (askState.modelOpen) {
    const s = el('askModelSearch');
    if (s) { s.focus(); s.setSelectionRange(s.value.length, s.value.length); }
  }
}

function toggleAskModel() {
  askState.modelOpen = !askState.modelOpen;
  askState.modelQuery = '';
  renderAskModelBar();
}

function onAskModelSearch(v) {
  askState.modelQuery = v || '';
  renderAskModelBar();
}

function pickAskModel(m) {
  askState.model = m;
  askState.modelOpen = false;
  askState.modelQuery = '';
  renderAskModelBar();
}
function renderAsk() {
  const box = el('chatBox');
  if (!box) return;
  box.innerHTML = askState.messages.map((m, mi) => {
    let body = m.role === 'ai' ? md(m.text) : esc(m.text);
    // AI 回答附带「查询了哪些数据」：让管理员知道结论的依据，也便于判断是否查全。
    if (m.role === 'ai' && m.toolCalls && m.toolCalls.length) {
      body += '<div class="ask-tools"><span class="ask-tools-label">查询 ' +
        m.toolCalls.length + ' 次</span>' +
        m.toolCalls.map(c => '<code class="ask-tool">' + esc(c) + '</code>').join('') + '</div>';
    }
    if (m.role === 'ai' && m.suggestions && m.suggestions.length) {
      body += renderAskSuggestions(m, mi);
    }
    return '<div class="bubble ' + (m.role === 'me' ? 'me' : (m.role === 'err' ? 'err' : 'ai')) + '">' + body + '</div>';
  }).join('')
    + (askState.busy ? '<div class="bubble ai"><span class="dots">正在分析</span></div>' : '');
  box.scrollTop = box.scrollHeight;
}

// renderAskSuggestions 渲染 AI 提议的配置修改。
// 关键点：这些值此时**还没有写入配置**，卡片上必须明示「待确认」，
// 只有点「应用」才会调后端真正落盘（方案 A）。
function renderAskSuggestions(m, mi) {
  return '<div class="ask-sugs">' + m.suggestions.map((s, si) => {
    const st = s.state || 'pending';
    let html = '<div class="ask-sug' + (st === 'applied' ? ' applied' : (st === 'ignored' ? ' ignored' : '')) + '">' +
      '<div class="ask-sug-head">' + esc(s.label || s.target) +
      (st === 'pending' ? '<span class="ask-sug-badge">待确认</span>' : '') + '</div>' +
      '<div class="ask-sug-diff">' +
        '<span class="old">' + esc(s.current || '（空）') + '</span>' +
        '<span class="arrow">→</span>' +
        '<span class="new">' + esc(s.proposed) + '</span>' +
      '</div>' +
      (s.reason ? '<div class="ask-sug-reason">' + esc(s.reason) + '</div>' : '');
    if (st === 'pending') {
      html += '<div class="ask-sug-actions">' +
        '<button type="button" class="btn primary" onclick="applySuggestion(' + mi + ',' + si + ')">应用</button>' +
        '<button type="button" class="btn" onclick="ignoreSuggestion(' + mi + ',' + si + ')">忽略</button>' +
        '</div>';
    } else if (st === 'applied') {
      html += '<div class="ask-sug-done">已应用，当前值：' + esc(s.appliedAs || s.proposed) + '</div>';
    } else {
      html += '<div class="ask-sug-done" style="color:#94a3b8">已忽略，未修改配置</div>';
    }
    return html + '</div>';
  }).join('') + '</div>';
}

async function applySuggestion(mi, si) {
  const m = askState.messages[mi];
  const s = m && m.suggestions ? m.suggestions[si] : null;
  if (!s || s.state === 'applied' || s.applying) return;
  s.applying = true;
  const { ok, data } = await api('api/admin/ask/apply', 'POST', { target: s.target, value: s.value });
  s.applying = false;
  if (!ok) { fail(data); return; }
  s.state = 'applied';
  s.current = data.before;
  s.appliedAs = data.after;
  toast('已应用：' + (data.label || s.label) + ' ' + data.before + ' → ' + data.after, 'ok');
  renderAsk();
  refreshAfterSuggestion(s.target);
}

function ignoreSuggestion(mi, si) {
  const m = askState.messages[mi];
  const s = m && m.suggestions ? m.suggestions[si] : null;
  if (!s || s.state === 'applied') return;
  s.state = 'ignored';
  renderAsk();
}

// refreshAfterSuggestion 让其他面板显示新值，避免「问答里改了、设置页还是旧的」。
function refreshAfterSuggestion(target) {
  if (!target) return;
  if (target.indexOf('ip_') === 0) {
    if (typeof loadIPs === 'function') loadIPs();
    return;
  }
  if (target === 'allowed_models') {
    if (typeof loadModels === 'function') loadModels();
    loadAskModels();
    return;
  }
  if (typeof loadSettings === 'function') loadSettings();
}

// md 把模型返回的 Markdown 渲染成可读排版：先转义，再处理标题/表格/列表/加粗/行内码。
function md(src) {
  const lines = esc(src || '').split('\n');
  const out = [];
  let inTable = false, inList = false;
  const inline = s => s
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/`([^`]+)`/g, '<code>$1</code>');
  const closeList = () => { if (inList) { out.push('</ul>'); inList = false; } };
  const closeTable = () => { if (inTable) { out.push('</tbody></table>'); inTable = false; } };
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i];
    const line = raw.trim();
    if (!line) { closeList(); closeTable(); continue; }
    if (/^\|.*\|$/.test(line)) {
      const cells = line.slice(1, -1).split('|').map(c => c.trim());
      if (/^[-:\s|]+$/.test(line)) continue; // 分隔行
      if (!inTable) {
        closeList();
        out.push('<table class="md-table"><thead><tr>' + cells.map(c => '<th>' + inline(c) + '</th>').join('') + '</tr></thead><tbody>');
        inTable = true;
      } else {
        out.push('<tr>' + cells.map(c => '<td>' + inline(c) + '</td>').join('') + '</tr>');
      }
      continue;
    }
    closeTable();
    const h = line.match(/^(#{1,4})\s+(.*)$/);
    if (h) { closeList(); out.push('<div class="md-h">' + inline(h[2]) + '</div>'); continue; }
    const li = line.match(/^[-*]\s+(.*)$/);
    if (li) {
      if (!inList) { out.push('<ul class="md-list">'); inList = true; }
      out.push('<li>' + inline(li[1]) + '</li>');
      continue;
    }
    const on = line.match(/^(\d+)[.)]\s+(.*)$/);
    if (on) {
      if (!inList) { out.push('<ol class="md-list">'); inList = true; }
      out.push('<li>' + inline(on[2]) + '</li>');
      continue;
    }
    closeList();
    out.push('<p class="md-p">' + inline(line) + '</p>');
  }
  closeList();
  closeTable();
  return out.join('');
}
function askQuick(q) {
  el('chatIn').value = q;
  sendAsk();
}
async function sendAsk() {
  const input = el('chatIn');
  const q = (input.value || '').trim();
  if (!q || askState.busy) return;
  input.value = '';
  askState.messages.push({ role: 'me', text: q });
  askState.busy = true;
  askBusy(true);
  renderAsk();
  const { ok, data } = await api('api/admin/ask', 'POST', { question: q, model: askState.model });
  askState.busy = false;
  askBusy(false);
  if (ok) {
    askState.messages.push({
      role: 'ai',
      text: data.answer || '(空回答)',
      toolCalls: Array.isArray(data.tool_calls) ? data.tool_calls : [],
      suggestions: Array.isArray(data.suggestions) ? data.suggestions.map(s => Object.assign({ state: 'pending' }, s)) : []
    });
  } else {
    askState.messages.push({ role: 'err', text: (data && data.error) || '请求失败' });
  }
  renderAsk();
}
function askBusy(on) {
  const btn = document.querySelector('#ask .chat-in button');
  const input = el('chatIn');
  if (btn) { btn.disabled = !!on; btn.textContent = on ? '分析中…' : '发送'; }
  if (input) input.disabled = !!on;
}

/* ---------- 启动 ---------- */
(async function init() {
  bindBackLinks();
  if (token) {
    const r = await api('api/admin/overview');
    if (r.ok) { enter(); return; }
  }
  showLogin();
})();
