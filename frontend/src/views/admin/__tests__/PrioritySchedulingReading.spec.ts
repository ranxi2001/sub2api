import { describe, expect, it, vi } from 'vitest'
import { client as apiClient, settle as flushPromises, analysis, deferred, fixture, overrideGet, reads, render, unmount, wrapper } from './prioritySchedulingFixture'

describe('priority performance reading lifecycle', () => {
  it('paginates the shared performance table and preserves the reader and unsaved draft on polling', async () => {
    vi.useFakeTimers(); overrideGet((url,options)=>url==='/admin/priority-scheduling/analysis'?{data:analysis(options.params,65)}:undefined)
    render(); await flushPromises()
    const pager=wrapper!.getComponent('[data-testid="candidate-pagination"]')
    pager.vm.$emit('update:pageSize',10);pager.vm.$emit('update:page',3);await flushPromises()
    await wrapper!.get('[data-testid="target_tps"]').setValue(45)
    await vi.advanceTimersByTimeAsync(30000);await flushPromises()
    expect(reads()).toHaveLength(2)
    expect(pager.props()).toMatchObject({page:3,pageSize:10})
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('Fixture 21')
    expect((wrapper!.vm as any).draft.target_tps).toBe(45)
  })

  it('retains rows through pending reads and reports stale data on failure, then recovers', async () => {
    render();await flushPromises()
    const pending=deferred<any>();vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    const refresh=(wrapper!.vm as any).refresh();await flushPromises()
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('Fixture 1')
    pending.reject(Error('fixture unavailable'));await refresh;await flushPromises()
    expect(wrapper!.get('[data-testid="candidate-status"]').text()).toContain('priorityScheduling.reading.stale')
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('accountPerformance.unavailable')
    await (wrapper!.vm as any).refresh();await flushPromises()
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('25.0 t/s')
  })

  it('does not overlap a slow read or restart polling after unmount', async () => {
    vi.useFakeTimers();const pending=deferred<any>()
    overrideGet((url)=>url==='/admin/priority-scheduling/analysis'?pending.promise:undefined)
    render();await flushPromises();await vi.advanceTimersByTimeAsync(90000)
    expect(reads()).toHaveLength(1)
    unmount();pending.resolve({data:analysis({model:'fixture-model',group_id:17,minutes:60})});await flushPromises()
    await vi.advanceTimersByTimeAsync(60000);expect(reads()).toHaveLength(1)
  })

  it.each(['logout','unmount'])('does not publish delayed initialization after %s', async action => {
    const pending=deferred<any>();vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    render();await flushPromises()
    if(action==='logout')fixture.auth.user=null;else unmount()
    pending.resolve({data:{group_ids:[],models:[]}});await flushPromises()
    expect(reads()).toHaveLength(0)
  })

  it('pauses polling while the tab is hidden', async () => {
    vi.useFakeTimers();render();await flushPromises()
    vi.spyOn(document,'visibilityState','get').mockReturnValue('hidden')
    await vi.advanceTimersByTimeAsync(60000);expect(reads()).toHaveLength(1)
  })
})
