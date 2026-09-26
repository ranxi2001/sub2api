<template>
  <div class="space-y-3">
    <label class="block text-xs">{{ t('admin.accounts.pelicanTest.question') }}</label>
    <Select :model-value="modelValue.question_kind || 'pelican'" :options="questionOptions" @update:model-value="selectQuestion" />
    <p v-if="isProbe" class="text-xs text-gray-500 dark:text-gray-400" data-testid="state-probe-hint">{{ t('admin.accounts.pelicanTest.stateProbeHint') }}</p>
    <template v-else>
      <TextArea :model-value="modelValue.prompt" :label="t('admin.accounts.pelicanTest.promptLabel')" :rows="3" @update:model-value="update('prompt', $event)" />
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label class="mb-1 block text-xs text-gray-600 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.reasoning') }}</label>
          <Select :model-value="modelValue.reasoning_effort" :options="reasoningOptions" @update:model-value="update('reasoning_effort', $event)" />
        </div>
        <Input :model-value="String(modelValue.parallel_count)" type="number" :label="t('admin.accounts.pelicanTest.parallel')" :hint="t('admin.accounts.pelicanTest.parallelHint')" @update:model-value="update('parallel_count', Number($event))" />
      </div>
    </template>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { questionPrompt, STATE_PROBE_QUESTION } from '@/utils/intelligenceTest'
import { useI18n } from 'vue-i18n'
import Input from '@/components/common/Input.vue'
import Select from '@/components/common/Select.vue'
import TextArea from '@/components/common/TextArea.vue'
import type { PelicanTestConfig } from '@/types'
const props = defineProps<{ modelValue: PelicanTestConfig }>()
const emit = defineEmits<{ 'update:modelValue': [value: PelicanTestConfig] }>()
const { t } = useI18n()
const isProbe = computed(() => props.modelValue.question_kind === STATE_PROBE_QUESTION)
const reasoningOptions = computed(() => ['low', 'medium', 'high'].map(value => ({ value, label: t(`admin.accounts.pelicanTest.reasoning${value[0].toUpperCase()}${value.slice(1)}`) })))
const questionOptions = computed(() => [
  ...['candy', 'pelican'].map(value => ({ value, label: t(`admin.accounts.pelicanTest.${value}Question`) })),
  { value: STATE_PROBE_QUESTION, label: t('admin.accounts.pelicanTest.stateProbeQuestion') }
])
function selectQuestion(value: string | number | boolean | null) {
  if (value === STATE_PROBE_QUESTION) {
    // 探针不需要题目；同一账号同一时刻只能跑一次探针，并行固定为 1。
    emit('update:modelValue', { ...props.modelValue, question_kind: STATE_PROBE_QUESTION, prompt: '', parallel_count: 1, reasoning_effort: props.modelValue.reasoning_effort || 'medium' })
    return
  }
  if (value !== 'candy' && value !== 'pelican') return
  emit('update:modelValue', { ...props.modelValue, question_kind: value, prompt: questionPrompt(value) })
}
function update(key: keyof PelicanTestConfig, value: string | number | boolean | null) {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}
</script>
