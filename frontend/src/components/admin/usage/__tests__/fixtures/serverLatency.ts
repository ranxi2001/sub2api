import type { AdminUsageLog } from '@/types'

// Synthetic sampled boundaries: the displayed server totals must never lose 97ms.
export const serverLatency = {
  id: 97,
  request_id: 'synthetic-server-latency',
  model: 'synthetic-model',
  created_at: '2026-11-04T12:00:00Z',
  input_tokens: 100,
  output_tokens: 200,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  first_token_ms: 1923,
  duration_ms: 18853,
  queue_wait_ms: 97,
  queue_timing: {
    status: 'measured',
    source: 'ws_frame_ctx_pool',
    scope: 'pre_final_forward',
    queue_clamped: false,
    server_first_token_ms: 2020,
    server_duration_ms: 18950,
    forward_first_token_ms: 1923,
    forward_duration_ms: 18853,
  },
} as AdminUsageLog
