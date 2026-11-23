import { describe, expect, it } from 'vitest'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_TOKEN,
  BILLING_MODE_VIDEO,
  getBillingModeLabel,
  getDisplayBillingMode,
  isImageUsage
} from '../billingMode'
import zhAdminResources from '@/i18n/locales/zh/admin/resources'

describe('billingMode helpers', () => {
  it('prefers explicit video mode over image_count', () => {
    expect(
      getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })
    ).toBe(BILLING_MODE_VIDEO)
    expect(isImageUsage({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })).toBe(false)
  })

  it('infers image when image_count set and mode missing', () => {
    expect(getDisplayBillingMode({ image_count: 2, billing_mode: null })).toBe(BILLING_MODE_IMAGE)
  })

  it('keeps token mode even with image_count', () => {
    expect(
      getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_TOKEN })
    ).toBe(BILLING_MODE_TOKEN)
  })

  it('uses the Chinese labels from the user CSV locale', () => {
    const t = (key: string) => {
      const leaf = key.split('.').pop() as keyof typeof zhAdminResources.usage
      return zhAdminResources.usage[leaf] as string
    }
    expect(getBillingModeLabel(BILLING_MODE_TOKEN, t)).toBe('按量')
    expect(getBillingModeLabel('per_request', t)).toBe('按次')
    expect(getBillingModeLabel(BILLING_MODE_IMAGE, t)).toBe('按次(图片)')
    expect(getBillingModeLabel(BILLING_MODE_VIDEO, t)).toBe('按次(视频)')
  })
})
