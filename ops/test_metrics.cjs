// 验证管理台观测展开项、缺失用量与旧记录展示，不连接正在运行的服务。
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
class Element {
  constructor(tag){this.tag=tag;this.children=[];this.textContent='';}
  append(...nodes){this.children.push(...nodes);}
  addEventListener(){}
  text(){return this.textContent+this.children.map(c=>c.text()).join('\n');}
}
const context=vm.createContext({document:{createElement:tag=>new Element(tag)}});
const source=fs.readFileSync(__dirname+'/web/app.js','utf8');
vm.runInContext(source.slice(0,source.indexOf('async function refresh()')),context);
test('metrics render stages, usage and bounded trace as text',()=>{
  const td=new Element('td');
  context.renderMetrics(td,{id:'one',metrics:{total_ms:1250,iterations:2,memory_cache_hit:true,memory_files:4,usage:{total_tokens:12,input_tokens:10,output_tokens:2,cached_input_tokens:0},stages_ms:{checkout:10,model:1000},tool_count:1,tools:[{name:'<script>unsafe</script>',duration_ms:5,result_bytes:20}]}});
  const text=td.text();
  assert.match(text,/1.25 秒 · 2 轮 · 12 token/);
  assert.match(text,/检出：0.010 秒/);
  assert.match(text,/依据校验：未执行/);
  assert.match(text,/命中提交缓存 · 4 个文档/);
  assert.match(text,/输入 10 \/ 输出 2/);
  assert.match(text,/<script>unsafe<\/script> · 5 ms/);
  assert.equal(td.children[0].children.at(-1).children[0].tag,'li');
});
test('legacy and absent usage are not reported as zero',()=>{
  const old=new Element('td');context.renderMetrics(old,{});assert.equal(old.text(),'未记录');
  const td=new Element('td');context.renderMetrics(td,{id:'two',metrics:{total_ms:0,iterations:0,usage:null,tool_count:0}});
  assert.match(td.text(),/用量未记录/);assert.doesNotMatch(td.text(),/0 token/);
});

test('empty memory index displays zero documents',()=>{
  const td=new Element('td');
  context.renderMetrics(td,{id:'empty-memory',metrics:{total_ms:10,iterations:1,usage:null,tool_count:0,memory_cache_hit:false,memory_files:0}});
  assert.match(td.text(),/完成索引检查 · 0 个文档/);
});

test('grounding rejection and partial batches remain visible',()=>{
 const td=new Element('td');
 context.renderMetrics(td,{id:'partial',metrics:{total_ms:10,iterations:1,usage:null,tool_count:0,stages_ms:{ground:5},ground_rejected:2,batches_total:3,batches_completed:1}});
 assert.match(td.text(),/依据校验未通过 2 条/);
 assert.match(td.text(),/批次完成 1\/3/);
});
