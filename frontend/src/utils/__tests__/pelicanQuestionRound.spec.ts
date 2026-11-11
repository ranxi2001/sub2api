import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '@/api/client'
import { currentPelicanQuestionRound, preparePelicanQuestionRound, pelicanQuestionRound } from '../pelicanQuestionRound'
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn() } }))
const question = (id: string) => ({ prompt: id, actual_prompt: `${id} with delivery contract`, pelican_question: { id, version: 'v1', source: 'builtin:svg-motion', round_id: `manual:${id}`, prompt: id, criteria: ['fixture'] } })
beforeEach(() => { vi.clearAllMocks(); pelicanQuestionRound.value = null })
describe('manual Pelican comparison rounds', () => {
 it('reuses a round across accounts of one admin and explicitly replaces it for the next comparison', async () => {
  vi.mocked(apiClient.get).mockResolvedValueOnce({ data: question('first') }).mockResolvedValueOnce({ data: question('next') })
  await preparePelicanQuestionRound(41)
  expect(currentPelicanQuestionRound(41)?.pelican_question?.id).toBe('first')
  expect(currentPelicanQuestionRound(42)).toBeNull()
  const accountA = JSON.parse(JSON.stringify(currentPelicanQuestionRound(41)))
  const accountB = JSON.parse(JSON.stringify(currentPelicanQuestionRound(41)))
  expect(accountA).toEqual(accountB)
  await preparePelicanQuestionRound(41)
  expect(currentPelicanQuestionRound(41)?.pelican_question?.id).toBe('next')
  expect(accountA.pelican_question.id).toBe('first')
  expect(apiClient.get).toHaveBeenCalledTimes(2)
 })
 it('does not replace the previous round after a failed or aborted preparation', async () => {
  vi.mocked(apiClient.get).mockResolvedValueOnce({ data: question('original') })
  await preparePelicanQuestionRound(41)
  vi.mocked(apiClient.get).mockRejectedValueOnce(new Error('offline'))
  await expect(preparePelicanQuestionRound(41)).rejects.toThrow('offline')
  const controller = new AbortController(); controller.abort()
  vi.mocked(apiClient.get).mockResolvedValueOnce({ data: question('cancelled') })
  await expect(preparePelicanQuestionRound(41, controller.signal)).rejects.toThrow('Aborted')
  expect(currentPelicanQuestionRound(41)?.pelican_question?.id).toBe('original')
 })
})
