const test = require('node:test');
const assert = require('node:assert/strict');
const ui = require('./web/ui.js');
const rows = [
  {repo:'acme/Engine',number:21,head_sha:'abc123',state:'failed'},
  {repo:'acme/Engine',number:22,head_sha:'def456',state:'running'},
  {repo:'acme/Web',number:23,head_sha:'aabbcc',state:'completed'},
  {repo:'acme/Web',number:24,head_sha:'abc111',state:'interrupted'},
  {repo:'acme/Web',number:25,head_sha:'eeff00',state:'queued'}
];
test('search combines status with repository, PR and SHA, case insensitively',()=>{
  assert.deepEqual(ui.filterReviews(rows,'attention','ABC').map(r=>r.number),[21,24]);
  assert.deepEqual(ui.filterReviews(rows,'all','#23').map(r=>r.number),[23]);
  assert.deepEqual(ui.filterReviews(rows,'active',' engine ').map(r=>r.number),[22]);
  assert.equal(ui.filterReviews(rows,'completed','does-not-exist').length,0);
});
test('pagination clamps page when polling or filters reduce results',()=>{
  const page=ui.pageRows(rows,4,2);assert.equal(page.page,3);assert.deepEqual(page.rows,[rows[4]]);
  assert.deepEqual(ui.pageRows([],99),{page:1,pages:1,rows:[],total:0});
});
test('a live PID alone does not report healthy public connectivity',()=>{
  const base={pids:{reviewer:12,tunnel:13},activity:rows,phase:'configuring',hooks_pending:true};
  assert.deepEqual(ui.health(base,true).tunnel,['同步中','warn']);
  assert.deepEqual(ui.health({...base,health_failures:2},true).tunnel,['连接异常','warn']);
  assert.deepEqual(ui.health({...base,local_health_failures:2},true).reviewer,['响应异常','warn']);
  assert.equal(ui.health(base,true).queued,1);
  assert.equal(ui.health(base,true).running,1);
  assert.deepEqual(ui.health(base,false).reviewer,['待确认','neutral']);
});
test('disconnect, pending operations and service readiness gate mutations',()=>{
  const s={desired:true,pids:{reviewer:12},phase:'running'};
  for(const action of ['stop','restart','retry','reconcile','tunnel']){
    assert.equal(ui.canAct(action,s,false,false),false);
    assert.equal(ui.canAct(action,{...s,pending:true},true,false),false);
    assert.equal(ui.canAct(action,s,true,true),false);
    assert.equal(ui.canAct(action,s,true,false),true);
  }
  assert.equal(ui.canAct('start',s,true,false),false);
  assert.equal(ui.canAct('retry',{...s,phase:'recovering'},true,false),false);
  assert.equal(ui.canAct('start',{desired:false},true,false),true);
});
test('comment links reject executable URLs, plain HTTP and embedded credentials',()=>{
  for(const url of ['javascript:alert(1)','data:text/html,test','http://example.com','https://token@example.com','/relative'])assert.equal(ui.safeLink(url),null);
  assert.equal(ui.safeLink('https://github.com/acme/repo/pull/1#issuecomment-2'),'https://github.com/acme/repo/pull/1#issuecomment-2');
});
test('log filtering preserves original line numbers and classifies errors',()=>{
  const result=ui.logLines(['ok','12:00 ERR connection','12:01 WARN retry','<script>untrusted</script>'],'err');
  assert.deepEqual(result,[{text:'12:00 ERR connection',number:2,level:'error'}]);
  assert.equal(ui.logLines(['<script>untrusted</script>'],'')[0].text,'<script>untrusted</script>');
});
test('hook input preserves IDs without rounding and rejects ambiguous entries',()=>{
  assert.deepEqual(ui.parseHooks('acme/repo 123\nacme/web 0'),{'acme/repo':123,'acme/web':0});
  for(const input of ['acme/repo 9007199254740993','acme/repo -1','acme/repo 2\nacme/repo 3','../../repo 2','acme/repo 1 extra'])assert.throws(()=>ui.parseHooks(input));
});
