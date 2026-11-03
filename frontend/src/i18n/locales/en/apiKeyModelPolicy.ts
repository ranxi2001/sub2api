export default {
  title: 'Key model settings',
  scopeHint: 'Administrator settings for this API key only. Saved separately from other key details.',
  modelMapping: 'Model mapping',
  mappingHint: 'For text requests, match the exact model ID and use the target model for routing and billing. Both model IDs must be allowed by the group. Other model IDs remain unchanged. Up to 100 mappings.',
  noMappings: 'No mappings. Requests use their original model IDs.',
  requestedModel: 'Requested model',
  targetModel: 'Target model',
  addMapping: 'Add mapping',
  removeMapping: 'Remove mapping',
  reasoningEffort: 'Fixed reasoning effort',
  followClient: 'Follow client',
  effortHint: 'For OpenAI requests, replace the client’s reasoning effort, including when omitted. Group limits still apply: an excessive level is downgraded or rejected according to the group policy. Choose a level supported by the target model.',
  invalidModel: 'Enter model IDs of 1–200 UTF-8 bytes without whitespace or control characters.',
  duplicateModel: 'The requested model “{model}” has more than one mapping.',
  tooManyMappings: 'Use at most 100 model mappings.',
  saved: 'Key model settings saved',
  saveFailed: 'Failed to save key model settings'
}
