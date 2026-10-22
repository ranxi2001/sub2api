import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const api = vi.hoisted(() => ({ get: vi.fn(), checkNodes: vi.fn(), saveWallet: vi.fn(), deleteWallet: vi.fn(), saveSettings: vi.fn(), syncRates: vi.fn(), saveRate: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/bepusdt', () => ({ bepusdtAPI: api }))
import BEpusdtManager from '../BEpusdtManager.vue'
const config = { settings: { rpc_endpoint_base: 'https://rpc.example' }, secret_configured: { rate_sync_coingecko_api_key: true }, wallets: [], rates: [], stats: { base: { block: '2040', succ: '100.00%', time: 1790000000 } } }
beforeEach(() => { vi.clearAllMocks(); api.get.mockResolvedValue({ data: config }); api.saveWallet.mockResolvedValue({}); api.deleteWallet.mockResolvedValue({}) })
async function openPanel(page: ReturnType<typeof mount>) {
  const details = page.get('details')
  ;(details.element as HTMLDetailsElement).open = true
  await details.trigger('toggle')
  await flushPromises()
}
describe('BEpusdtManager', () => {
  it('initializes only when opened and displays scanner activity', async () => {
    const page = mount(BEpusdtManager)
    expect(api.get).not.toHaveBeenCalled()
    await openPanel(page)
    expect(api.get).toHaveBeenCalledOnce()
    expect(page.text()).toContain('2040')
    expect(page.text()).toContain('100.00%')
    const password = page.findAll('input[type="password"]')[1]!
    expect((password.element as HTMLInputElement).value).toBe('')
    expect(password.attributes('placeholder')).toBe('admin.accounts.leaveEmptyToKeep')
    page.unmount()
  })
  it('saves the selected wallet network and preserves edits when pending payments reject a change', async () => {
    api.saveWallet.mockRejectedValue({ message: 'wallet has pending payments' })
    const page = mount(BEpusdtManager)
    await openPanel(page)
    await page.get('#bepusdt-wallet-network').setValue('usdc.base')
    await page.get('#bepusdt-wallet-address').setValue('0x1111111111111111111111111111111111111111')
    const section = page.findAll('section')[0]!
    await section.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    await flushPromises()
    expect(api.saveWallet).toHaveBeenCalledWith(expect.objectContaining({ trade_type: 'usdc.base', address: '0x1111111111111111111111111111111111111111', status: 1 }))
    expect(page.get('[role="alert"]').text()).toContain('pending payments')
    expect((page.get('#bepusdt-wallet-address').element as HTMLInputElement).value).toContain('0x1111')
    page.unmount()
  })
  it('requires confirmation before deleting a wallet', async () => {
    api.get.mockResolvedValue({ data: { ...config, wallets: [{ id: 12, name: 'Base', address: '0x1111', trade_type: 'usdc.base', status: 1, remark: '' }] } })
    const page = mount(BEpusdtManager)
    await openPanel(page)
    await page.findAll('button').find(button => button.text() === 'common.delete')!.trigger('click')
    expect(api.deleteWallet).not.toHaveBeenCalled()
    await page.findAll('button').find(button => button.text() === 'common.confirm')!.trigger('click')
    await flushPromises()
    expect(api.deleteWallet).toHaveBeenCalledWith(12)
    page.unmount()
  })
  it('shows first node failures and checks saved nodes without discarding unsaved input', async () => {
    api.get.mockResolvedValue({ data: { ...config, stats: { bsc: { block: '', time: 0, succ: '0.00%', status: 'error', error: 'RPC HTTP 403' } } } })
    api.checkNodes.mockResolvedValue({ data: { node_checks: { bsc: { status: 'error', checked_at: 1790000000, error: 'RPC HTTP 403' } } } })
    const page = mount(BEpusdtManager)
    await openPanel(page)
    expect(page.text()).toContain('admin.settings.payment.scanNeverSucceeded')
    expect(page.text()).toContain('RPC HTTP 403')
    const node = page.findAll('input').find(input => (input.element as HTMLInputElement).value === 'https://rpc.example')!
    await node.setValue('https://unsaved.example')
    await page.get('[data-testid="check-payment-nodes"]').trigger('click')
    await flushPromises()
    expect(api.checkNodes).toHaveBeenCalledOnce()
    expect(api.saveSettings).not.toHaveBeenCalled()
    expect(page.text()).toContain('admin.settings.payment.nodeUnavailable')
    expect((node.element as HTMLInputElement).value).toBe('https://unsaved.example')
    page.unmount()
  })
})
