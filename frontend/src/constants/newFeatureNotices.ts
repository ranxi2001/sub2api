/**
 * Stable notice IDs. Add an ID when announcing a feature, then reuse it beside
 * every related menu, tab, and setting. Dismissals are saved per admin in that
 * browser; use a new ID to announce the feature again in a later release.
 */
export const NEW_FEATURE_NOTICES = {
  protocolFeatureSwitches: 'protocol-feature-switches-initial-announcement',
} as const
