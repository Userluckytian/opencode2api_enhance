async function loadModels() {
  try {
    const res = await fetch('api/models');
    const data = await res.json();
    const sel = document.getElementById('model');
    sel.innerHTML = '';
    if (!data.models || !data.models.length) {
      const o = document.createElement('option');
      o.textContent = '（暂无可领取的模型）';
      sel.appendChild(o);
      document.getElementById('getKeyBtn').disabled = true;
      return;
    }
    data.models.forEach(m => {
      const o = document.createElement('option');
      o.value = m; o.textContent = m;
      sel.appendChild(o);
    });
  } catch (e) {
    const err = document.getElementById('error');
    err.textContent = '加载模型失败：' + e; err.classList.remove('hidden');
  }
}

// loadRules 拉取并展示领取规则（重置时刻、限领规则）。
// 额度数值不在这里展示 —— 统一由后台的「默认额度」配置项决定，
// 使用者只看到「默认额度」这个说法，具体数值属于内部配置。
async function loadRules() {
  const box = document.getElementById('rules');
  if (!box) return;
  try {
    const res = await fetch('api/claim-rules');
    const r = await res.json();
    const hour = Number(r.reset_hour || 8);
    const items = [];
    items.push('每个 Key 每日可用 <b>默认额度</b>（以管理员配置为准）');
    items.push('额度每日 <b>' + String(hour).padStart(2, '0') + ':00</b> 自动重置回默认额度');
    if (r.never_expires) items.push('Key <b>长期有效</b>，用完当天额度次日自动恢复');
    if (r.one_key_per_ip) items.push('每个 IP 固定 <b>1 个</b> Key，<b>无需每天重新领取</b>（重复点击会拿回同一个 Key）');
    box.innerHTML = '<ul class="rules">' + items.map(t => '<li>' + t + '</li>').join('') + '</ul>';
  } catch (e) {
    box.innerHTML = '<div class="muted" style="font-size:12px">规则加载失败</div>';
  }
}

async function getKey() {
  const model = document.getElementById('model').value;
  const err = document.getElementById('error');
  err.classList.add('hidden');
  try {
    const res = await fetch('api/keys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model })
    });
    const data = await res.json();
    if (!res.ok) {
      err.textContent = data.error || '领取失败'; err.classList.remove('hidden');
      return;
    }
    document.getElementById('baseUrl').textContent = data.base_url;
    document.getElementById('apiKey').textContent = data.key;
    document.getElementById('result').classList.remove('hidden');
    // 重复领取时拿到的是同一个 Key，额度每天 08:00 自动重置 —— 明确告诉用户。
    const tip = document.getElementById('resultTip');
    if (tip) {
      tip.textContent = data.is_new
        ? '已生成你的 Key。额度每天 08:00 自动重置回默认值，明天直接用这个 Key 即可，无需重新领取。'
        : '这是你原有的 Key（同一 IP 固定一个）。额度每天 08:00 自动重置回默认值，明天直接用这个 Key 即可，无需重新领取。';
      tip.classList.remove('hidden');
    }
    loadRules();
  } catch (e) {
    err.textContent = '请求失败：' + e; err.classList.remove('hidden');
  }
}

document.getElementById('getKeyBtn').addEventListener('click', getKey);
document.getElementById('adminLink').addEventListener('click', () => { location.href = 'admin.html'; });
document.getElementById('copyBtn').addEventListener('click', async () => {
  const text = document.getElementById('baseUrl').textContent + '\n' + document.getElementById('apiKey').textContent;
  const btn = document.getElementById('copyBtn');
  try {
    await navigator.clipboard.writeText(text);
    btn.textContent = '已复制';
    btn.style.background = '#0f766e';
    btn.style.color = '#fff';
  } catch (e) {
    btn.textContent = '复制失败，请手动选中';
  }
  setTimeout(() => {
    btn.textContent = '一键复制';
    btn.style.background = '';
    btn.style.color = '';
  }, 1500);
});

loadModels();
loadRules();