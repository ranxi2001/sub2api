import { apiClient } from '../client'
export interface UpstreamChannel { id: number; code: string; name: string; remark: string; revision: number; members: number }
export interface ChannelBinding { account_id: number; account_name: string; deleted: boolean; channel_id: number; revision: number; channel?: UpstreamChannel }
export interface ChannelDraft { code?: string; name: string; remark: string; revision?: number; confirm?: boolean }
export async function listUpstreamChannels(search = '', page = 1) { return (await apiClient.get<{ items: UpstreamChannel[]; total: number }>('/admin/upstream-channels', { params: { search, page, page_size: 20 } })).data }
export async function getUpstreamChannel(id: number) { return (await apiClient.get<UpstreamChannel>(`/admin/upstream-channels/${id}`)).data }
export async function saveUpstreamChannel(id: number, body: ChannelDraft) { return id ? (await apiClient.put<UpstreamChannel>(`/admin/upstream-channels/${id}`, body)).data : (await apiClient.post<UpstreamChannel>('/admin/upstream-channels', body)).data }
export async function deleteUpstreamChannel(id: number, revision: number) { return (await apiClient.delete(`/admin/upstream-channels/${id}`, { data: { revision, confirm: true } })).data }
export async function readChannelBindings(ids: number[], options?: { signal?: AbortSignal }) { return (await apiClient.get<{ items: ChannelBinding[] }>('/admin/upstream-channel-bindings', { params: { account_ids: ids.join(',') }, ...options })).data.items }
export async function bindChannelAccounts(channel_id: number, bindings: ChannelBinding[], options?: { signal?: AbortSignal }) { return (await apiClient.put<{ items: ChannelBinding[] }>('/admin/upstream-channel-bindings', { channel_id, bindings: bindings.map(({ account_id, channel_id, revision }) => ({ account_id, channel_id, revision })), confirm: true }, options)).data.items }
export async function getChannelMembers(id: number, page = 1) { return (await apiClient.get<{ items: ChannelBinding[]; total: number }>(`/admin/upstream-channels/${id}/members`, { params: { page, page_size: 20 } })).data }
