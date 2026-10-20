'use strict';
let state = {
}, loadedConfig = false, busy = false;
const $ = selector => document.querySelector(selector);
const labels = {
  stopped:'服务已停止',starting:'正在启动',running:'服务运行中',recovering:'正在恢复入口',error:'等待恢复',queued:'排队中',running_review:'审查中',completed:'已发布',failed:'失败',interrupted:'已中断',skipped:'已跳过'
};
function message(text, bad=false) {
  const el=$('#message');
  el.textContent=text;
  el.style.display='block';
  el.className=bad?'bad':'';
}
async function request(path, body) {
  const response=await fetch(path, body===undefined?{
  }:{
    method:'POST',headers:{
      'Content-Type':'application/json','X-CSRF-Token':state.csrf
    },body:JSON.stringify(body)
  });
  const result=await response.json();
  if(!response.ok)throw new Error(result.error||'请求失败');
  return result;
}
function cell(row, text) {
  const td=document.createElement('td');
  td.textContent=text;
  row.append(td);
  return td;
}
async function refresh() {
  try {
    state=await request('/api/status');
    $('#connection').textContent='管理器在线';
    $('#connection').className='badge good';
    $('#phase').textContent=labels[state.phase]||state.phase;
    $('#error').textContent=state.error||' ';
    $('#reviewer').textContent=state.pids.reviewer?'PID '+state.pids.reviewer:'已停止';
    $('#tunnel').textContent=state.pids.tunnel?'PID '+state.pids.tunnel:'已停止';
    $('#probe').textContent=state.phase==='running'?'连续失败 '+state.health_failures+'/3':'启动后每 30 秒签名探测';
    $('#queue').textContent=state.activity.filter(a=>a.state==='queued').length+' 等待';
    $('#endpoint').textContent=state.url?state.url+'/webhook/github':'服务启动后生成地址';
    $('#log-output').textContent=state.logs.join('\n')||'暂无日志';
    const rows=$('#reviews');
    rows.replaceChildren();
    $('#empty').hidden=state.activity.length>0;
    for(const item of state.activity){
      const row=document.createElement('tr');
      cell(row,item.repo+' #'+item.number);
      const sha=cell(row,'');
      const code=document.createElement('code');
      code.textContent=(item.head_sha||'待解析').slice(0,10);
      sha.append(code);
      const status=cell(row,'');
      const badge=document.createElement('span');
      badge.className='badge '+(item.state==='completed'?'good':item.state==='failed'?'bad':'');
      badge.textContent=item.state==='running'?'审查中':labels[item.state]||item.state;
      status.append(badge);
      if(item.state==='failed')badge.title='失败阶段：'+item.stage;
      cell(row,new Date(item.updated_at).toLocaleString('zh-CN',{
        hour12:false
      }));
      const actions=cell(row,'');
      if(item.comment_url){
        try{
          const url=new URL(item.comment_url);
          if(url.protocol==='https:'&&!url.username&&!url.password){
            const a=document.createElement('a');
            a.href=url.href;
            a.target='_blank';
            a.rel='noopener noreferrer';
            a.textContent='查看评论';
            actions.append(a);
          }
        }catch{
        }
      }
      const retry=document.createElement('button');
      retry.textContent='重新审查';
      retry.disabled=state.phase!=='running'||busy;
      retry.addEventListener('click',()=>action('retry',{
        repo:item.repo,number:item.number
      }));
      actions.append(retry);
      rows.append(row);
    }
    document.querySelectorAll('[data-action]').forEach(button=>{
      const action=button.dataset.action;button.disabled=busy||state.pending||(action==='start'&&state.desired)||(action!=='start'&&!state.desired);
    });
    $('#config-note').textContent=state.desired?'先在「服务与审查」中停止服务，再修改配置':'';
    $('#config-form button').disabled=state.desired||busy;
    $('#retry-form button').disabled=state.phase!=='running'||busy;
  }catch(error){
    $('#connection').textContent='管理器断开';
    $('#connection').className='badge bad';
    message(error.message,true);
  }
}
async function action(name,body={
}){
  busy=true;
  try{
    await request('/api/action/'+name,body);
    message(name==='retry'?'重新审查请求已提交，等待排队状态':'操作已提交，请查看服务状态和日志');
  }catch(error){
    message(error.message,true);
  }finally{
    busy=false;
    await refresh();
  }
}
async function config(){
  if(loadedConfig)return;
  try{
    const c=await request('/api/config');
    const f=$('#config-form');
    for(const name of ['provider','model','api_base','task_timeout','request_timeout'])f.elements[name].value=c.agent[name]||({
      provider:'openai-compatible',task_timeout:'20m',request_timeout:'5m'
    }[name]||'');
    for(const name of ['host','trigger_user'])f.elements[name].value=c.github[name]||'';
    for(const name of ['reviewer_account','admin_account'])f.elements[name].value=c.manager[name]||({
      reviewer_account:'raymojo42',admin_account:'fullac'
    }[name]);
    f.elements.api_key.placeholder=c.agent.api_key_set?'已配置 · 留空保留':'填写密钥';
    f.elements.webhook_secret.placeholder=c.github.webhook_secret_set?'已配置 · 留空保留':'填写密钥';
    f.elements.max_context_tokens.value=c.agent.max_context_tokens||40000;
    f.elements.port.value=c.server?.port||9968;
    f.elements.auto_start.checked=!!c.manager.auto_start;
    f.elements.hooks.value=Object.entries(c.manager.hooks||{
    }).map(([repo,id])=>repo+' '+id).join('\n');
    const select=$('#retry-repo');
    select.replaceChildren();
    for(const repo of c.github.repos||[]){
      const option=document.createElement('option');
      option.value=repo;
      option.textContent=repo;
      select.append(option);
    }loadedConfig=true;
  }catch(error){
    message(error.message,true);
  }
}
document.querySelectorAll('[data-tab]').forEach(button=>button.addEventListener('click',()=>{
  document.querySelectorAll('.view').forEach(view=>view.hidden=view.id!==button.dataset.tab);document.querySelectorAll('[data-tab]').forEach(tab=>tab.classList.toggle('selected',tab===button));$('#heading').textContent=button.textContent;config();
}));
document.querySelectorAll('[data-action]').forEach(button=>button.addEventListener('click',()=>action(button.dataset.action)));
$('#refresh').addEventListener('click',refresh);
$('#retry-form').addEventListener('submit',event=>{
  event.preventDefault();action('retry',{
    repo:$('#retry-repo').value,number:Number($('#retry-number').value)
  });
});
$('#config-form').addEventListener('submit',async event=>{
  event.preventDefault();const f=event.target;try{
    const hooks={
    };for(const line of f.elements.hooks.value.split('\n').map(s=>s.trim()).filter(Boolean)){
      const fields=line.split(/\s+/);if(fields.length!==2||!/^\d+$/.test(fields[1])||!Number.isSafeInteger(Number(fields[1])))throw new Error('每行填写 owner/repo 和 webhook ID');hooks[fields[0]]=Number(fields[1]);
    }const cfg={
      agent:{
      },github:{
        repos:Object.keys(hooks)
      },manager:{
        hooks,auto_start:f.elements.auto_start.checked
      },server:{
        host:'127.0.0.1',port:Number(f.elements.port.value)
      }
    };for(const name of ['provider','model','api_base','api_key','task_timeout','request_timeout'])cfg.agent[name]=f.elements[name].value.trim();cfg.agent.max_context_tokens=Number(f.elements.max_context_tokens.value);for(const name of ['host','trigger_user','webhook_secret'])cfg.github[name]=f.elements[name].value.trim();for(const name of ['reviewer_account','admin_account'])cfg.manager[name]=f.elements[name].value.trim();await request('/api/config',cfg);f.elements.api_key.value='';f.elements.webhook_secret.value='';loadedConfig=false;await config();message('配置已保存。启动服务后生效。');
  }catch(error){
    message(error.message,true);
  }
});
(async()=>{
  await refresh();await config();setInterval(refresh,3000);
})();
