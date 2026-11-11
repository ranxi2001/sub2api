import { describe, expect, it, vi } from 'vitest'
import { client as apiClient, settle as flushPromises, analysis, choose, config, deferred, directory, fixture, overrideGet, reads, render, unmount, wrapper } from './prioritySchedulingFixture'

describe('priority shared performance scope', () => {
  it.each([
    { configured: [], groups: directory, expected: 17 },
    { configured: [999,11], groups: directory, expected: 11 },
    { configured: [999], groups: directory, expected: 17 },
    { configured: [], groups: [directory[0]!], expected: 11 },
    { configured: [], groups: [], expected: 0 },
  ])('selects the default group before its first analysis: $configured -> $expected', async ({ configured, groups, expected }) => {
    overrideGet((url, options) => {
      if (url === '/admin/priority-scheduling/config') return { data: { ...config, group_ids: configured } }
      if (url === '/admin/groups/all') return { data: groups.filter(group => group.platform === options.params.platform) }
      if (url === '/admin/priority-scheduling/models') {
        if (options.params.group_id === 999) throw Error('deleted group')
        return { data: { models: [`model-${options.params.group_id ?? 0}`] } }
      }
    })
    render(); await flushPromises()
    expect(reads()).toHaveLength(1)
    expect(reads()[0]![1]).toMatchObject({ params: { model: `model-${expected}`, minutes: 60, ...(expected ? { group_id: expected } : {}) } })
    expect((wrapper!.vm as any).selectedGroup).toBe(expected)
    expect(apiClient.put).not.toHaveBeenCalled(); expect(apiClient.post).not.toHaveBeenCalled()
  })

  it('preserves manually selected ungrouped and custom public models on refresh', async () => {
    render(); await flushPromises()
    await choose('priority-group-filter','priorityScheduling.filters.ungrouped')
    await choose('priority-model-filter','another-model')
    await wrapper!.get('[data-testid="latency-refresh"]').trigger('click'); await flushPromises()
    expect((reads().at(-1)![1] as any).params).toEqual({ model: 'another-model', minutes: 60 })
  })

  it.each(['scope','window','logout','unmount'])('cancels old performance analysis on %s and rejects its late response', async action => {
    render(); await flushPromises()
    const pending = deferred<any>(); vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    const refresh = (wrapper!.vm as any).refresh(); await flushPromises()
    const signal = (reads().at(-1)![1] as any).signal
    if (action === 'scope') await choose('priority-group-filter','Composite (#23)')
    else if (action === 'window') await wrapper!.get('[data-testid="latency-window"]').setValue(15)
    else if (action === 'logout') fixture.auth.user = null
    else unmount()
    expect(signal.aborted).toBe(true)
    pending.resolve({ data: analysis({ model:'fixture-model',group_id:17,minutes:60 },3,'old') }); await refresh; await flushPromises()
    if (action === 'scope') expect((wrapper!.vm as any).snapshot.group_id).toBe(23)
    if (action === 'window') expect((wrapper!.vm as any).snapshot.window_minutes).toBe(15)
  })

  it('fences an in-flight read after saving without overwriting newer edits or selected scope', async () => {
    render(); await flushPromises()
    const pending=deferred<any>(); vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    const old=(wrapper!.vm as any).refresh(); await flushPromises()
    const saved=deferred<any>(); vi.mocked(apiClient.put).mockReturnValueOnce(saved.promise)
    const vm=wrapper!.vm as any; vm.draft.target_tps=40
    const saving=vm.save(); await flushPromises(); vm.draft.target_tps=60
    saved.resolve({data:{...config,target_tps:40}}); await saving; await flushPromises()
    pending.resolve({data:analysis({model:'fixture-model',group_id:17,minutes:60},3,'old')}); await old; await flushPromises()
    expect(vm.draft.target_tps).toBe(60); expect(vm.snapshot.config_revision).toBe('v1')
    expect(vm.selectedGroup).toBe(17)
  })

  it.each([{model:'other'},{group_id:23},{window_minutes:15},{history_ready:false},{history_status:'error'}])('rejects inconsistent or unavailable analysis %o', async change => {
    render(); await flushPromises()
    const bad=analysis({model:'fixture-model',group_id:17,minutes:60},3,'new')
    Object.assign(bad.snapshot,change); vi.mocked(apiClient.get).mockResolvedValueOnce({data:bad})
    await (wrapper!.vm as any).refresh(); await flushPromises()
    expect((wrapper!.vm as any).snapshot.config_revision).toBe('v1')
    expect(wrapper!.get('[data-testid="candidate-status"]').text()).toContain('priorityScheduling.reading.stale')
  })
})
