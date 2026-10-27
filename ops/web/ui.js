/* Pure presentation helpers, shared by the browser and node:test. */
(function (root) {
  'use strict';
  const states = {
    queued: ['排队中', 'neutral'], running: ['审查中', 'active'], completed: ['已发布', 'good'],
    failed: ['失败', 'bad'], interrupted: ['已中断', 'warn'], skipped: ['已跳过', 'neutral']
  };
  const phases = {
    stopped: ['服务已停止', 'neutral'], starting: ['正在启动', 'warn'], running: ['服务运行中', 'good'],
    recovering: ['正在恢复入口', 'warn'], configuring: ['正在同步 webhook', 'warn'], error: ['等待恢复', 'bad']
  };
  function reviewStatus(value) { return states[value] || [value || '未知', 'neutral']; }
  function phaseStatus(value) { return phases[value] || ['读取中', 'neutral']; }
  function safeLink(value) {
    try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.href : null; }
    catch { return null; }
  }
  function filterReviews(rows, filter, query) {
    const text = query.trim().toLocaleLowerCase();
    return rows.filter(row => {
      const status = filter === 'all' || (filter === 'active' && ['queued', 'running'].includes(row.state)) ||
        (filter === 'attention' && ['failed', 'interrupted'].includes(row.state)) || (filter === 'completed' && row.state === 'completed');
      return status && (!text || `${row.repo} #${row.number} ${row.head_sha || ''}`.toLocaleLowerCase().includes(text));
    });
  }
  function pageRows(rows, requested, size = 10) {
    const pages = Math.max(1, Math.ceil(rows.length / size));
    const page = Math.max(1, Math.min(requested, pages));
    return {page, pages, rows: rows.slice((page - 1) * size, page * size), total: rows.length};
  }
  function health(state, online) {
    const rows = state.activity || [], pids = state.pids || {};
    const running = rows.filter(row => row.state === 'running').length;
    const queued = rows.filter(row => row.state === 'queued').length;
    let reviewer = !pids.reviewer ? ['未启动', 'neutral'] : state.local_health_failures ? ['响应异常', 'warn'] :
      state.phase === 'starting' ? ['启动中', 'warn'] : ['运行中', 'good'];
    let tunnel = !pids.tunnel ? ['未连接', 'neutral'] : state.health_failures ? ['连接异常', 'warn'] :
      state.hooks_pending ? ['同步中', 'warn'] : state.phase === 'running' ? ['已连通', 'good'] : ['恢复中', 'warn'];
    if (!online) reviewer = tunnel = ['待确认', 'neutral'];
    return {reviewer, tunnel, running, queued};
  }
  function canAct(action, state, online, busy) {
    if (!online || busy || state.pending) return false;
    if (action === 'start') return !state.desired;
    if (action === 'stop' || action === 'restart') return Boolean(state.desired);
    if (action === 'tunnel') return Boolean(state.desired && state.pids?.reviewer && state.phase === 'running');
    return state.phase === 'running';
  }
  function logLines(lines, query) {
    const text = query.trim().toLocaleLowerCase();
    return lines.map((text, index) => ({text, number: index + 1,
      level: /\b(ERR|ERROR)\b|level=ERROR|失败/.test(text) ? 'error' : /\bWARN(?:ING)?\b|重试/.test(text) ? 'warning' : ''
    })).filter(line => !text || line.text.toLocaleLowerCase().includes(text));
  }
  function parseHooks(value) {
    const hooks = {};
    for (const line of value.split('\n').map(s => s.trim()).filter(Boolean)) {
      const parts = line.split(/\s+/);
      if (parts.length !== 2 || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(parts[0]) ||
          !/^\d+$/.test(parts[1]) || !Number.isSafeInteger(Number(parts[1]))) {
        throw new Error('每行填写 owner/repo 和有效的 webhook ID');
      }
      if (Object.hasOwn(hooks, parts[0])) throw new Error('同一个仓库只填写一次');
      hooks[parts[0]] = Number(parts[1]);
    }
    return hooks;
  }
  const api = {reviewStatus, phaseStatus, safeLink, filterReviews, pageRows, health, canAct, logLines, parseHooks};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.MojoUI = api;
})(typeof window !== 'undefined' ? window : globalThis);
