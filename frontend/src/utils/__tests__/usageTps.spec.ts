import { describe, expect, it } from 'vitest'

import { formatUsageOutputTps, usageOutputTps, usageOutputTpsUnavailableReason } from '../usageTps'

describe('usageTps', () => {
  it('averages all output tokens over the full recorded duration', () => {
    expect(usageOutputTps({ output_tokens: 872, duration_ms: 31_260, first_token_ms: 2_910 })).toBeCloseTo(27.895, 3)
    expect(formatUsageOutputTps({ output_tokens: 872, duration_ms: 31_260, first_token_ms: 2_910 })).toBe('27.9 t/s')
  })

  it.each([0, 1_000, 9_999, 10_000, 10_001, null, undefined])(
    'uses the same average with first_token_ms=%s, including missing and terminal-only output',
    (firstTokenMs) => {
      const row = { output_tokens: 500, duration_ms: 10_000, first_token_ms: firstTokenMs }
      expect(usageOutputTps(row)).toBe(50)
      expect(formatUsageOutputTps(row)).toBe('50.0 t/s')
    },
  )

  it.each([
    // Synthetic records reproduce the reported 109500 and 73000 t/s spikes
    // with 10ms and 1ms after the first token, respectively.
    { output_tokens: 1_095, duration_ms: 9_250, first_token_ms: 9_240, expected: '118 t/s' },
    { output_tokens: 73, duration_ms: 6_761, first_token_ms: 6_760, expected: '10.8 t/s' },
  ])('does not amplify buffered output with a $duration_ms ms duration', ({ expected, ...row }) => {
    expect(formatUsageOutputTps(row)).toBe(expected)
  })

  it('rounds to an integer from 100 t/s upwards', () => {
    expect(formatUsageOutputTps({ output_tokens: 3_000, duration_ms: 10_000, first_token_ms: 1_000 })).toBe('300 t/s')
    expect(formatUsageOutputTps({ output_tokens: 999, duration_ms: 10_000, first_token_ms: 0 })).toBe('99.9 t/s')
  })

  it('returns null when there is nothing meaningful to measure', () => {
    expect(usageOutputTps(null)).toBeNull()
    expect(usageOutputTps(undefined)).toBeNull()
    expect(usageOutputTps({ output_tokens: 0, duration_ms: 5_000, first_token_ms: 1_000 })).toBeNull()
    expect(usageOutputTps({ output_tokens: 100, duration_ms: null, first_token_ms: null })).toBeNull()
    expect(usageOutputTps({ output_tokens: 100, duration_ms: 0, first_token_ms: null })).toBeNull()
    expect(formatUsageOutputTps({ output_tokens: 0, duration_ms: 5_000 })).toBeNull()
  })

  it.each([-1, NaN, Infinity, -Infinity])('rejects invalid output or duration %s', (invalid) => {
    expect(usageOutputTps({ output_tokens: invalid, duration_ms: 10_000 })).toBeNull()
    expect(formatUsageOutputTps({ output_tokens: 500, duration_ms: invalid })).toBeNull()
  })

  it('skips image and video requests', () => {
    expect(usageOutputTps({ output_tokens: 4_160, duration_ms: 40_000, image_count: 1 })).toBeNull()
    expect(usageOutputTps({ output_tokens: 4_160, duration_ms: 40_000, image_output_tokens: 4_160 })).toBeNull()
    expect(usageOutputTps({ output_tokens: 100, duration_ms: 40_000, billing_mode: 'video' })).toBeNull()
  })

  it('does not report a rate for a single output token', () => {
    // Interrupted Anthropic stream: only message_start's placeholder output_tokens=1 was observed,
    // while the request streamed for 21s before the upstream failed.
    const interrupted = { output_tokens: 1, duration_ms: 21_135, first_token_ms: 973 }
    expect(usageOutputTps(interrupted)).toBeNull()
    expect(formatUsageOutputTps(interrupted)).toBeNull()
    expect(usageOutputTpsUnavailableReason(interrupted)).toBe('singleToken')

    expect(formatUsageOutputTps({ output_tokens: 2, duration_ms: 4_000 })).toBe('0.5 t/s')
    expect(usageOutputTpsUnavailableReason({ output_tokens: 2, duration_ms: 4_000 })).toBeNull()
  })

  it.each([
    [{ output_tokens: 500, duration_ms: 40_000, image_count: 2 }, 'media'],
    [{ output_tokens: 1, duration_ms: 40_000, billing_mode: 'video' }, 'media'],
    [{ output_tokens: 0, duration_ms: 5_000 }, 'noOutput'],
    [{ output_tokens: NaN, duration_ms: 5_000 }, 'noOutput'],
    [{ output_tokens: 1, duration_ms: 0 }, 'singleToken'],
    [{ output_tokens: 100, duration_ms: null }, 'noDuration'],
    [{ output_tokens: 100, duration_ms: Infinity }, 'noDuration'],
    [null, 'noOutput'],
  ] as const)('explains why TPS is unavailable for %o', (row, reason) => {
    expect(usageOutputTpsUnavailableReason(row)).toBe(reason)
    expect(usageOutputTps(row)).toBeNull()
  })
})
