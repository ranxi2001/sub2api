async function (payload) {
  // The V2 writer remains byte-for-byte stable for historical receipts. V3
  // retries only a startup failure that may be transient, with the same PNG.
  const publish = text;
  let result;
  {
    const text = value => { result = value; };
    const writer = /* V2_WRITER */;
    await writer(payload);
    const transientStartupFailure = result?.status === 'failed' &&
      result.stage === 'startup' &&
      (result.code === 'client_tool_error' ||
        (result.code === 'writer_start_failed' && result.reason?.includes('process exit')));
    if (transientStartupFailure) {
      result = undefined;
      await writer(payload);
    }
  }
  if (result?.status === 'failed') {
    result.instruction = 'Image generation already completed. Do not regenerate. Report the file-delivery code, exit_code, and reason; retry this same delivery call only after the client writer problem is resolved.';
  }
  publish(result);
}
