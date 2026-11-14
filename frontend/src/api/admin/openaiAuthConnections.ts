import { apiClient } from '../client'

export type OAuthConnectionKind = 'codex' | 'bps' | 'siwc'
export type ConnectionKind = OAuthConnectionKind | 'api_key'
export interface OpenAIAuthConnection {
  id: string
  account_id: number
  kind: ConnectionKind
  label: string
  legacy: boolean
  refresh_state?: 'idle' | 'in_progress' | 'uncertain' | 'rejected'
  status: 'pending' | 'active' | 'disabled' | 'unlinked'
  issuer: string
  client_id: string
  subject?: string
  identity_verified: boolean
  workspace?: string
  resource?: string
  audience?: string
  scopes: string[]
  capabilities: string[]
  host_id?: string
  preferred: boolean
  revision: number
  cooldown_until?: string
}
const path = (id: number) => `/admin/accounts/${id}/auth-connections`
export const listAuthConnections = async (id: number): Promise<OpenAIAuthConnection[]> => (await apiClient.get(path(id))).data
export const createAuthConnection = async (id: number, input: { kind: OAuthConnectionKind; label: string }): Promise<OpenAIAuthConnection> => {
  if (!['codex', 'bps', 'siwc'].includes(input.kind) || 'api_key' in input) {
    throw new Error('OAuth connections cannot register API keys')
  }
  return (await apiClient.post(path(id), input)).data
}
export const updateAuthConnection = async (id: number, c: OpenAIAuthConnection, action: 'select' | 'disable' | 'enable' | 'unlink' | 'recover'): Promise<OpenAIAuthConnection> => (await apiClient.patch(`${path(id)}/${c.id}`, { revision: c.revision, action })).data
export const authorizeConnection = async (id: number, connectionId: string): Promise<{ auth_url: string; session_id: string }> => (await apiClient.post(`${path(id)}/${connectionId}/authorize`)).data
export const completeConnection = async (id: number, connectionId: string, sessionId: string, callbackUrl: string): Promise<void> => { await apiClient.post(`${path(id)}/${connectionId}/callback`, { session_id: sessionId, callback_url: callbackUrl }) }
export const connectionAuthorizationStatus = async (id: number, connectionId: string, sessionId: string): Promise<{ status: 'waiting' | 'saved' | 'unconfirmed' }> => (await apiClient.post(`${path(id)}/${connectionId}/authorization-status`, { session_id: sessionId })).data
export const revokeConnection = async (id: number, c: OpenAIAuthConnection): Promise<{ remote_revocation_confirmed: boolean }> => (await apiClient.post(`${path(id)}/${c.id}/revoke`, { revision: c.revision })).data
export const useLegacyConnections = async (id: number): Promise<void> => { await apiClient.post(`${path(id)}/use-legacy`) }
export const rotateConnectionAPIKey = async (id: number, c: OpenAIAuthConnection, apiKey: string): Promise<void> => { await apiClient.put(`${path(id)}/${c.id}/api-key`, { revision: c.revision, api_key: apiKey }) }
