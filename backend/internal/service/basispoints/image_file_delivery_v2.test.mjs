import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
const source = fs.readFileSync(new URL('./image_file_delivery_v2.js', import.meta.url), 'utf8');
const legacy = fs.readFileSync(new URL('./image_file_delivery.js', import.meta.url), 'utf8');
const nl = String.fromCharCode(10);
const payload = {call_id:'call_test', sha256:'a'.repeat(64), png_b64:'AA=='};
const receipt = {kind:'sub2api_image_file_v1', status:'saved', call_id:payload.call_id, sha256:payload.sha256, path:'/workspace/output/images/sub2api-'+payload.sha256+'.png', bytes:1};
const ready = {session_id:42, output:'BPS_IMAGE_STDIN_READY'+nl};
const running = {session_id:42, output:''};
const saved = {exit_code:0, output:JSON.stringify(receipt)+nl};
async function execute({start=ready, responses=[saved], code=source, tick=1000, missingRuntime=false}={}) {
  const outputs=[], writes=[];
  let clock=0, previews=0, command='';
  const tools={exec_command:async args=>{command=args.cmd;return start;},view_image:async()=>{previews++;throw Error('unexpected preview');}};
  if(!missingRuntime) tools.write_stdin=async args=>{
    clock+=tick;writes.push(args);
    const result=responses.shift() ?? running;
    if(result instanceof Error) throw result;
    return result;
  };
  const run=vm.runInNewContext('('+code+')',{tools,Date:{now:()=>clock},text:x=>outputs.push(JSON.parse(JSON.stringify(x))),image:()=>{previews++;}});
  await run(payload);
  assert.equal(outputs.length,1,'one terminal delivery receipt');
  return {result:outputs[0],writes,previews,command};
}
test('immediate delivery succeeds without uploading the saved image again',async()=>{
  const r=await execute();assert.equal(r.result.status,'saved');assert.equal(r.previews,0);assert.equal(r.writes[0].chars,payload.png_b64+nl);
});
test('waits for a split readiness marker before sending PNG data',async()=>{
  const r=await execute({start:running,responses:[{session_id:42,output:'BPS_IMAGE_'},{session_id:42,output:'STDIN_READY'+nl},saved]});
  assert.equal(r.result.status,'saved');assert.deepEqual(r.writes.map(x=>x.chars),['','',payload.png_b64+nl]);
});
test('a save still running after three polls is not reported as failed',async()=>{
  const r=await execute({responses:[running,running,running,running,running,saved]});assert.equal(r.result.status,'saved');assert.equal(r.writes.length,6);
});
test('receipt is accepted only after successful process exit',async()=>{
  const r=await execute({responses:[{session_id:42,output:JSON.stringify(receipt)+nl},running,{exit_code:0,output:''}]});assert.equal(r.result.status,'saved');assert.equal(r.writes.length,3);
});
test('an existing verified image does not retransmit the image bytes',async()=>{
  const r=await execute({start:saved,responses:[]});assert.equal(r.result.status,'saved');assert.equal(r.writes.length,0);
});
test('startup permission failures retain a useful class and exit code',async()=>{
  const r=await execute({start:{exit_code:1,output:'PermissionError: denied'}});assert.equal(r.result.status,'failed');assert.equal(r.result.code,'writer_start_failed');assert.equal(r.result.exit_code,1);assert.match(r.result.reason,/PermissionError/);assert.equal(r.writes.length,0);
});
test('a running reader is bounded when the readiness marker never arrives',async()=>{
  const r=await execute({start:running,responses:[],tick:10000});assert.equal(r.result.code,'writer_start_timeout');assert.ok(r.writes.length<10);assert.ok(r.writes.every(x=>x.chars===''||x.chars===nl));
});
test('a stalled save times out without claiming success or regenerating',async()=>{
  const r=await execute({responses:[],tick:15000});assert.equal(r.result.code,'writer_finish_timeout');assert.match(r.result.instruction,/Do not regenerate/);assert.ok(r.writes.length<10);
});
test('mismatched size and hash never produce saved receipts',async()=>{
  for(const change of [{bytes:2},{sha256:'b'.repeat(64)},{call_id:'wrong'}]){
    const r=await execute({responses:[{exit_code:0,output:JSON.stringify({...receipt,...change})}]});assert.equal(r.result.code,'receipt_invalid');
  }
});
test('a missing client runtime is an explicit failure',async()=>{
  const r=await execute({missingRuntime:true});assert.equal(r.result.code,'runtime_unavailable');
});
test('the original implementation reproduces both early failure cases',async()=>{
  const startup=await execute({code:legacy,start:running,responses:[ready,saved]});assert.equal(startup.result.status,'failed');assert.equal(startup.result.reason,'image file writer did not start');
  const finish=await execute({code:legacy,responses:[running,running,running,running,running,saved]});assert.equal(finish.result.status,'failed');assert.equal(finish.result.reason,'client image persistence failed');
});
