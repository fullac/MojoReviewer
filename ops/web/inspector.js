/* Read-only inspector. Untrusted Markdown is sanitized; transcripts stay text. */
'use strict';
window.MojoInspector = (() => {
  const $ = selector => document.querySelector(selector);
  const el = (tag, text, cls) => { const n = document.createElement(tag); if(text !== undefined) n.textContent = text; if(cls) n.className = cls; return n; };
  let item, mode = 'content', epoch = 0, sid = '', source = 'live';
  const labels = {content:'消息正文',reasoning_content:'模型保存的推理',tool_calls:'工具调用',content_parts:'多模态内容',attachments:'附件记录',metadata:'消息元数据'};
  const roles = {user:'用户 · 审查任务',assistant:'Mojo · 助手',tool:'工具返回',system:'系统'};
  async function api(kind, params = {}) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(),45000);
    try {
      const response = await fetch('/api/reviews/' + kind + '?' + new URLSearchParams({id:item.id,...params}), {signal:controller.signal});
      const data = await response.json();
      if(!response.ok) throw new Error(data.error || '读取失败');
      return data;
    } finally {clearTimeout(timer);}
  }
  function notice(text, bad = false) { return el('p',text,'inspector-notice' + (bad ? ' bad' : '')); }
  function button(text, fn, cls = 'button secondary') {const b = el('button',text,cls); b.type = 'button'; b.addEventListener('click',fn); return b;}
  function errorText(error) {return error.name === 'AbortError' ? '读取超时，请重新加载。' : error.message;}
  function markdown(text) {
    const article = el('article',undefined,'review-markdown');
    if(!window.marked || !window.DOMPurify) {article.append(el('pre',text)); return article;}
    const clean = DOMPurify.sanitize(marked.parse(text), {
      ALLOWED_TAGS:['p','br','strong','em','del','code','pre','blockquote','ul','ol','li','h1','h2','h3','h4','h5','h6','hr','table','thead','tbody','tr','th','td','a'],
      ALLOWED_ATTR:['href','title','start'], RETURN_DOM_FRAGMENT:true
    });
    clean.querySelectorAll('a').forEach(a => {
      const href = window.MojoUI.safeLink(a.getAttribute('href'));
      if(href) {a.href = href; a.target = '_blank'; a.rel = 'noopener noreferrer';}
      else a.removeAttribute('href');
    });
    article.append(clean); return article;
  }
  function rawSection(title, text, open = false) {
    const d = el('details',undefined,'message-block'); d.open = open;
    d.append(el('summary',title),el('pre',text || '未保存')); return d;
  }
  async function showContent(ticket) {
    const body = $('#inspector-body');
    const data = await api('content'); if(ticket !== epoch) return;
    body.replaceChildren();
    if(!data.available) {body.append(notice(data.reason)); return;}
    const toolbar = el('div',undefined,'inspector-toolbar');
    toolbar.append(el('span',`GitHub 已发布正文 · ${data.author || '审查账户'}${data.updated_at ? ' · ' + new Date(data.updated_at).toLocaleString('zh-CN',{hour12:false}) : ''}`));
    toolbar.append(button('复制 Markdown',async () => {
      try {await navigator.clipboard.writeText(data.body); toolbar.append(el('span','已复制','copy-feedback'));}
      catch {toolbar.append(notice('复制失败，可展开原始 Markdown 手动选择。',true));}
    }));
    body.append(toolbar,markdown(data.body),rawSection('原始 Markdown',data.body));
  }
  function selector(label, options, selected, change) {
    const wrap = el('label',label), select = el('select');
    for(const option of options) {const n = el('option',option.label); n.value = option.id; select.append(n);}
    select.value = selected; select.addEventListener('change',() => change(select.value)); wrap.append(select); return wrap;
  }
  function sessionHeader(data) {
    const header = el('div',undefined,'session-header');
    const association = {exact:'已关联本次审查的会话',inferred:'历史记录未绑定会话 ID；按 PR 和时间推定，请核对任务消息。',ambiguous:'找到多个同 PR 会话，无法确定本次所属，请选择后核对任务消息。',missing:'没有找到本地保存的对应会话。'};
    header.append(notice(association[data.association] || '会话关联未确认'));
    const selects = el('div',undefined,'session-selects');
    if(data.candidates.length) {
      const options = data.candidates.map(c => ({id:c.id,label:`${c.id} · ${c.title} · ${c.message_count || 0} 条消息`}));
      if(!data.session_id) options.unshift({id:'',label:'选择会话…'});
      selects.append(selector('会话',options,data.session_id,value => {sid = value; source = 'live'; show(mode);}));
    }
    if(data.sources?.length) selects.append(selector('记录版本',data.sources,data.source,value => {source = value; show(mode);}));
    header.append(selects);
    for(const warning of data.warnings || []) header.append(notice(warning));
    header.append(el('p',data.scope_note + ' 凭据与本机用户目录已脱敏。','muted inspector-scope'));
    return header;
  }
  function contextView(data) {
    const fragment = document.createDocumentFragment(), context = data.context;
    if(context) {
      fragment.append(notice(`本次宿主输入快照 · ${context.provider} / ${context.model} · ${context.created_at}`));
      fragment.append(notice(`开始时已有 ${context.message_start} 条历史消息。会话可能复用并发生压缩；消息编号不是永久的轮次边界。`));
      fragment.append(rawSection('系统提示 · Mojo 实际配置',context.system_prompt,true),rawSection('任务提示 · 含审查元数据与项目记忆',context.task_prompt,true));
    } else fragment.append(notice('这条历史审查没有保存独立的宿主输入快照，无法还原当时完整系统提示。任务提示、工具结果与回复可在「会话记录」中查看。'));
    if(data.agent_system_prompt) fragment.append(rawSection('运行时保存的 Agent 系统提示',data.agent_system_prompt,true));
    fragment.append(notice('此处包含已持久化的上下文。SDK 每轮动态注入的内容、工具 schema 与实际发给模型的完整请求没有逐轮留档，不能从历史会话完整重建。'));
    return fragment;
  }
  function messageView(message, data, ticket) {
    const row = el('article',undefined,'chat-message role-' + (['user','assistant','tool','system'].includes(message.role) ? message.role : 'unknown'));
    const head = el('header',undefined,'chat-heading');
    head.append(el('strong',roles[message.role] || message.role),el('span',`#${message.index + 1}${message.name ? ' · ' + message.name : ''}${message.created_at ? ' · ' + new Date(message.created_at).toLocaleString('zh-CN',{hour12:false}) : ''}`)); row.append(head);
    if(message.error) row.append(notice(message.error + '；下方仅为已保存的预览。',true));
    for(const block of message.blocks) {
      const section = rawSection((labels[block.key] || block.key) + (block.key === 'content' ? ` · ${block.total.toLocaleString()} 字符` : ''),block.text,block.key === 'content' && message.role !== 'tool' && (message.role !== 'user' || block.total < 1800));
      const pre = section.querySelector('pre');
      if(block.has_more) {
        let offset = block.next_offset;
        const more = button(`继续读取（${offset.toLocaleString()} / ${block.total.toLocaleString()} 字符）`,async () => {
          more.disabled = true;
          try {
            const chunk = await api('message',{session:data.session_id,source:data.source,version:data.version,index:message.index,field:block.key,offset});
            if(ticket !== epoch) return;
            pre.append(document.createTextNode(chunk.text)); offset = chunk.next_offset;
            if(chunk.has_more) more.textContent = `继续读取（${offset.toLocaleString()} / ${chunk.total.toLocaleString()} 字符）`;
            else more.remove();
          } catch(error) {if(ticket === epoch) section.append(notice(errorText(error),true));}
          finally {more.disabled = false;}
        },'text-button load-block'); section.append(more);
      }
      row.append(section);
    }
    return row;
  }
  function appendMessages(data,ticket) {
    const list = $('#chat-messages');
    for(const m of data.messages) list.append(messageView(m,data,ticket));
    const footer = $('#chat-pagination'); footer.replaceChildren();
    footer.append(el('span',`已显示 ${data.next_offset} / ${data.total} 条消息`));
    if(data.has_more) {
      const more = button('加载后续消息', async () => {
        more.disabled = true;
        try {
          const next = await api('session',{session:data.session_id,source:data.source,version:data.version,offset:data.next_offset});
          if(ticket === epoch) appendMessages(next,ticket);
        } catch(error) {if(ticket === epoch) footer.append(notice(errorText(error),true));}
        finally {more.disabled = false;}
      }); footer.append(more);
    }
  }
  async function showSession(ticket) {
    const data = await api('session',{session:sid,source}); if(ticket !== epoch) return;
    sid = data.session_id;
    const body = $('#inspector-body'); body.replaceChildren(sessionHeader(data));
    if(!data.session_id) return;
    if(mode === 'context') {body.append(contextView(data)); return;}
    const note = el('div',undefined,'conversation-intro');
    note.append(el('h3','会话记录'),el('p',`按时间顺序 · 共 ${data.total} 条消息 · 工具返回默认折叠`)); body.append(note);
    const list = el('div',undefined,'chat-messages'); list.id = 'chat-messages';
    const footer = el('div',undefined,'chat-pagination'); footer.id = 'chat-pagination'; body.append(list,footer);
    appendMessages(data,ticket);
  }
  async function show(nextMode) {
    mode = nextMode; const ticket = ++epoch;
    document.querySelectorAll('[data-inspector-tab]').forEach(button => {const selected = button.dataset.inspectorTab === mode; button.classList.toggle('selected',selected); button.setAttribute('aria-pressed',String(selected));});
    const body = $('#inspector-body'); body.replaceChildren(notice(mode === 'content' ? '正在读取 GitHub 评论…' : '正在读取本地会话…')); body.scrollTop = 0;
    try {if(mode === 'content') await showContent(ticket); else await showSession(ticket);}
    catch(error) {if(ticket === epoch) body.replaceChildren(notice(errorText(error),true),button('重新加载',() => show(mode)));}
  }
  document.querySelectorAll('[data-inspector-tab]').forEach(button => button.addEventListener('click',() => show(button.dataset.inspectorTab)));
  $('#inspector-reload').addEventListener('click',() => show(mode));
  $('#detail-dialog').addEventListener('close',() => {epoch++;});
  return {open(value, tab = 'content') {item = value; sid = ''; source = 'live'; show(tab);}};
})();
