async function (payload) {
  // Executable code is small and reviewable. PNG bytes are supplied only as
  // stdin data, avoiding both shell ARG_MAX and the Node code review size cap.
  const script = [
    'import sys,os,tty,signal,base64,hashlib,json,pathlib,stat,tempfile',
    'signal.alarm(90)',
    'expected,call_id,encoded_length=sys.argv[1],sys.argv[2],int(sys.argv[3])',
    'tty.setraw(sys.stdin.fileno())',
    'print("BPS_IMAGE_STDIN_READY",flush=True)',
    'raw=sys.stdin.buffer.readline(encoded_length+2)',
    'assert len(raw)==encoded_length+1 and raw.endswith(b"\\n"), "incomplete image input"',
    'data=base64.b64decode(raw[:-1],validate=True)',
    'assert hashlib.sha256(data).hexdigest()==expected, "image digest mismatch"',
    'directory=pathlib.Path.cwd().resolve()',
    'for component in ("output","images"):',
    ' directory=directory/component',
    ' directory.mkdir(mode=0o700,exist_ok=True)',
    ' assert directory.is_dir() and not directory.is_symlink(), "image directory must be a real workspace directory"',
    'target=directory/("sub2api-"+expected+".png")',
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
    'fd=os.open(str(target),os.O_RDONLY|os.O_NOFOLLOW)',
    'with os.fdopen(fd,"rb") as saved:',
    ' info=os.fstat(saved.fileno())',
    ' assert stat.S_ISREG(info.st_mode) and info.st_size==len(data), "saved image size mismatch"',
    ' assert hashlib.sha256(saved.read()).hexdigest()==expected, "saved image digest mismatch"',
    'print(json.dumps({"kind":"sub2api_image_file_v1","status":"saved","call_id":call_id,"sha256":expected,"path":str(target),"bytes":len(data)}),flush=True)'
  ].join('\n');
  const quote = value => "'" + value.replace(/'/g, "'\\''") + "'";
  let session;
  try {
    if (typeof tools.exec_command !== 'function' || typeof tools.write_stdin !== 'function') throw new Error('client stdin file runtime unavailable');
    const started = await tools.exec_command({cmd: 'exec python3 -u -c ' + quote(script) + ' ' + quote(payload.sha256) + ' ' + quote(payload.call_id) + ' ' + String(payload.png_b64.length), tty: true, login: false, yield_time_ms: 1000, max_output_tokens: 700});
    session = started.session_id;
    if (!session || !started.output.includes('BPS_IMAGE_STDIN_READY')) throw new Error('image file writer did not start');
    let result = await tools.write_stdin({session_id: session, chars: payload.png_b64 + '\n', yield_time_ms: 1000, max_output_tokens: 1000});
    let output = result.output || '';
    for (let n = 0; result.session_id && n < 3; n++) {
      result = await tools.write_stdin({session_id: session, chars: '', yield_time_ms: 1000, max_output_tokens: 1000});
      output += result.output || '';
    }
    if (result.exit_code !== 0) throw new Error('client image persistence failed');
    session = undefined;
    let receipt;
    for (const line of output.split(/\r?\n/)) {
      try {
        const candidate = JSON.parse(line);
        if (candidate.kind === 'sub2api_image_file_v1' && candidate.status === 'saved' && candidate.call_id === payload.call_id && candidate.sha256 === payload.sha256 && typeof candidate.path === 'string' && candidate.path.endsWith('/output/images/sub2api-' + payload.sha256 + '.png')) receipt = candidate;
      } catch {}
    }
    if (!receipt) throw new Error('image persistence receipt missing');
    text(receipt);
    if (typeof tools.view_image === 'function') {
      try {
        const preview = await tools.view_image({path: receipt.path});
        image(preview.image_url);
      } catch { text({image_preview: 'unavailable', local_image_saved: true}); }
    }
  } catch (e) {
    // This session runs an exec'ed bounded data reader, never an interactive
    // shell. End incomplete data input; its alarm also bounds cancellation.
    if (session) { try { await tools.write_stdin({session_id: session, chars: '\n', yield_time_ms: 1000, max_output_tokens: 100}); } catch {} }
    text({kind: 'sub2api_image_file_v1', status: 'failed', call_id: payload.call_id, reason: String(e.message).slice(0, 160), instruction: 'Image generation already completed. Do not regenerate or claim delivery succeeded. Report the client file delivery failure.'});
  }
}
