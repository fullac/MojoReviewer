'use strict';
const UI = window.MojoUI;
const $ = selector => document.querySelector(selector);
const $$ = selector => [...document.querySelectorAll(selector)];
let state = {activity: [], pids: {}, logs: []};
let online = false, loadedConfig = false, busy = false, refreshing = false;
let filter = 'all', page = 1, spotlight = 0, paused = false;
let displayedLogs = [], lastRows = '', lastLogs = '', toastTimer, motion;
let selectedReview = null, lastDetail = '';
const stageLabels = {queued:'等待执行', prepare:'准备仓库', workspace:'准备仓库', memory:'读取项目记忆', model:'模型审查', publish:'发布评论', published:'评论已发布', shutdown:'运行中断'};
function node(tag, text, className) {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  if (className) el.className = className;
  return el;
}
function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', '#i-' + name); svg.append(use); svg.setAttribute('aria-hidden', 'true');
  return svg;
}
function badge(pair, filled = false) { return node('span', pair[0], 'status ' + pair[1] + (filled ? ' filled' : '')); }
function setStatus(selector, pair) { const el = $(selector); el.textContent = pair[0]; el.className = 'status ' + pair[1]; }
function message(text, bad = false) {
  clearTimeout(toastTimer);
  $('#message-text').textContent = text; $('#message').className = 'toast' + (bad ? ' bad' : ''); $('#message').hidden = false;
  if (!bad) toastTimer = setTimeout(() => { $('#message').hidden = true; }, 6000);
}
async function request(path, body) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 12000);
  try {
    const response = await fetch(path, {signal:controller.signal, ...(body === undefined ? {} : {
      method:'POST', headers:{'Content-Type':'application/json','X-CSRF-Token':state.csrf}, body:JSON.stringify(body)
    })});
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || '请求失败');
    return result;
  } finally { clearTimeout(timer); }
}
function dates(value) {
  const date = new Date(value);
  if (!value || Number.isNaN(date.getTime())) return ['—', ''];
  return [date.toLocaleTimeString('zh-CN', {hour12:false}), date.toLocaleDateString('zh-CN', {month:'2-digit',day:'2-digit'})];
}
function updateControls() {
  $$('[data-action]').forEach(button => {
    button.disabled = !UI.canAct(button.dataset.action, state, online, busy);
    if (button.dataset.action === 'start') button.hidden = Boolean(state.desired);
    if (button.dataset.action === 'stop') button.hidden = !state.desired;
  });
  $$('[data-open-retry]').forEach(button => { button.disabled = !UI.canAct('retry', state, online, busy); });
  $('#retry-form button[type=submit]').disabled = !UI.canAct('retry', state, online, busy) || !loadedConfig;
  $('#retry-note').textContent = !online ? '连接恢复后可发起审查。' : state.phase !== 'running' ? '请先在工作台启动服务，等待公网入口就绪。' : '加入队列后可在审查记录中跟进进度。';
  const locked = !online || state.desired || busy;
  $$('#config-form fieldset').forEach(fieldset => { fieldset.disabled = locked; });
  $('#config-form button').disabled = locked || !loadedConfig;
  $('#config-note').textContent = !online ? '管理器连接恢复后可编辑。' : state.desired ? '服务运行中。先返回工作台停止服务，再修改配置。' : '保存后，重新启动服务生效。';
  $('#copy-endpoint').disabled = !state.url;
}
function renderHealth() {
  const h = UI.health(state, online);
  setStatus('#connection', online ? ['管理器在线','good'] : ['连接已断开','bad']);
  setStatus('#phase', online ? UI.phaseStatus(state.phase) : ['状态未确认', 'neutral']);
  $('#offline').hidden = online;
  $('#error').hidden = !online || !state.error; $('#error').textContent = state.error || '';
  for (const name of ['reviewer','tunnel']) {
    $('#' + name).textContent = h[name][0]; $('#' + name).className = 'panel-value ' + h[name][1];
    $('#' + name + '-pid').textContent = state.pids[name] ? 'PID ' + state.pids[name] : '进程尚未启动';
  }
  $('#reviewer-summary').textContent = !online ? '等待重新连接' : !state.pids.reviewer ? '启动后自动接收新任务' :
    state.local_health_failures ? `本地探测失败 ${state.local_health_failures} / 3 次` : h.running ? `${h.running} 项审查正在执行` : '空闲，等待新任务';
  $('#probe').textContent = !online ? '等待重新连接' : state.health_failures ? `公网探测失败 ${state.health_failures} / 3 次` :
    state.hooks_pending ? '等待 webhook 同步完成' : state.phase === 'running' ? '签名探测正常 · 自动恢复已就绪' : '等待公网入口就绪';
  $('#queue').textContent = h.queued;
  $('#queue-summary').textContent = h.running ? `${h.running} 项执行中 · 串行处理` : '暂无执行中的审查';
  $('#queue-capacity').replaceChildren(...Array.from({length:32}, (_, i) => node('span', '', i < h.queued ? 'used' : '')));
  $('#endpoint').textContent = state.url ? state.url + '/webhook/github' : '服务启动后生成入口地址';
  $('#endpoint').title = $('#endpoint').textContent;
}
function renderSpotlight(animate = false) {
  const items = state.activity.slice(0, 8);
  spotlight = items.length ? Math.min(spotlight, items.length - 1) : 0;
  const item = items[spotlight];
  $('#spotlight-text').textContent = item ? `${item.repo} #${item.number}  ·  ${UI.reviewStatus(item.state)[0]}${item.stage ? '  /  ' + (stageLabels[item.stage] || item.stage) : ''}` : '第一条审查记录会出现在这里。';
  $('#spotlight-count').textContent = items.length ? `${String(spotlight + 1).padStart(2,'0')} / ${String(items.length).padStart(2,'0')}` : '';
  $('#spotlight-prev').disabled = items.length < 2; $('#spotlight-next').disabled = items.length < 2;
  if (animate && window.gsap && !matchMedia('(prefers-reduced-motion: reduce)').matches) gsap.fromTo('.spotlight-copy', {y:5,opacity:.4}, {y:0,opacity:1,duration:.25,overwrite:true});
}
function commentLink(item) {
  const href = UI.safeLink(item.comment_url);
  if (!href) return null;
  const link = node('a', '查看评论', 'row-link'); link.href = href; link.target = '_blank'; link.rel = 'noopener noreferrer'; link.append(icon('external')); return link;
}
function renderReviews(force = false) {
  const signature = JSON.stringify([state.activity, filter, $('#review-search').value, page]);
  if (!force && signature === lastRows) return;
  lastRows = signature;
  const all = state.activity;
  const result = UI.pageRows(UI.filterReviews(all, filter, $('#review-search').value), page);
  page = result.page;
  $('#review-count').textContent = all.length;
  $('#active-count').textContent = all.filter(x => ['queued','running'].includes(x.state)).length;
  $('#attention-count').textContent = all.filter(x => ['failed','interrupted'].includes(x.state)).length;
  const rows = document.createDocumentFragment();
  for (const item of result.rows) {
    const row = node('tr'), repo = node('td');
    const parts = item.repo.split('/');
    const name = node('div', parts.slice(1).join('/') || item.repo, 'repo-title'); name.append(node('span', '#' + item.number, 'pr-number'));
    repo.append(name, node('div', parts[0], 'repo-owner')); row.append(repo);
    const status = node('td'); status.append(badge(UI.reviewStatus(item.state), true)); row.append(status);
    const sha = node('td'); sha.append(node('code', (item.head_sha || '待解析').slice(0,8), 'sha-code')); row.append(sha);
    const date = dates(item.updated_at), time = node('td', date[0], 'time-cell'); time.append(node('small', date[1])); time.title = item.updated_at || ''; row.append(time);
    const actionCell = node('td'), actions = node('div', undefined, 'row-actions');
    const session = node('button','会话','row-detail'); session.setAttribute('aria-label', `查看 ${item.repo} #${item.number} 的会话记录`); session.addEventListener('click', () => showDetail(item,'session')); actions.append(session);
    const detail = node('button', '正文', 'row-detail'); detail.setAttribute('aria-label', `查看 ${item.repo} #${item.number} 的审查详情`); detail.addEventListener('click', () => showDetail(item)); actions.append(detail); actionCell.append(actions); row.append(actionCell); rows.append(row);
  }
  $('#reviews').replaceChildren(rows);
  $('#empty').hidden = result.total > 0;
  const filtered = filter !== 'all' || $('#review-search').value.trim();
  $('#empty-title').textContent = filtered ? '没有匹配的审查记录' : '还没有审查记录';
  $('#empty-description').textContent = filtered ? '试试其他仓库名称、PR 编号或状态。' : '配置仓库并启动服务后，新的 PR 会自动进入队列。';
  $('#clear-filters').hidden = !filtered;
  $('#result-count').textContent = result.total ? `显示 ${(page - 1) * 10 + 1}–${Math.min(page * 10,result.total)} 条，共 ${result.total} 条` : '0 条记录';
  $('#page-label').textContent = `${page} / ${result.pages}`;
  $('#previous-page').disabled = page <= 1; $('#next-page').disabled = page >= result.pages;
}
function renderDetail() {
  if (!selectedReview) return;
  const item = state.activity.find(x => x.id === selectedReview.id) || selectedReview;
  const signature = JSON.stringify([item, online, busy, state.phase, state.pending]);
  if (signature === lastDetail) return;
  lastDetail = signature;
  $('#detail-title').textContent = `${item.repo} #${item.number}`;
  const dl = node('dl');
  const fields = [['状态',UI.reviewStatus(item.state)[0]],['执行阶段',stageLabels[item.stage] || item.stage || '—'],['提交 SHA',item.head_sha || '待解析'],['更新时间',item.updated_at ? new Date(item.updated_at).toLocaleString('zh-CN',{hour12:false}) : '—']];
  if (item.reason) fields.push(['说明',item.reason]);
  for (const [label,value] of fields) dl.append(node('dt',label),node('dd',value));
  $('#detail-content').replaceChildren(dl);
  const actions = $('#detail-actions'); actions.replaceChildren();
  const retry = node('button','重新审查','button primary'); retry.disabled = !UI.canAct('retry',state,online,busy); retry.addEventListener('click', async () => {if(await action('retry',{repo:item.repo,number:item.number})) $('#detail-dialog').close();}); actions.append(retry);
  const link = commentLink(item); if (link) actions.append(link);
}
function showDetail(item, tab = 'content') { selectedReview = item; lastDetail = ''; renderDetail(); $('#detail-dialog').showModal(); window.MojoInspector.open(item,tab); }
function renderLogs(force = false) {
  if (!paused) displayedLogs = state.logs;
  const lines = UI.logLines(displayedLogs, $('#log-search').value);
  const signature = JSON.stringify(lines);
  $('#log-count').textContent = `${lines.length} / ${displayedLogs.length} 行${paused ? ' · 已暂停' : ''}`;
  setStatus('#log-stream', !online ? ['连接中断','bad'] : paused ? ['更新已暂停','warn'] : ['实时更新','good']);
  if (!force && signature === lastLogs) return;
  lastLogs = signature;
  const output = $('#log-output'), scrollTop = output.scrollTop, fragment = document.createDocumentFragment();
  if (!lines.length) fragment.append(node('div', $('#log-search').value ? '没有匹配的日志，试试其他关键词。' : '暂无日志。服务运行后，输出会出现在这里。', 'log-empty'));
  for (const line of lines) {const el = node('div',undefined,'log-line ' + line.level); el.append(node('span',line.number,'log-number'),node('span',line.text)); fragment.append(el);}
  output.replaceChildren(fragment);
  output.scrollTop = $('#follow-logs').checked && !paused ? output.scrollHeight : scrollTop;
}
async function refresh() {
  if (refreshing) return;
  refreshing = true; $('#refresh').disabled = true;
  try {
    state = await request('/api/status'); online = true;
    $('#last-updated').textContent = '同步于 ' + new Date().toLocaleTimeString('zh-CN',{hour12:false});
    renderReviews(); renderSpotlight();
    if (!loadedConfig) await config();
  } catch { online = false; $('#last-updated').textContent = '同步中断 · 显示上次数据'; }
  finally {
    renderHealth(); renderLogs(); updateControls();
    if ($('#detail-dialog').open) renderDetail();
    refreshing = false; $('#refresh').disabled = false;
  }
}
async function action(name, body = {}) {
  if (!UI.canAct(name, state, online, busy)) { message('当前服务状态暂不支持此操作。',true); return false; }
  busy = true; updateControls();
  try { await request('/api/action/' + name,body); message(name === 'retry' ? '审查请求已提交，可在记录中跟进执行进度。' : '操作已提交，服务状态将自动更新。'); return true; }
  catch (error) { message(error.name === 'AbortError' ? '请求超时，请检查当前状态后重试。' : error.message,true); return false; }
  finally { busy = false; await refresh(); updateControls(); }
}
async function config() {
  try {
    const c = await request('/api/config'), f = $('#config-form');
    const defaults = {provider:'openai-compatible',task_timeout:'20m',request_timeout:'5m'};
    for (const name of ['provider','model','api_base','task_timeout','request_timeout']) f.elements[name].value = c.agent[name] || defaults[name] || '';
    for (const name of ['host','trigger_user']) f.elements[name].value = c.github[name] || '';
    for (const name of ['reviewer_account','admin_account']) f.elements[name].value = c.manager[name] || (name === 'reviewer_account' ? 'raymojo42' : 'fullac');
    f.elements.api_key.placeholder = c.agent.api_key_set ? '已配置，留空保留' : '填写 API Key';
    f.elements.webhook_secret.placeholder = c.github.webhook_secret_set ? '已配置，留空保留' : '填写 webhook secret';
    f.elements.max_context_tokens.value = c.agent.max_context_tokens || 40000;
    f.elements.port.value = c.server?.port || 9968; f.elements.auto_start.checked = Boolean(c.manager.auto_start);
    f.elements.hooks.value = Object.entries(c.manager.hooks || {}).map(([repo,id]) => repo + ' ' + id).join('\n');
    const select = $('#retry-repo'), previous = select.value;
    select.replaceChildren(...(c.github.repos || []).map(repo => {const option = node('option',repo); option.value = repo; return option;}));
    if ((c.github.repos || []).includes(previous)) select.value = previous;
    loadedConfig = true;
  } catch { message('配置读取失败，请刷新后重试。',true); }
}
function motionSetup() {
  motion?.revert();
  if (!window.gsap || !window.ScrollTrigger || $('#overview').hidden) return;
  gsap.registerPlugin(ScrollTrigger); motion = gsap.matchMedia();
  motion.add('(prefers-reduced-motion: no-preference)', () => {
    gsap.fromTo('.hero-media', {scale:.8,opacity:.2}, {scale:1,opacity:1,duration:1.1,ease:'power2.out'});
    gsap.to('.hero-media img', {scale:1.08,opacity:.2,ease:'none',scrollTrigger:{trigger:'.hero',start:'top 90px',end:'bottom 80px',scrub:.7}});
  });
  motion.add('(max-width: 760px) and (prefers-reduced-motion: no-preference)', () => {
    const cards = $$('.health-panel');
    cards.slice(0,-1).forEach((card,i) => { ScrollTrigger.create({trigger:card,start:()=>'top ' + (134+i*10),endTrigger:'#health-grid',end:'bottom 380px',pin:true,pinSpacing:false,invalidateOnRefresh:true}); });
  });
}
function switchTab(id, updateHash = true) {
  if (!['overview','settings','logs'].includes(id)) id = 'overview';
  $$('.view').forEach(view => { view.hidden = view.id !== id; });
  $$('.topbar [data-tab]').forEach(button => {const active = button.dataset.tab === id; button.classList.toggle('selected',active); if(active) button.setAttribute('aria-current','page'); else button.removeAttribute('aria-current');});
  if (updateHash) history.replaceState(null,'','#' + id);
  window.scrollTo({top:0,behavior:'instant'}); motionSetup();
  if (id === 'logs') renderLogs(true);
}
async function copy(text, success) { try {await navigator.clipboard.writeText(text); message(success);} catch {message('未能复制，请选择文本后手动复制。',true);} }
$$('[data-tab]').forEach(button => button.addEventListener('click', () => switchTab(button.dataset.tab)));
window.addEventListener('hashchange', () => {
  const id = location.hash.slice(1);
  if (['overview','settings','logs'].includes(id)) switchTab(id,false);
});
$$('[data-action]').forEach(button => button.addEventListener('click', () => action(button.dataset.action)));
$$('[data-open-retry]').forEach(button => button.addEventListener('click', () => {$('#retry-dialog').showModal(); $('#retry-number').focus();}));
$$('[data-close-dialog]').forEach(button => button.addEventListener('click', () => button.closest('dialog').close()));
$('#dismiss-message').addEventListener('click', () => {$('#message').hidden = true;});
$('#jump-reviews').addEventListener('click', () => $('#review-section').scrollIntoView({behavior:matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth'}));
$('#refresh').addEventListener('click',refresh); $('#reconnect').addEventListener('click',refresh);
$('#copy-endpoint').addEventListener('click', () => copy($('#endpoint').textContent,'Webhook 地址已复制。'));
$$('.panel-summary').forEach(button => button.addEventListener('click', () => {
  const wasExpanded = button.getAttribute('aria-expanded') === 'true';
  $$('.panel-summary').forEach(other => {const expanded = other === button && !wasExpanded; other.setAttribute('aria-expanded',String(expanded)); other.closest('.health-panel').classList.toggle('expanded',expanded); $('#' + other.getAttribute('aria-controls')).hidden = !expanded;});
  $('#health-grid').classList.toggle('has-expanded',!wasExpanded); window.ScrollTrigger?.refresh();
}));
function setFilter(value) { filter = value; page = 1; $$('[data-filter]').forEach(button => {button.classList.toggle('selected',button.dataset.filter === value); button.setAttribute('aria-pressed',String(button.dataset.filter === value));}); renderReviews(true); }
$$('[data-filter]').forEach(button => button.addEventListener('click', () => setFilter(button.dataset.filter)));
$('#review-search').addEventListener('input', () => {page = 1; renderReviews(true);});
$('#clear-filters').addEventListener('click', () => {$('#review-search').value = ''; setFilter('all');});
$('#previous-page').addEventListener('click', () => {page--; renderReviews(true);});
$('#next-page').addEventListener('click', () => {page++; renderReviews(true);});
for (const [id,delta] of [['spotlight-prev',-1],['spotlight-next',1]]) $('#' + id).addEventListener('click', () => {const count = Math.min(8,state.activity.length); if(count) spotlight = (spotlight + delta + count) % count; renderSpotlight(true);});
$('#log-search').addEventListener('input', () => renderLogs(true));
$('#pause-logs').addEventListener('click', () => {paused = !paused; $('#pause-logs').textContent = paused ? '恢复更新' : '暂停更新'; $('#pause-logs').setAttribute('aria-pressed',String(paused)); renderLogs(true);});
$('#follow-logs').addEventListener('change', () => renderLogs(true));
$('#copy-logs').addEventListener('click', () => copy(UI.logLines(displayedLogs,$('#log-search').value).map(line => line.text).join('\n'),'当前筛选的日志已复制。'));
$('#retry-form').addEventListener('submit', async event => {
  event.preventDefault(); const number = Number($('#retry-number').value);
  if (!Number.isSafeInteger(number) || number < 1) {message('请输入有效的 PR 编号。',true); return;}
  if (await action('retry',{repo:$('#retry-repo').value,number})) {$('#retry-dialog').close(); $('#retry-number').value = ''; setFilter('all');}
});
$('#config-form').addEventListener('submit', async event => {
  event.preventDefault(); if (!online || state.desired || busy) return;
  const f = event.currentTarget;
  try {
    const hooks = UI.parseHooks(f.elements.hooks.value);
    const cfg = {agent:{},github:{repos:Object.keys(hooks)},manager:{hooks,auto_start:f.elements.auto_start.checked},server:{host:'127.0.0.1',port:Number(f.elements.port.value)}};
    for (const name of ['provider','model','api_base','api_key','task_timeout','request_timeout']) cfg.agent[name] = f.elements[name].value.trim();
    cfg.agent.max_context_tokens = Number(f.elements.max_context_tokens.value);
    for (const name of ['host','trigger_user','webhook_secret']) cfg.github[name] = f.elements[name].value.trim();
    for (const name of ['reviewer_account','admin_account']) cfg.manager[name] = f.elements[name].value.trim();
    busy = true; updateControls(); await request('/api/config',cfg);
    f.elements.api_key.value = ''; f.elements.webhook_secret.value = ''; loadedConfig = false; await config(); message('配置已保存。启动服务后生效。');
  } catch(error) {message(error.name === 'AbortError' ? '保存请求超时，请刷新确认配置。' : error.message,true);}
  finally {busy = false; updateControls();}
});
switchTab(location.hash.slice(1) || 'overview',false);
refresh(); setInterval(refresh,3000);
