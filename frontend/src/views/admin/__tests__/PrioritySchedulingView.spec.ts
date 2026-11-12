import { describe, expect, it, vi } from 'vitest'
import { client as apiClient, settle as flushPromises, analysis, config, deferred, overrideGet, performance, render, wrapper } from './prioritySchedulingFixture'
import AccountPerformanceCell from '@/components/account/AccountPerformanceCell.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

describe('priority scheduling shares account performance', () => {
  it('renders the existing component and the exact evidence used by the server score', async () => {
    render(); await flushPromises()
    const cells=wrapper!.findAllComponents(AccountPerformanceCell)
    expect(cells).toHaveLength(3)
    expect(cells[0]!.props('metrics')).toEqual(performance())
    expect(cells[0]!.props('canConfigure')).toBe(false)
    expect(cells[0]!.get('[data-metric="tps"]').text()).toContain('25.0 t/s')
    expect(cells[0]!.get('[data-metric="degradation"]').text()).toContain('5.0%')
    expect(cells[0]!.get('[data-metric="ttft"]').text()).toContain('4.00s')
    expect(cells[0]!.get('[data-metric="cache"]').text()).toContain('86.8%')
    expect(cells[0]!.get('[data-metric="upstream"]').text()).toContain('0.5%')
    expect(cells[0]!.get('[data-metric="upstream"]').text()).toContain('accountPerformance.localSample')
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('75.0')
    expect(wrapper!.find('[data-testid="latency-panel"]').exists()).toBe(false)
    expect(apiClient.put).not.toHaveBeenCalled(); expect(apiClient.post).not.toHaveBeenCalled()
  })

  it.each([undefined,null,NaN])('keeps missing or invalid metrics unknown: %s', async invalid => {
    overrideGet((url,options) => {
      if(url!=='/admin/priority-scheduling/analysis')return
      const data=analysis(options.params,1)
      const m=data.snapshot.candidates[0]!.performance!
      m.avg_tps=invalid as any; m.avg_ttft_ms=invalid as any; m.degradation_percent=invalid as any
      m.upstream_error=undefined; m.avg_cache_percent=invalid as any
      return {data}
    })
    render(); await flushPromises()
    const cell=wrapper!.getComponent(AccountPerformanceCell)
    for(const name of ['tps','ttft','degradation','cache','upstream'])expect(cell.get(`[data-metric="${name}"]`).text()).toContain('accountPerformance.uncollected')
  })

  it('distinguishes a measured zero error rate from missing samples', async () => {
    overrideGet((url,options) => {
      if(url!=='/admin/priority-scheduling/analysis')return
      const data=analysis(options.params,1); data.snapshot.candidates[0]!.performance!.upstream_error!.failures=0
      data.snapshot.candidates[0]!.performance!.upstream_error!.percent=0
      return {data}
    })
    render(); await flushPromises()
    expect(wrapper!.getComponent(AccountPerformanceCell).get('[data-metric="upstream"]').text()).toContain('0.0%')
  })

  it('keeps sticky clearing explicit and scoped to the reviewed actual selection', async () => {
    const latest=analysis({model:'fixture-model',group_id:17,minutes:60}).snapshot
    overrideGet((url) => url==='/admin/priority-scheduling/snapshot'?{data:{snapshot:latest}}:undefined)
    vi.mocked(apiClient.post).mockImplementation(async (url) => {
      if(url==='/admin/priority-scheduling/recompute')return {data:{before:latest,after:latest,timed_out:false,diff:[{account_id:1,account_name:'Fixture 1',changed:true,old_score:70,new_score:75,old_weight:1,new_weight:2,old_rank:2,new_rank:1,new_tier:'eligible',old_error_samples:1,new_error_samples:0}]}}
      if(url==='/admin/priority-scheduling/sticky-bindings/clear')return {data:{group_id:17,deleted:1,remaining:0,by_account:[]}}
      throw Error('unexpected mutation')
    })
    const confirm=vi.spyOn(window,'confirm').mockReturnValue(false)
    render(); await flushPromises(); await wrapper!.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    await wrapper!.get('[data-testid="clear-changed-sticky"]').trigger('click'); await flushPromises()
    expect(confirm).toHaveBeenCalled()
    expect(vi.mocked(apiClient.post).mock.calls.some(([url])=>url.endsWith('/clear'))).toBe(false)
    confirm.mockReturnValue(true)
    await wrapper!.get('[data-testid="clear-changed-sticky"]').trigger('click'); await flushPromises()
    expect(apiClient.post).toHaveBeenCalledWith('/admin/priority-scheduling/sticky-bindings/clear',{group_id:17,account_ids:[1]})
  })

  it('reads back the recomputed group after automatically switching the sticky scope', async () => {
    const latest = analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }).snapshot
    const pending = deferred<any>()
    overrideGet((url, options) => {
      if (url === '/admin/priority-scheduling/config') return { data: { ...config, group_ids: [12] } }
      if (url === '/admin/priority-scheduling/snapshot') return { data: { snapshot: latest } }
      if (url === '/admin/priority-scheduling/sticky-bindings') {
        if (options.params.group_id === 11) return pending.promise
        return { data: { group_id: 12, total: 4, by_account: [{ account_id: 2, count: 4 }] } }
      }
    })
    vi.mocked(apiClient.post).mockResolvedValue({ data: { before: latest, after: latest, timed_out: false, diff: [] } })
    render(); await flushPromises()
    expect(wrapper!.get('[data-testid="sticky-group"]').element).toHaveProperty('value', '12')
    await wrapper!.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(apiClient.get).toHaveBeenLastCalledWith('/admin/priority-scheduling/sticky-bindings', { params: { group_id: 11 } })
    pending.resolve({ data: { group_id: 11, total: 2, by_account: [{ account_id: 1, count: 2 }] } }); await flushPromises()
    expect(wrapper!.get('[data-testid="sticky-group"]').element).toHaveProperty('value', '11')
    expect(wrapper!.find('[data-testid="sticky-clear-1"]').exists()).toBe(true)
    expect(wrapper!.find('[data-testid="sticky-clear-2"]').exists()).toBe(false)
  })

  it('does not expose changed-sticky cleanup when the actual snapshot changes', async () => {
    const latest=analysis({model:'fixture-model',group_id:17,minutes:60}).snapshot
    overrideGet((url)=>url==='/admin/priority-scheduling/snapshot'?{data:{snapshot:latest}}:undefined)
    vi.mocked(apiClient.post).mockResolvedValue({data:{before:latest,after:{...latest,at:'2026-11-11T02:00:00Z'},timed_out:false,diff:[]}})
    render(); await flushPromises(); await wrapper!.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(wrapper!.find('[data-testid="clear-changed-sticky"]').exists()).toBe(false)
  })
})


describe('priority cache weight', () => {
  it('defaults an old response to zero and persists cache weight in every mode', async () => {
    render(); await flushPromises()
    const weight = wrapper!.get<HTMLInputElement>('[data-testid="cache-weight"]')
    expect(weight.element.value).toBe('0')
    for (const mode of ['balanced', 'experience', 'profit', 'custom']) {
      await wrapper!.get('[data-testid="mode"]').setValue(mode)
      await weight.setValue('25.5')
      await wrapper!.get('form').trigger('submit')
      await flushPromises()
      expect(apiClient.put).toHaveBeenLastCalledWith('/admin/priority-scheduling/config', expect.objectContaining({mode,cache_weight:25.5}))
    }
    await weight.setValue('0')
    await wrapper!.get('form').trigger('submit'); await flushPromises()
    expect(apiClient.put).toHaveBeenLastCalledWith('/admin/priority-scheduling/config', expect.objectContaining({cache_weight:0}))
  })

  it('loads a saved weight and keeps header and field notices on the same ID', async () => {
    overrideGet(url => url === '/admin/priority-scheduling/config' ? { data: {...config,cache_weight:40} } : undefined)
    render(); await flushPromises()
    expect(wrapper!.get<HTMLInputElement>('[data-testid="cache-weight"]').element.value).toBe('40')
    const badges = wrapper!.findAllComponents(NewFeatureBadge).filter(b => b.props('noticeId') === NEW_FEATURE_NOTICES.priorityCacheWeight)
    expect(badges).toHaveLength(2)
    expect(apiClient.put).not.toHaveBeenCalled()
  })
})
