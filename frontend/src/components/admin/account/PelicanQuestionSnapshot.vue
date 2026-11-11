<template>
  <details class="max-h-64 overflow-auto rounded border p-3 text-sm" data-testid="pelican-question-snapshot">
    <summary>{{ t('pelicanTests.questionBank.snapshot') }}<span v-if="question"> · {{ question.id }} · {{ question.version }}</span></summary>
    <template v-if="question">
      <p class="mt-2 break-all text-xs text-gray-500">{{ question.source }} · {{ question.round_id }}</p>
      <pre class="mt-2 whitespace-pre-wrap">{{ actualPrompt || question.prompt }}</pre>
      <ul class="mt-2 list-inside list-disc"><li v-for="criterion in question.criteria" :key="criterion">{{ criterion }}</li></ul>
    </template>
    <template v-else><p>{{ t('pelicanTests.questionBank.legacy') }}</p><pre v-if="actualPrompt" class="whitespace-pre-wrap">{{ actualPrompt }}</pre></template>
  </details>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { PelicanQuestion } from '@/types'
defineProps<{ question?: PelicanQuestion; actualPrompt?: string }>()
const { t } = useI18n()
</script>
