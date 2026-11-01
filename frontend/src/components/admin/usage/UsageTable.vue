<template>
  <div :class="flat ? '' : 'card overflow-hidden'">
    <div
      v-if="showIpGeoToolbar"
      class="flex items-center justify-end gap-2 border-b border-gray-200 px-4 py-2 dark:border-dark-700"
    >
      <span v-if="pendingIpCount > 0" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('usage.ipGeo.pending', { count: pendingIpCount }) }}
      </span>
      <button
        type="button"
        class="inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400 dark:hover:bg-primary-900/30"
        :disabled="ipGeoBatchLoading || pendingIpCount === 0"
        @click="handleBatchFetchIpGeo"
      >
        {{ ipGeoBatchLoading ? t('usage.ipGeo.batchFetching') : t('usage.ipGeo.batchFetch') }}
      </button>
    </div>
    <div class="overflow-auto">
      <DataTable
        :columns="columns"
        :data="data"
        :loading="loading"
        :server-side-sort="serverSideSort"
        :default-sort-key="defaultSortKey"
        :default-sort-order="defaultSortOrder"
        @sort="(key, order) => $emit('sort', key, order)"
      >
        <template #cell-user="{ row }">
          <div class="text-sm">
            <span v-if="observerMode" class="font-medium text-gray-900 dark:text-white">{{ row.user?.email || "-" }}</span>
            <button
              v-else-if="row.user?.email"
              class="font-medium text-primary-600 underline decoration-dashed underline-offset-2 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
              @click="$emit('userClick', row.user_id, row.user?.email)"
              :title="t('admin.usage.clickToViewBalance')"
            >
              {{ row.user.email }}
            </button>
            <span v-else class="font-medium text-gray-900 dark:text-white">-</span>
            <span v-if="row.user?.deleted_at" class="ml-1 inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30">
              {{ t('admin.usage.userDeletedBadge') }}
            </span>
            <span class="ml-1 text-gray-500 dark:text-gray-400">#{{ row.user_id }}</span>
          </div>
        </template>

        <template #cell-api_key="{ row }">
          <span class="text-sm text-gray-900 dark:text-white">{{ row.api_key?.name || '-' }}</span>
        </template>

        <template #cell-account="{ row }">
          <span class="text-sm text-gray-900 dark:text-white">{{ row.account?.name || '-' }}</span>
        </template>

        <template #cell-model="{ row }">
          <div class="space-y-0.5 text-xs">
            <div v-if="row.model_mapping_chain && row.model_mapping_chain.includes('→')" class="space-y-0.5">
              <div v-for="(step, i) in row.model_mapping_chain.split('→')" :key="i"
                   class="break-all"
                   :class="i === 0 ? 'font-medium text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'"
                   :style="i > 0 ? `padding-left: ${i * 0.75}rem` : ''">
                <span v-if="i > 0" class="mr-0.5">↳</span>{{ step }}
              </div>
            </div>
            <div v-else-if="row.upstream_model && row.upstream_model !== row.model" class="space-y-0.5">
              <div class="break-all font-medium text-gray-900 dark:text-white">
                {{ row.model }}
              </div>
              <div class="break-all text-gray-500 dark:text-gray-400">
                <span class="mr-0.5">↳</span>{{ row.upstream_model }}
              </div>
            </div>
            <span v-else class="font-medium text-gray-900 dark:text-white">{{ row.model }}</span>
            <div
              v-if="userVisibleResponseModel(row)"
              class="break-all text-gray-500 dark:text-gray-400"
              data-testid="user-upstream-response-model"
            >
              <span class="mr-0.5">↳</span>{{ t('usage.upstreamResponseModel') }}: {{ userVisibleResponseModel(row) }}
            </div>
            <div
              v-if="row.upstream_model_mismatch === true && row.upstream_response_model"
              class="break-all pl-3 text-[11px]"
              :class="isLikelyModelVariant(row) ? 'text-amber-600 dark:text-amber-400' : 'text-orange-600 dark:text-orange-400'"
              :title="modelAuditTitle(row)"
            >
              <span class="mr-1">↳ {{ t('usage.upstreamResponseModel') }}:</span>{{ row.upstream_response_model }}
              <span
                class="ml-1 inline-flex rounded px-1 py-px text-[10px] font-medium ring-1 ring-inset"
                :class="isLikelyModelVariant(row)
                  ? 'bg-amber-50 text-amber-700 ring-amber-200 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-500/30'
                  : 'bg-orange-50 text-orange-700 ring-orange-200 dark:bg-orange-500/10 dark:text-orange-300 dark:ring-orange-500/30'"
              >
                {{ isLikelyModelVariant(row) ? t('usage.modelVariant') : t('usage.modelMismatch') }}
              </span>
            </div>
          </div>
        </template>

        <template #cell-reasoning_effort="{ row }">
          <div v-if="hasReasoningEffortMapping(row)" data-testid="reasoning-effort-cell" class="space-y-0.5 text-xs">
            <div class="font-medium text-gray-900 dark:text-white">
              {{ formatReasoningEffort(row.reasoning_effort) }}
            </div>
            <div class="text-gray-500 dark:text-gray-400">
              <span class="mr-0.5">↳</span>{{ formatReasoningEffort(row.upstream_reasoning_effort) }}
            </div>
          </div>
          <span v-else data-testid="reasoning-effort-cell" class="text-sm text-gray-900 dark:text-white">
            {{ formatReasoningEffort(row.reasoning_effort) }}
          </span>
        </template>

        <template #cell-endpoint="{ row }">
          <div class="max-w-[320px] space-y-1 text-xs">
            <div class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400">{{ t('usage.inbound') }}:</span>
              <span class="ml-1">{{ row.inbound_endpoint?.trim() || '-' }}</span>
            </div>
            <div v-if="showUpstreamEndpoint" class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400">{{ t('usage.upstream') }}:</span>
              <span class="ml-1">{{ row.upstream_endpoint?.trim() || '-' }}</span>
            </div>
          </div>
        </template>

        <template #cell-group="{ row }">
          <span v-if="row.group" class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200">
            {{ row.group.name }}
          </span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-stream="{ row }">
          <div class="flex flex-wrap items-center gap-1">
            <span data-testid="request-type-badge" class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getRequestTypeBadgeClass(row)">
              {{ getRequestTypeLabel(row) }}
            </span>
            <span
              v-if="row.native_compaction_v2"
              data-testid="native-compaction-badge"
              class="inline-flex items-center rounded bg-teal-100 px-2 py-0.5 text-xs font-medium text-teal-800 dark:bg-teal-900 dark:text-teal-200"
            >
              {{ t('usage.nativeCompactionV2') }}
            </span>
          </div>
        </template>

        <template #cell-billing_mode="{ row }">
          <span class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getBillingModeBadgeClass(getDisplayBillingMode(row))">
            {{ getBillingModeLabel(getDisplayBillingMode(row), t) }}
          </span>
        </template>

        <template #cell-tokens="{ row }">
          <!-- 图片生成请求（仅按次计费时显示图片格式） -->
          <div v-if="isImageUsage(row)" class="flex items-center gap-1.5">
            <svg class="h-4 w-4 text-indigo-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" />
            </svg>
            <span class="font-medium text-gray-900 dark:text-white">{{ row.image_count }}{{ t('usage.imageUnit') }}</span>
            <span class="text-gray-400">({{ formatImageBillingSize(row, t) }})</span>
          </div>
          <!-- Token 请求 -->
          <button v-else type="button" class="group relative flex items-center gap-1.5 text-left focus-visible:outline focus-visible:outline-primary-500"
            :aria-label="t('usage.tokenDetails')" :aria-expanded="tokenTooltipVisible && tokenTooltipData === row"
            @mouseenter="!isMobile && showTokenTooltip($event, row)"
            @mouseleave="!isMobile && scheduleHide(hideTokenTooltip)"
            @click="showTokenTooltip($event, row)">
            <div class="space-y-1 text-sm">
              <div class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <Icon name="arrowDown" size="sm" class="h-3.5 w-3.5 text-emerald-500" />
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.input_tokens?.toLocaleString() || 0 }}</span>
                </div>
                <div class="inline-flex items-center gap-1">
                  <Icon name="arrowUp" size="sm" class="h-3.5 w-3.5 text-violet-500" />
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.output_tokens?.toLocaleString() || 0 }}</span>
                </div>
              </div>
              <div v-if="row.cache_read_tokens > 0 || row.cache_creation_tokens > 0" class="flex items-center gap-2">
                <div v-if="row.cache_read_tokens > 0" class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-sky-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4" /></svg>
                  <span class="font-medium text-sky-600 dark:text-sky-400">{{ formatCacheTokens(row.cache_read_tokens) }}</span>
                </div>
                <div v-if="row.cache_creation_tokens > 0" class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-amber-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" /></svg>
                  <span class="font-medium text-amber-600 dark:text-amber-400">{{ formatCacheTokens(row.cache_creation_tokens) }}</span>
                  <span v-if="row.cache_creation_1h_tokens > 0" class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-orange-100 text-orange-600 ring-1 ring-inset ring-orange-200 dark:bg-orange-500/20 dark:text-orange-400 dark:ring-orange-500/30">1h</span>
                  <span v-if="row.cache_ttl_overridden" :title="t('usage.cacheTtlOverriddenHint')" class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30 cursor-help">R</span>
                </div>
              </div>
              <div v-if="hasImageInputTokens(row)" class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-fuchsia-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
                  <span class="font-medium text-fuchsia-600 dark:text-fuchsia-400">{{ row.image_input_tokens.toLocaleString() }}</span>
                </div>
              </div>
              <div v-if="hasImageOutputTokens(row)" class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-pink-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
                  <span class="font-medium text-pink-600 dark:text-pink-400">{{ row.image_output_tokens.toLocaleString() }}</span>
                </div>
              </div>
            </div>
            <!-- Token Detail Tooltip -->
            <span class="flex h-4 w-4 items-center justify-center rounded-full bg-gray-100 dark:bg-gray-700" aria-hidden="true">
              <Icon name="infoCircle" size="xs" class="text-gray-400" />
            </span>
          </button>
        </template>

        <template #cell-cost="{ row }">
          <button type="button" class="group relative text-left text-sm focus-visible:outline focus-visible:outline-primary-500"
            :aria-label="t('usage.costDetails')" :aria-expanded="tooltipVisible && tooltipData === row"
            @mouseenter="!isMobile && showTooltip($event, row)"
            @mouseleave="!isMobile && scheduleHide(hideTooltip)"
            @click="showTooltip($event, row)">
            <span class="flex items-center gap-1.5">
              <span class="font-medium text-green-600 dark:text-green-400">${{ row.actual_cost?.toFixed(6) || '0.000000' }}</span>
              <span
                v-if="showLongContextBadge && row.long_context_billing_applied"
                data-testid="long-context-billing-marker"
                :title="t('admin.usage.longContextPricingTooltip')"
                class="inline-flex items-center rounded px-1 py-px text-[10px] font-semibold leading-tight bg-amber-100 text-amber-700 ring-1 ring-inset ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30"
              >{{ t('admin.usage.longContext') }}</span>
              <!-- Cost Detail Tooltip -->
              <span class="flex h-4 w-4 items-center justify-center rounded-full bg-gray-100 dark:bg-gray-700" aria-hidden="true">
                <Icon name="infoCircle" size="xs" class="text-gray-400" />
              </span>
            </span>
            <span v-if="showAccountBilling && row.account_rate_multiplier != null" class="mt-0.5 block text-[11px] text-orange-500 dark:text-orange-400">
              A ${{ accountBilled(row).toFixed(6) }}
            </span>
          </button>
        </template>

        <!-- 状态列：首字/总耗时/TPS 用左侧色条表示健康度。缓存率百分数为绿色，色条延伸到这一行，但不改变上面三段的健康色。 -->
        <template #cell-latency="{ row }">
          <component :is="enableTimingDetails ? 'button' : 'div'" :type="enableTimingDetails ? 'button' : undefined" class="grid grid-cols-[0.25rem_max-content_max-content] items-baseline gap-x-2 gap-y-0.5 text-left text-xs" :class="enableTimingDetails ? 'hover:opacity-80 focus-visible:outline focus-visible:outline-primary-500' : ''" :aria-label="enableTimingDetails ? t('requestTiming.title') : undefined" @click="enableTimingDetails && (timingRecord = row)">
            <span
              data-testid="latency-bar"
              class="col-start-1 row-start-1 w-1 self-stretch rounded-full"
              :class="[showCacheRate ? 'row-span-4' : 'row-span-3', latencyBarClasses(row)]"
              :style="cacheRateBarStyle(row)"
              aria-hidden="true"
            ></span>
            <span class="col-start-2 row-start-1 text-gray-400 dark:text-gray-500">{{ t(enableTimingDetails ? 'usage.latencyFirstTokenShort' : row.queue_timing?.status === 'measured' ? 'usage.latencyFirstToken' : 'requestTiming.legacyFirst') }}</span>
            <span v-if="firstTokenQueueInclusive(row) != null" class="col-start-3 row-start-1 font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(firstTokenQueueInclusive(row) ?? 0)]" :title="row.queue_timing?.status === 'measured' ? firstTokenQueueTitle(row) : enableTimingDetails ? t('requestTiming.legacyFirst') : undefined">
              {{ formatDuration(firstTokenQueueInclusive(row)) }}
              <span v-if="!enableTimingDetails && row.queue_timing?.status === 'measured' && row.queue_wait_ms" class="ml-1 text-[10px] font-normal text-amber-600 dark:text-amber-400">{{ t('usage.queuePortion') }} {{ row.queue_timing.queue_clamped ? '≥' : '' }}{{ formatDuration(row.queue_wait_ms) }}</span>
            </span>
            <span v-else class="col-start-3 row-start-1 text-gray-400 dark:text-gray-500" :title="enableTimingDetails ? t('requestTiming.legacyFirst') : undefined">-</span>
            <span class="col-start-2 row-start-2 text-gray-400 dark:text-gray-500">{{ t(enableTimingDetails ? 'usage.latencyDurationShort' : row.queue_timing?.status === 'measured' ? 'usage.latencyDuration' : 'requestTiming.legacyTotal') }}</span>
            <span class="col-start-3 row-start-2 font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[durationSeverity(durationQueueInclusive(row) ?? 0)]" :title="row.queue_timing?.status === 'measured' ? t('usage.latencyDurationHint', { forward: formatDuration(row.queue_timing?.forward_duration_ms), queue: formatDuration(row.queue_wait_ms ?? 0) }) : enableTimingDetails ? t('requestTiming.legacyTotal') : undefined">
              {{ formatDuration(durationQueueInclusive(row)) }}
              <span v-if="!enableTimingDetails && row.queue_timing?.status === 'measured' && row.queue_wait_ms" class="ml-1 text-[10px] font-normal text-amber-600 dark:text-amber-400">{{ t('usage.queuePortion') }} {{ row.queue_timing.queue_clamped ? '≥' : '' }}{{ formatDuration(row.queue_wait_ms) }}</span>
            </span>
            <span class="col-start-2 row-start-3 text-gray-400 dark:text-gray-500">{{ t('usage.latencyTps') }}</span>
            <span
              v-if="formatUsageOutputTps(row)"
              data-testid="latency-tps"
              class="col-start-3 row-start-3 font-medium tabular-nums"
              :class="LATENCY_TEXT_CLASSES[tpsSeverity(usageOutputTps(row) ?? 0)]"
              :title="t('usage.latencyTpsHint')"
            >{{ formatUsageOutputTps(row) }}</span>
            <span v-else data-testid="latency-tps" class="col-start-3 row-start-3 text-gray-400 dark:text-gray-500">-</span>
            <template v-if="showCacheRate">
              <span class="col-start-2 row-start-4 text-gray-400 dark:text-gray-500">{{ t('usage.latencyCacheRate') }}</span>
              <span
                v-if="formatUsageCacheRate(row)"
                data-testid="latency-cache-rate"
                class="col-start-3 row-start-4 font-medium tabular-nums text-emerald-600 dark:text-emerald-400"
                :title="cacheRateTitle(row)"
              >{{ formatUsageCacheRate(row) }}</span>
              <span v-else data-testid="latency-cache-rate" class="col-start-3 row-start-4 text-gray-400 dark:text-gray-500">-</span>
            </template>
          </component>
        </template>

        <template #cell-content_reasoning_first_ms="{ row }">
          <span
            data-testid="content-timing-reasoning-first"
            class="tabular-nums"
            :class="row.content_timing?.reasoning_first_ms == null ? 'text-gray-400 dark:text-gray-500' : 'font-medium text-gray-700 dark:text-gray-200'"
            :title="t('latencyObservability.typedScope')"
          >{{ formatContentTiming(row, 'reasoning_first_ms') }}</span>
        </template>

        <template #cell-content_visible_text_first_ms="{ row }">
          <span
            data-testid="content-timing-visible-text-first"
            class="tabular-nums"
            :class="row.content_timing?.visible_text_first_ms == null ? 'text-gray-400 dark:text-gray-500' : 'font-medium text-gray-700 dark:text-gray-200'"
            :title="t('latencyObservability.typedScope')"
          >{{ formatContentTiming(row, 'visible_text_first_ms') }}</span>
        </template>

        <template #cell-content_response_byte_first_ms="{ row }">
          <span
            data-testid="content-timing-response-byte-first"
            class="tabular-nums"
            :class="row.content_timing?.response_byte_first_ms == null ? 'text-gray-400 dark:text-gray-500' : 'font-medium text-gray-700 dark:text-gray-200'"
            :title="t('latencyObservability.typedScope')"
          >{{ formatContentTiming(row, 'response_byte_first_ms') }}</span>
        </template>

        <template #cell-created_at="{ value }">
          <span class="text-sm text-gray-600 dark:text-gray-400">{{ formatDateTime(value) }}</span>
        </template>

        <template #cell-request_id="{ row }">
          <div v-if="row.request_id" class="flex max-w-[160px] items-center gap-1.5">
            <span class="truncate font-mono text-xs text-gray-500 dark:text-gray-400" :title="row.request_id">
              {{ row.request_id }}
            </span>
            <button
              type="button"
              class="shrink-0 rounded p-0.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
              :class="copiedRequestId === row.request_id ? 'text-green-500 hover:text-green-500' : ''"
              :title="copiedRequestId === row.request_id ? t('keys.copied') : t('keys.copyToClipboard')"
              @click="copyRequestId(row.request_id)"
            >
              <Icon :name="copiedRequestId === row.request_id ? 'check' : 'copy'" size="sm" class="h-3.5 w-3.5" />
            </button>
          </div>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-upstream_request_id="{ row }">
          <div v-if="row.upstream_request_id" class="flex max-w-[160px] items-center gap-1.5">
            <span class="truncate font-mono text-xs text-gray-500 dark:text-gray-400" :title="row.upstream_request_id">
              {{ row.upstream_request_id }}
            </span>
            <button
              type="button"
              class="shrink-0 rounded p-0.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
              :class="copiedRequestId === row.upstream_request_id ? 'text-green-500 hover:text-green-500' : ''"
              :title="copiedRequestId === row.upstream_request_id ? t('keys.copied') : t('keys.copyToClipboard')"
              @click="copyUpstreamRequestId(row.upstream_request_id)"
            >
              <Icon :name="copiedRequestId === row.upstream_request_id ? 'check' : 'copy'" size="sm" class="h-3.5 w-3.5" />
            </button>
          </div>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-user_agent="{ row }">
          <span v-if="row.user_agent" class="text-sm text-gray-600 dark:text-gray-400 block max-w-[320px] truncate" :title="row.user_agent">{{ formatUserAgent(row.user_agent) }}</span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-ip_address="{ row }">
          <div v-if="row.ip_address">
            <span class="text-sm font-mono text-gray-600 dark:text-gray-400">{{ row.ip_address }}</span>
            <IpGeoCell :ip="row.ip_address" />
          </div>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #empty><EmptyState :message="t('usage.noRecords')" /></template>
      </DataTable>
    </div>
  </div>

  <!-- Token Tooltip Portal -->
  <UsageDetailPopover :show="tokenTooltipVisible" :anchor="tokenTooltipAnchor" :title="t('usage.tokenDetails')"
    @close="hideTokenTooltip" @mouseenter="cancelHide" @mouseleave="scheduleHide(hideTokenTooltip)">
      <div class="max-w-[calc(100vw-1rem)] whitespace-normal rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <div class="space-y-1.5">
          <div>
            <div class="text-xs font-semibold text-gray-300 mb-1">{{ t('usage.tokenDetails') }}</div>
            <div v-if="tokenTooltipData && tokenTooltipData.input_tokens > 0 && !hasImageInputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.inputTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.input_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageInputTokens(tokenTooltipData) && textInputTokens(tokenTooltipData) > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.inputTokens') }}</span>
              <span class="font-medium text-white">{{ textInputTokens(tokenTooltipData).toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageInputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageInputTokens') }}</span>
              <span class="font-medium text-fuchsia-300">{{ tokenTooltipData.image_input_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.output_tokens > 0 && !hasImageOutputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.output_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageOutputTokens(tokenTooltipData) && textOutputTokens(tokenTooltipData) > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputTokens') }}</span>
              <span class="font-medium text-white">{{ textOutputTokens(tokenTooltipData).toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageOutputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageOutputTokens') }}</span>
              <span class="font-medium text-pink-300">{{ tokenTooltipData.image_output_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_creation_tokens > 0">
              <!-- 有 5m/1h 明细时，展开显示 -->
              <template v-if="tokenTooltipData.cache_creation_5m_tokens > 0 || tokenTooltipData.cache_creation_1h_tokens > 0">
                <div v-if="tokenTooltipData.cache_creation_5m_tokens > 0" class="flex items-center justify-between gap-4">
                  <span class="text-gray-400 flex items-center gap-1.5">
                    {{ t('admin.usage.cacheCreation5mTokens') }}
                    <span class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-amber-500/20 text-amber-400 ring-1 ring-inset ring-amber-500/30">5m</span>
                  </span>
                  <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_5m_tokens.toLocaleString() }}</span>
                </div>
                <div v-if="tokenTooltipData.cache_creation_1h_tokens > 0" class="flex items-center justify-between gap-4">
                  <span class="text-gray-400 flex items-center gap-1.5">
                    {{ t('admin.usage.cacheCreation1hTokens') }}
                    <span class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-orange-500/20 text-orange-400 ring-1 ring-inset ring-orange-500/30">1h</span>
                  </span>
                  <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_1h_tokens.toLocaleString() }}</span>
                </div>
              </template>
              <!-- 无明细时，只显示聚合值 -->
              <div v-else class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('admin.usage.cacheCreationTokens') }}</span>
                <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_tokens.toLocaleString() }}</span>
              </div>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_ttl_overridden" class="flex items-center justify-between gap-4">
              <span class="text-gray-400 flex items-center gap-1.5">
                {{ t('usage.cacheTtlOverriddenLabel') }}
                <span class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-500/20 text-rose-400 ring-1 ring-inset ring-rose-500/30">R-{{ tokenTooltipData.cache_creation_1h_tokens > 0 ? '5m' : '1H' }}</span>
              </span>
              <span class="font-medium text-rose-400">{{ tokenTooltipData.cache_creation_1h_tokens > 0 ? t('usage.cacheTtlOverridden1h') : t('usage.cacheTtlOverridden5m') }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_read_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.cache_read_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="showCacheRate && tokenTooltipData && formatUsageCacheRate(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.latencyCacheRate') }}</span>
              <span class="font-medium text-emerald-400" :title="cacheRateTitle(tokenTooltipData)">{{ formatUsageCacheRate(tokenTooltipData) }}</span>
            </div>
          </div>
          <div class="flex items-center justify-between gap-6 border-t border-gray-700 pt-1.5">
            <span class="text-gray-400">{{ t('usage.totalTokens') }}</span>
            <span class="font-semibold text-blue-400">{{ ((tokenTooltipData?.input_tokens || 0) + (tokenTooltipData?.output_tokens || 0) + (tokenTooltipData?.cache_creation_tokens || 0) + (tokenTooltipData?.cache_read_tokens || 0)).toLocaleString() }}</span>
          </div>
        </div>
      </div>
  </UsageDetailPopover>

  <!-- Cost Tooltip Portal -->
  <UsageDetailPopover :show="tooltipVisible" :anchor="tooltipAnchor" :title="t('usage.costDetails')"
    @close="hideTooltip" @mouseenter="cancelHide" @mouseleave="scheduleHide(hideTooltip)">
      <div class="max-w-[calc(100vw-1rem)] whitespace-normal rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <div class="space-y-1.5">
          <!-- Cost Breakdown -->
          <div class="mb-2 border-b border-gray-700 pb-1.5">
            <div class="text-xs font-semibold text-gray-300 mb-1">{{ t('usage.costDetails') }}</div>
            <div v-if="tooltipData && tooltipData.input_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.inputCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.input_cost.toFixed(8) }}</span>
            </div>
            <div v-if="tooltipData && hasImageInputCost(tooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageInputCost') }}</span>
              <span class="font-medium text-fuchsia-300">${{ tooltipData.image_input_cost.toFixed(8) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.output_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.output_cost.toFixed(8) }}</span>
            </div>
            <div v-if="tooltipData && hasImageOutputCost(tooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageOutputCost') }}</span>
              <span class="font-medium text-pink-300">${{ tooltipData.image_output_cost.toFixed(8) }}</span>
            </div>
            <!-- Token billing: show unit prices per 1M tokens -->
            <template v-if="tooltipData && !isImageUsage(tooltipData) && (!tooltipData.billing_mode || tooltipData.billing_mode === BILLING_MODE_TOKEN)">
              <div v-if="tooltipData && textInputTokens(tooltipData) > 0" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.inputTokenPrice') }}</span>
                <span class="font-medium text-sky-300">{{ formatTokenPricePerMillion(tooltipData.input_cost, textInputTokens(tooltipData)) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
              <div v-if="tooltipData && hasImageInputTokens(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageInputTokenPrice') }}</span>
                <span class="font-medium text-fuchsia-300">{{ formatTokenPricePerMillion(tooltipData.image_input_cost ?? 0, tooltipData.image_input_tokens) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
              <div v-if="tooltipData && tooltipData.output_cost > 0 && textOutputTokens(tooltipData) > 0" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.outputTokenPrice') }}</span>
                <span class="font-medium text-violet-300">{{ formatTokenPricePerMillion(tooltipData.output_cost, textOutputTokens(tooltipData)) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
              <div v-if="tooltipData && hasImageOutputTokens(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageOutputTokenPrice') }}</span>
                <span class="font-medium text-pink-300">{{ formatTokenPricePerMillion(tooltipData.image_output_cost ?? 0, tooltipData.image_output_tokens) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
            </template>
            <template v-else-if="tooltipData && isImageUsage(tooltipData)">
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageCount') }}</span>
                <span class="font-medium text-white">{{ tooltipData.image_count }}{{ t('usage.imageUnit') }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageBillingSize') }}</span>
                <span class="font-medium text-white">{{ formatImageBillingSize(tooltipData, t) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageSizeSource') }}</span>
                <span class="font-medium text-white">{{ formatImageSizeSource(tooltipData, t) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageInputSize') }}</span>
                <span class="font-medium text-white">{{ formatImageInputSize(tooltipData, t) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageOutputSize') }}</span>
                <span class="font-medium text-white">{{ formatImageOutputSize(tooltipData, t) }}</span>
              </div>
              <div v-if="formatImageSizeBreakdown(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageSizeBreakdown') }}</span>
                <span class="font-medium text-white">{{ formatImageSizeBreakdown(tooltipData) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageUnitPrice') }}</span>
                <span class="font-medium text-sky-300">${{ imageUnitPrice(tooltipData).toFixed(8) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageTotalPrice') }}</span>
                <span class="font-medium text-white">${{ tooltipData.total_cost?.toFixed(8) || '0.00000000' }}</span>
              </div>
            </template>
            <div v-else class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.unitPrice') }}</span>
              <span class="font-medium text-sky-300">${{ tooltipData?.total_cost?.toFixed(8) || '0.00000000' }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreationCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_creation_cost.toFixed(8) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_read_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_read_cost.toFixed(8) }}</span>
            </div>
          </div>
          <!-- Rate and Summary -->
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.serviceTier') }}</span>
            <span class="font-semibold text-cyan-300">{{ getUsageServiceTierLabel(tooltipData?.service_tier, t) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.rate') }}</span>
            <span class="font-semibold text-blue-400">{{ formatMultiplier(tooltipData?.rate_multiplier ?? 1) }}x</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.original') }}</span>
            <span class="font-medium text-white">${{ tooltipData?.total_cost?.toFixed(8) || '0.00000000' }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.userBilled') }}</span>
            <span class="font-semibold text-green-400">${{ tooltipData?.actual_cost?.toFixed(8) || '0.00000000' }}</span>
          </div>
          <!-- Account billing (separated from user billing) -->
          <template v-if="showAccountBilling">
            <div class="flex items-center justify-between gap-6 border-t border-gray-700 pt-1.5">
              <span class="text-gray-400">{{ t('usage.accountMultiplier') }}</span>
              <span class="font-semibold text-blue-400">{{ formatMultiplier(tooltipData?.account_rate_multiplier ?? 1) }}x</span>
            </div>
            <div class="flex items-center justify-between gap-6">
              <span class="text-gray-400">{{ t('usage.accountBilled') }}</span>
              <span class="font-semibold text-green-400">
                ${{ accountBilled({
                  total_cost: tooltipData?.total_cost,
                  account_stats_cost: tooltipData?.account_stats_cost,
                  account_rate_multiplier: tooltipData?.account_rate_multiplier,
                }).toFixed(8) }}
              </span>
            </div>
          </template>
        </div>
      </div>
  </UsageDetailPopover>
  <UsageTimingDialog :record="timingRecord" @close="timingRecord = null" />
</template>

<script setup lang="ts">
import { computed, ref, inject, onUnmounted } from 'vue'
import { useWindowSize } from '@vueuse/core'
import UsageDetailPopover from './UsageDetailPopover.vue'
import UsageTimingDialog from './UsageTimingDialog.vue'
import { observerUsageContext } from './observerUsageContext'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { formatDateTime, formatReasoningEffort, reasoningEffortValuesEqual } from '@/utils/format'
import { formatCacheTokens, formatMultiplier } from '@/utils/formatters'
import { formatTokenPricePerMillion } from '@/utils/usagePricing'
import { getUsageServiceTierLabel } from '@/utils/usageServiceTier'
import { resolveUsageRequestType } from '@/utils/usageRequestType'
import {
  LATENCY_BAR_FROM_CLASSES,
  LATENCY_BAR_TO_CLASSES,
  LATENCY_BAR_VIA_CLASSES,
  LATENCY_TEXT_CLASSES,
  durationSeverity,
  firstTokenSeverity,
  tpsSeverity,
  type LatencySeverity,
} from '@/utils/latencyHealth'
import { formatUsageOutputTps, usageOutputTps } from '@/utils/usageTps'
import { formatUsageCacheRate, showUsageCacheRate, usageCacheRateParts } from '@/utils/usageCacheRate'
import {
  BILLING_MODE_TOKEN,
  getBillingModeLabel,
  getBillingModeBadgeClass,
  isImageUsage,
  getDisplayBillingMode,
  imageUnitPrice,
} from '@/utils/billingMode'
import {
  formatImageBillingSize,
  formatImageInputSize,
  formatImageOutputSize,
  formatImageSizeBreakdown,
  formatImageSizeSource,
  hasImageOutputTokens,
  textOutputTokens,
  hasImageOutputCost,
  hasImageInputTokens,
  textInputTokens,
  hasImageInputCost,
} from '@/utils/imageUsage'

/** Compute the account-billed cost for display: (account_stats_cost ?? total_cost) * rate_multiplier */
function accountBilled(row: { total_cost?: number | null; account_stats_cost?: number | null; account_rate_multiplier?: number | null }): number {
  const base = row.account_stats_cost != null ? row.account_stats_cost : (row.total_cost ?? 0)
  const result = base * (row.account_rate_multiplier ?? 1)
  return Number.isNaN(result) ? 0 : result
}


import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import IpGeoCell from '@/components/common/IpGeoCell.vue'
import Icon from '@/components/icons/Icon.vue'
import { fetchBatch, getEntry } from '@/utils/ipGeoLookup'
import { userVisibleUpstreamResponseModel } from '@/utils/upstreamResponseModel'
import type { AdminUsageLog } from '@/types'
import type { Column } from '@/components/common/types'

interface Props {
  data: AdminUsageLog[]
  loading?: boolean
  columns: Column[]
  serverSideSort?: boolean
  defaultSortKey?: string
  defaultSortOrder?: 'asc' | 'desc'
  showAccountBilling?: boolean
  enableTimingDetails?: boolean
  showUpstreamEndpoint?: boolean
  /** 嵌入统一卡片内使用：去掉自身卡片外观 */
  flat?: boolean
  /** 管理端默认可见；用户端还要打开「向用户展示」。 */
  cacheRateAudience?: 'admin' | 'user'
}

const observerMode = inject(observerUsageContext, false)

const timingRecord = ref<AdminUsageLog | null>(null)

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  serverSideSort: false,
  defaultSortKey: '',
  defaultSortOrder: 'asc',
  showAccountBilling: true,
  enableTimingDetails: false,
  showUpstreamEndpoint: true,
  flat: false,
  cacheRateAudience: 'admin'
})
const emit = defineEmits<{
  userClick: [userID: number, email?: string]
  sort: [key: string, order: 'asc' | 'desc']
  ipGeoBatchFailed: []
}>()
const { t } = useI18n()
const { width: viewportWidth } = useWindowSize()
const isMobile = computed(() => viewportWidth.value < 768)
const appStore = useAppStore()
const copiedRequestId = ref<string | null>(null)
const showAccountBilling = props.showAccountBilling
const showUpstreamEndpoint = props.showUpstreamEndpoint
const ipGeoBatchLoading = ref(false)

// 长上下文计费 x2 徽标展示开关（公开设置，默认开启；未加载/缺失时按开启处理）
const showLongContextBadge = computed(() => appStore.cachedPublicSettings?.usage_show_long_context_badge !== false)
const showCacheRate = computed(() => showUsageCacheRate(appStore.cachedPublicSettings, props.cacheRateAudience))
const userVisibleResponseModel = (row: AdminUsageLog): string => (
  props.cacheRateAudience === 'user'
    ? userVisibleUpstreamResponseModel(appStore.cachedPublicSettings, row)
    : ''
)

const showIpGeoToolbar = computed(() => props.columns.some((col) => col.key === 'ip_address'))

const hasReasoningEffortMapping = (row: AdminUsageLog): boolean => {
  const requested = row.reasoning_effort?.trim() || ''
  const forwarded = row.upstream_reasoning_effort?.trim() || ''
  return requested !== '' && forwarded !== '' && !reasoningEffortValuesEqual(requested, forwarded)
}

const sentUpstreamModel = (row: AdminUsageLog): string => row.upstream_model?.trim() || row.model?.trim() || ''

const normalizeModelVariant = (model: string): string => model
  .trim()
  .toLowerCase()
  .replace(/-latest$/, '')
  .replace(/-\d{4}-\d{2}-\d{2}$/, '')
  .replace(/-\d{8}$/, '')

const isLikelyModelVariant = (row: AdminUsageLog): boolean => {
  const sent = sentUpstreamModel(row)
  const response = row.upstream_response_model?.trim() || ''
  return sent !== '' && response !== '' && normalizeModelVariant(sent) === normalizeModelVariant(response)
}

const modelAuditTitle = (row: AdminUsageLog): string => [
  `${t('usage.requestedModel')}: ${row.model || '-'}`,
  `${t('usage.sentUpstreamModel')}: ${sentUpstreamModel(row) || '-'}`,
  `${t('usage.upstreamResponseModel')}: ${row.upstream_response_model || '-'}`,
].join('\n')

const currentPageIps = computed(() =>
  Array.from(new Set(props.data.map((row) => row.ip_address).filter((ip): ip is string => Boolean(ip))))
)

const pendingIpCount = computed(() => {
  if (!showIpGeoToolbar.value) return 0
  return currentPageIps.value.filter((ip) => {
    const status = getEntry(ip).status
    return status === 'idle' || status === 'error'
  }).length
})

const handleBatchFetchIpGeo = async () => {
  ipGeoBatchLoading.value = true
  try {
    const ok = await fetchBatch(currentPageIps.value)
    if (!ok) emit('ipGeoBatchFailed')
  } finally {
    ipGeoBatchLoading.value = false
  }
}

const copyIdentifier = async (value: string, copiedMessage: string) => {
  try {
    await navigator.clipboard.writeText(value)
    copiedRequestId.value = value
    appStore.showSuccess(copiedMessage)
    window.setTimeout(() => {
      if (copiedRequestId.value === value) copiedRequestId.value = null
    }, 2000)
  } catch {
    appStore.showError(t('common.copyFailed'))
  }
}

const copyRequestId = (requestId: string) => copyIdentifier(requestId, t('admin.usage.requestIdCopied'))
const copyUpstreamRequestId = (upstreamRequestId: string) =>
  copyIdentifier(upstreamRequestId, t('admin.usage.upstreamRequestIdCopied'))

// Tooltip state - cost
const tooltipVisible = ref(false)
const tooltipAnchor = ref<DOMRect | null>(null)
const tooltipData = ref<AdminUsageLog | null>(null)

// Tooltip state - token
const tokenTooltipVisible = ref(false)
const tokenTooltipAnchor = ref<DOMRect | null>(null)
const tokenTooltipData = ref<AdminUsageLog | null>(null)

const getRequestTypeLabel = (row: AdminUsageLog): string => {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'cyber') return t('usage.cyber')
  if (requestType === 'live') return t('usage.live')
  if (requestType === 'ws_v2') return t('usage.ws')
  if (requestType === 'stream') return t('usage.stream')
  if (requestType === 'sync') return t('usage.sync')
  return t('usage.unknown')
}

const getRequestTypeBadgeClass = (row: AdminUsageLog): string => {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'cyber') return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
  if (requestType === 'live') return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200'
  if (requestType === 'ws_v2') return 'bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200'
  if (requestType === 'stream') return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
  if (requestType === 'sync') return 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200'
  return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
}



const formatUserAgent = (ua: string): string => {
  return ua
}

// 超过 1 分钟简化为 "Xm Ys"，免去人工换算（超过 1 小时再进位为 "Xh Ym"）
const formatDuration = (ms: number | null | undefined): string => {
  if (ms == null) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const totalSec = Math.round(ms / 1000)
  if (totalSec < 3600) return `${Math.floor(totalSec / 60)}m ${totalSec % 60}s`
  return `${Math.floor(totalSec / 3600)}h ${Math.floor((totalSec % 3600) / 60)}m`
}

type ContentTimingField = 'reasoning_first_ms' | 'visible_text_first_ms' | 'response_byte_first_ms'
const formatContentTiming = (row: AdminUsageLog, field: ContentTimingField): string => {
  const value = row.content_timing?.[field]
  return value == null ? '—' : formatDuration(value)
}

const cacheRateTitle = (row: AdminUsageLog): string | undefined => {
  const parts = usageCacheRateParts(row)
  if (!parts) return undefined
  return t('usage.latencyCacheRateHint', {
    input: parts.input.toLocaleString(),
    read: parts.read.toLocaleString(),
    write: parts.write.toLocaleString(),
  })
}

const LATENCY_BAR_COLORS: Record<LatencySeverity, string> = {
  good: '#10b981',
  warn: '#fbbf24',
  slow: '#f97316',
  critical: '#ef4444',
}

// Persisted boundary observations account for protocol-specific legacy overlap.
// Historical rows retain their legacy values and are not claimed as measured.
const firstTokenQueueInclusive = (row: AdminUsageLog): number | null =>
  row.queue_timing?.status === 'measured' ? row.queue_timing.server_first_token_ms : row.first_token_ms
const firstTokenQueueTitle = (row: AdminUsageLog): string =>
  t('usage.latencyFirstTokenHint', {
    first: formatDuration(row.queue_timing?.forward_first_token_ms),
    queue: formatDuration(row.queue_wait_ms),
  })
const durationQueueInclusive = (row: AdminUsageLog): number | null =>
  row.queue_timing?.status === 'measured' ? row.queue_timing.server_duration_ms : row.duration_ms

const latencySeverities = (row: AdminUsageLog) => {
  const duration = durationSeverity(durationQueueInclusive(row) ?? 0)
  const tps = usageOutputTps(row)
  const firstToken = firstTokenQueueInclusive(row)
  return {
    first: firstToken != null ? firstTokenSeverity(firstToken) : duration,
    duration,
    tps: tps != null ? tpsSeverity(tps) : duration,
  }
}

// 延迟色条三段依次对应首字/总耗时/TPS（30%/50%/70% 分别落在三行内）；无首字或无 TPS 的段沿用总耗时档。
const latencyBarClasses = (row: AdminUsageLog): string[] => {
  const severity = latencySeverities(row)
  return [
    'bg-gradient-to-b from-30% via-50% to-70%',
    LATENCY_BAR_FROM_CLASSES[severity.first],
    LATENCY_BAR_VIA_CLASSES[severity.duration],
    LATENCY_BAR_TO_CLASSES[severity.tps],
  ]
}

// 有缓存率时色条盖住四行：前三行仍用三行停点的相对位置，第四行固定绿色，不把缓存率算进健康度。
const cacheRateBarStyle = (row: AdminUsageLog): Record<string, string> | undefined => {
  if (!showCacheRate.value) return undefined
  const severity = latencySeverities(row)
  const first = LATENCY_BAR_COLORS[severity.first]
  const duration = LATENCY_BAR_COLORS[severity.duration]
  const tps = LATENCY_BAR_COLORS[severity.tps]
  const cache = LATENCY_BAR_COLORS.good
  return {
    backgroundImage: `linear-gradient(to bottom, ${first} 22.5%, ${duration} 37.5%, ${tps} 52.5%, ${tps} 75%, ${cache} 75%, ${cache} 100%)`,
  }
}

// Leave a small bridge between the trigger and the scrollable desktop popover.
let hideTimer: ReturnType<typeof setTimeout> | undefined
const cancelHide = () => { clearTimeout(hideTimer) }
const scheduleHide = (hide: () => void) => { cancelHide(); hideTimer = setTimeout(hide, 120) }
onUnmounted(cancelHide)

const hideTooltip = () => { tooltipVisible.value = false; tooltipData.value = null }
const hideTokenTooltip = () => { tokenTooltipVisible.value = false; tokenTooltipData.value = null }
const showTooltip = (event: Event, row: AdminUsageLog) => {
  cancelHide()
  hideTokenTooltip()
  tooltipAnchor.value = (event.currentTarget as HTMLElement).getBoundingClientRect()
  tooltipData.value = row
  tooltipVisible.value = true
}
const showTokenTooltip = (event: Event, row: AdminUsageLog) => {
  cancelHide()
  hideTooltip()
  tokenTooltipAnchor.value = (event.currentTarget as HTMLElement).getBoundingClientRect()
  tokenTooltipData.value = row
  tokenTooltipVisible.value = true
}
</script>
