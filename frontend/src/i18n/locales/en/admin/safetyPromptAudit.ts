export default {
  safetyPromptAudit: {
    nav: 'Safety prompts',
    title: 'Safety prompt audit',
    description: 'Review suspected model hits recorded by the safety prompt plugin. A record is an exact match awaiting human review; the plugin does not identify the local user.',
    refresh: 'Refresh',
    openPlugins: 'Plugins',
    loading: 'Reading plugin status…',
    loadFailed: 'Could not read plugin audit data. Please retry.',
    pluginMissing: 'Safety prompt plugin is not installed',
    pluginMissingHint: 'Install and enable local.sub2api.safety-prompt in Plugins, then refresh this page.',
    pluginDisabled: 'The plugin is not running, so audit events are unavailable.',
    pluginState: 'Plugin state',
    version: 'Version',
    storageReady: 'Persistent storage OK · retention {days} days / latest {limit} · {evictions} evicted by capacity',
    storageWarning: 'Storage warning: {warning}',
    noAudit: 'The plugin did not return audit data. Confirm it is enabled, or open Plugins for full status.',
    filters: {
      from: 'From date (local time)',
      to: 'To date (inclusive)',
      search: 'Search event / request ID / model / upstream account / summary',
      all: 'All retained records',
      today: 'Today'
    },
    count: 'Showing {shown} / {total}. Suspected model hits, not confirmed violations.',
    badRange: 'The start date cannot be later than the end date.',
    empty: 'No records match these filters.',
    table: {
      time: 'Time / event ID',
      model: 'Model / upstream account',
      user: 'Request user',
      summary: 'Redacted summary'
    },
    unknownUser: 'Unknown (not provided by plugin)',
    requestId: 'Request ID',
    upstreamAccount: 'Upstream account',
    exportJson: 'Export JSON',
    exportCsv: 'Export CSV',
    exportEmpty: 'There are no records to export.',
    eventId: 'Event ID',
    model: 'Model',
    action: 'Action',
    summary: 'Summary',
    noSummary: 'No text summary',
    requestSha: 'Pre-injection request SHA-256'
  }
}
