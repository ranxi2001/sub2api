export default {
 column: 'Performance · 15 min', title: 'Account performance monitor',
 tps: 'Avg TPS', degradation: 'Probe degradation · 15m', ttft: 'Avg TTFT',
 uncollected: 'Not collected', unavailable: 'Unavailable', configure: 'Configure thresholds',
 paused: 'Automatically paused', enabled: 'Evaluate thresholds every 30 seconds',
 min: 'Pause below', max: 'Pause above', noBound: 'Off', save: 'Save thresholds',
 release: 'Release this pause and disable monitor', close: 'Close',
 saved: 'Thresholds saved', failed: 'Unable to update monitor. Reload after a conflict.',
 invalid: 'Enter finite nonnegative thresholds; minimum must not exceed maximum. Percentages must be 0–100.',
 semantics: 'Rolling 15 minutes by actual request/probe start time. TPS is the arithmetic mean of output tokens ÷ total request seconds (includes waiting), matching usage history. TTFT uses recorded first_token_ms for successful streaming text requests only. Only uniquely instrumented completed successful requests count; missing or failed evidence is not zero.',
 qualityNote: 'Degradation = explicitly incorrect completed quality probe rounds ÷ conclusive completed rounds × 100. Uses existing correct/incorrect judgments; transport failures, incomplete and inconclusive rounds are excluded. This is a probe failure percentage, not a percentage of all business requests. Results pool all models and effort levels on this account.',
 latchNote: 'Strictly below/above any configured bound pauses this monitor’s independent scheduling reason. Equality and missing samples do not trigger. Healthy/expired samples, disabling monitoring, or other account edits never clear this pause. Explicit release also disables this monitor; manual scheduling and other pause sources remain in effect.',
 samples: 'Samples: TPS {tps} · TTFT {ttft} · quality rounds {quality}',
 audit: 'Recent monitor events', noEvents: 'No monitor events', loading: 'Loading…',
 ms: 'ms', configureEvent: 'Thresholds configured', pauseEvent: 'Automatic pause', releaseEvent: 'Pause released; monitor disabled',
 refresh: 'Reload', reason: 'Triggered metrics: {reason}', auditUnavailable: 'Audit unavailable'
}
