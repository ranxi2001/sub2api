async function (payload) {
  // Keep image bytes in stdin data, never shell arguments. Wait for readiness
  // and process exit explicitly: one tool response is not a completion signal.
  const script = [
    'import sys,os,tty,signal,base64,hashlib,json,pathlib,stat,tempfile',
    'signal.alarm(90)',
    'expected,call_id,encoded_length,expected_size=sys.argv[1],sys.argv[2],int(sys.argv[3]),int(sys.argv[4])',
    'directory=pathlib.Path.cwd().resolve()',
    'for component in ("output","images"):',
    ' directory=directory/component',
    ' directory.mkdir(mode=0o700,exist_ok=True)',
    ' assert directory.is_dir() and not directory.is_symlink(), "image directory must be a real workspace directory"',
    'target=directory/("sub2api-"+expected+".png")',
    'def verify_and_report(reused):',
    ' fd=os.open(str(target),os.O_RDONLY|os.O_NOFOLLOW)',
    ' with os.fdopen(fd,"rb") as saved:',
    '  info=os.fstat(saved.fileno())',
    '  assert stat.S_ISREG(info.st_mode) and info.st_size==expected_size, "saved image size mismatch"',
    '  assert hashlib.sha256(saved.read()).hexdigest()==expected, "saved image digest mismatch"',
    ' print(json.dumps({"kind":"sub2api_image_file_v1","status":"saved","call_id":call_id,"sha256":expected,"path":str(target),"bytes":expected_size,"reused":reused}),flush=True)',
    'if os.path.lexists(str(target)):',
    ' verify_and_report(True)',
    ' sys.exit(0)',
    'tty.setraw(sys.stdin.fileno())',
    'print("BPS_IMAGE_STDIN_READY",flush=True)',
    'raw=sys.stdin.buffer.readline(encoded_length+2)',
    'assert len(raw)==encoded_length+1 and raw.endswith(b"\\n"), "incomplete image input"',
    'data=base64.b64decode(raw[:-1],validate=True)',
    'assert len(data)==expected_size and hashlib.sha256(data).hexdigest()==expected, "image digest mismatch"',
    'temporary=None',
    'try:',
    ' with tempfile.NamedTemporaryFile(dir=str(directory),prefix=".sub2api-",delete=False) as handle:',
    '  temporary=handle.name',
    '  handle.write(data)',
    '  handle.flush()',
    '  os.fsync(handle.fileno())',
    ' try: os.link(temporary,str(target))',
    ' except FileExistsError: pass',
    'finally:',
    ' if temporary: os.unlink(temporary)',
    'verify_and_report(False)'
  ].join('\n');
  const quote = value => "'" + value.replace(/'/g, "'\\''") + "'";
  let session;
  let stage = 'startup';
  let exitCode;
  let output = '';
  let receipt;
  const expectedSize = payload.png_b64.length / 4 * 3 - (payload.png_b64.endsWith('==') ? 2 : payload.png_b64.endsWith('=') ? 1 : 0);
  const consume = result => {
    session = result.session_id;
    exitCode = result.exit_code;
    output = (output + (result.output || '')).slice(-16384);
    for (const line of output.split(/\r?\n/)) {
      try {
        const candidate = JSON.parse(line);
        if (candidate.kind === 'sub2api_image_file_v1' && candidate.status === 'saved' && candidate.call_id === payload.call_id && candidate.sha256 === payload.sha256 && candidate.bytes === expectedSize && typeof candidate.path === 'string' && candidate.path.startsWith('/') && candidate.path.endsWith('/output/images/sub2api-' + payload.sha256 + '.png')) receipt = candidate;
      } catch {}
    }
  };
  const fail = (code, reason) => { const error = new Error(reason); error.code = code; throw error; };
  const failureClass = () => {
    for (const label of ['PermissionError', 'FileNotFoundError', 'NotADirectoryError', 'OSError', 'AssertionError']) {
      if (output.includes(label)) return label;
    }
    return 'process exit';
  };
  try {
    if (typeof tools.exec_command !== 'function' || typeof tools.write_stdin !== 'function') fail('runtime_unavailable', 'Client file runtime unavailable');
    const started = await tools.exec_command({cmd: 'exec python3 -u -c ' + quote(script) + ' ' + quote(payload.sha256) + ' ' + quote(payload.call_id) + ' ' + String(payload.png_b64.length) + ' ' + String(expectedSize), tty: true, login: false, yield_time_ms: 1000, max_output_tokens: 1000});
    consume(started);
    const readyDeadline = Date.now() + 20000;
    for (let n = 0; session && !receipt && !output.includes('BPS_IMAGE_STDIN_READY') && Date.now() < readyDeadline && n < 20; n++) {
      consume(await tools.write_stdin({session_id: session, chars: '', yield_time_ms: 1000, max_output_tokens: 1000}));
    }
    if (!receipt) {
      if (!session) fail('writer_start_failed', 'Image writer stopped before accepting data: ' + failureClass());
      if (!output.includes('BPS_IMAGE_STDIN_READY')) fail('writer_start_timeout', 'Image writer readiness timed out');
      stage = 'transfer';
      consume(await tools.write_stdin({session_id: session, chars: payload.png_b64 + '\n', yield_time_ms: 1000, max_output_tokens: 1000}));
    }
    stage = 'persist';
    const finishDeadline = Date.now() + 60000;
    for (let n = 0; session && Date.now() < finishDeadline && n < 60; n++) {
      consume(await tools.write_stdin({session_id: session, chars: '', yield_time_ms: 1000, max_output_tokens: 1000}));
    }
    if (session) fail('writer_finish_timeout', 'Image writer completion timed out');
    if (exitCode !== 0) fail('writer_exit_failed', 'Image writer did not complete successfully: ' + failureClass());
    if (!receipt) fail('receipt_invalid', 'Verified image persistence receipt missing');
    // The gateway attaches the verified absolute local path to the final reply.
    // Returning another image block here needlessly uploads the PNG next turn.
    text(receipt);
  } catch (e) {
    // Only this bounded reader is touched. Finish incomplete input; the alarm
    // bounds orphaned readers even if the client cancels or the tool disconnects.
    if (session) { try { await tools.write_stdin({session_id: session, chars: '\n', yield_time_ms: 1000, max_output_tokens: 100}); } catch {} }
    text({kind: 'sub2api_image_file_v1', status: 'failed', call_id: payload.call_id, stage, code: e.code || 'client_tool_error', exit_code: typeof exitCode === 'number' ? exitCode : null, reason: String(e.message).slice(0, 160), instruction: 'Image generation already completed. Do not regenerate or claim delivery succeeded. Preserve the completed image and report the file delivery error code.'});
  }
}
