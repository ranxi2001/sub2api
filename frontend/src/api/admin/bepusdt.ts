import { apiClient } from '../client'

export interface CryptoWallet {
  id: number
  name: string
  address: string
  trade_type: string
  status: number
  remark: string
}

export interface CryptoRate {
  id: number
  fiat: string
  crypto: string
  rate: string
}

export interface CollectionConfig {
  settings: Record<string, string>
  secret_configured: Record<string, boolean>
  wallets: CryptoWallet[]
  rates: CryptoRate[]
  stats: Record<string, { block: string; succ: string; time: number; head?: string; status?: string; error?: string; endpoint?: string; failover?: boolean }>
  node_checks?: Record<string, { status: string; checked_at: number; error?: string }>
}

const base = '/admin/payment/bepusdt'
export const bepusdtAPI = {
  get: () => apiClient.get<CollectionConfig>(base),
  checkNodes: () => apiClient.post<{ node_checks: NonNullable<CollectionConfig['node_checks']> }>(`${base}/nodes/check`, {}, { timeout: 90000 }),
  saveSettings: (settings: Record<string, string>) => apiClient.put(`${base}/settings`, { settings }),
  saveWallet: (wallet: CryptoWallet) => wallet.id
    ? apiClient.put(`${base}/wallets/${wallet.id}`, wallet)
    : apiClient.post(`${base}/wallets`, wallet),
  deleteWallet: (id: number) => apiClient.delete(`${base}/wallets/${id}`),
  syncRates: () => apiClient.post(`${base}/rates/sync`),
  saveRate: (fiat: string, crypto: string, rate: string) => apiClient.post(`${base}/rates`, { fiat, crypto, rate }),
}
