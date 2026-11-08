import type { CredentialOperationFailure, TokenGuardV2Account, TokenGuardV2Task } from '@/api/admin/accountTokenGuardV2'

const actions: Record<string, string> = {
  phone_verification_required: 'complete_phone_verification',
  account_deactivated: 'contact_provider', account_deleted: 'contact_provider', account_retired: 'contact_provider',
  requires_relogin: 'reauthorize', password_rejected: 'update_password', mfa_required: 'complete_verification',
  security_check: 'review_authorization', authorization_denied: 'review_authorization',
  rate_limited: 'check_service_later', timeout: 'check_service_later', transport_error: 'check_service_later',
  service_error: 'check_service_later', unknown: 'review_diagnostics'
}
export const operationStages = new Set(['starting', 'web_login', 'password_accepted', 'mfa_challenge', 'mfa_accepted',
  'web_redirect', 'oauth_authorize', 'session_select', 'workspace_select', 'callback_validation',
  'token_exchange', 'oauth_output', 'completed', 'inspection', 'refresh_token', 'http_request',
  'event_stream', 'terminal_result', 'credentials_validation', 'credentials_submit', 'unknown'])

// Defence for old API/worker records: never render arbitrary error strings.
export function safeOperationFailure(value?: Partial<CredentialOperationFailure>): CredentialOperationFailure {
  const code = value?.code && Object.prototype.hasOwnProperty.call(actions, value.code) ? value.code : 'unknown'
  const at = value?.occurred_at
  return { code, category: code, action: actions[code]!,
    stage: value?.stage && operationStages.has(value.stage) ? value.stage : 'unknown',
    occurred_at: at && /^\d{4}-\d{2}-\d{2}T[\d:.+Z-]+$/.test(at) && Number.isFinite(Date.parse(at)) ? at : undefined }
}

export function taskOperationFailure(task?: TokenGuardV2Task): CredentialOperationFailure | undefined {
  if (!task) return undefined
  if (task.failure) return safeOperationFailure(task.failure)
  if (task.status !== 'failed' && !task.error) return undefined
  return safeOperationFailure({ code: task.error, occurred_at: task.finished_at })
}

export function probeOperationFailure(item: TokenGuardV2Account): CredentialOperationFailure | undefined {
  if (item.probe_failure) return safeOperationFailure(item.probe_failure)
  if (['ok', 'pending', ''].includes(item.probe_state)) return undefined
  return safeOperationFailure({ stage: 'inspection', occurred_at: item.last_probe_at })
}

const legacyReasons = new Set(['protocol_error', 'password_rejected', 'totp_rejected', 'interaction_required', 'security_check',
  'candidate_missing', 'stored_identity_missing', 'id_token_invalid', 'access_token_invalid',
  'token_account_conflict', 'token_user_conflict', 'token_email_conflict', 'account_id_missing', 'account_id_mismatch',
  'user_id_missing', 'user_id_mismatch', 'email_missing', 'email_mismatch',
  'session_unavailable', 'workspace_mismatch', 'callback_missing', 'callback_invalid', 'callback_state_mismatch',
  'pkce_invalid', 'token_exchange_failed', 'credentials_incomplete', 'transport_error', 'runtime_error', 'process_failed',
  'timeout', 'diagnostic_invalid', 'output_invalid', 'http_rejected', 'duplicate_field', 'invalid_json', 'unsupported_event',
  'missing_result', 'response_too_large', 'invalid_result', 'provider_failed', 'submission_failed', 'credentials_not_accepted'])
export function safeDiagnosticToken(value: string): string {
  return operationStages.has(value) || Object.prototype.hasOwnProperty.call(actions, value) || legacyReasons.has(value) ? value : 'unknown'
}
