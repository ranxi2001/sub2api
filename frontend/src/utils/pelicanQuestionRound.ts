import { ref } from 'vue'
import { apiClient } from '@/api/client'
import type { PelicanTestConfig } from '@/types'

// Shared within this browser tab so opening another account reuses the round.
// Never reuse another administrator's round or persist credentials.
export const pelicanQuestionRound = ref<PelicanTestConfig | null>(null)
let owner: number | null = null
export async function preparePelicanQuestionRound(userId: number | null, signal?: AbortSignal) {
  const { data } = await apiClient.get<PelicanTestConfig>('/admin/accounts/pelican-question', { signal })
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  if (!data.pelican_question || !data.actual_prompt) throw new Error('Invalid question snapshot')
  owner = userId
  pelicanQuestionRound.value = data
  return data
}
export function currentPelicanQuestionRound(userId: number | null) {
  return owner === userId ? pelicanQuestionRound.value : null
}
