import type { UserErrorRequest } from '@/types'

const resetReasons = new Set([
  'NO_ERROR', 'PROTOCOL_ERROR', 'INTERNAL_ERROR', 'FLOW_CONTROL_ERROR', 'SETTINGS_TIMEOUT',
  'STREAM_CLOSED', 'FRAME_SIZE_ERROR', 'REFUSED_STREAM', 'CANCEL', 'COMPRESSION_ERROR',
  'CONNECT_ERROR', 'ENHANCE_YOUR_CALM', 'INADEQUATE_SECURITY', 'HTTP_1_1_REQUIRED'
])

export function userErrorMessage(
  error: Pick<UserErrorRequest, 'category' | 'message'> & { reset_reason?: string },
  t: (key: string, values?: Record<string, string>) => string
): string {
  if (error.category !== 'transport_error') return error.message
  const reason = error.reset_reason && resetReasons.has(error.reset_reason) ? ` ${error.reset_reason}` : ''
  return t('usage.errors.peerReset', { protocol: `HTTP/2${reason}` })
}
