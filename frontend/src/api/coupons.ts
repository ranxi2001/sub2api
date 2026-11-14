import { apiClient } from './client'
import type { BasePaginationResponse } from '@/types'
export interface CouponTemplate {
  id: number; name: string; kind: 'fixed' | 'percent' | 'bonus'; currency: string
  min_amount: number; value: number; max_discount: number; valid_days: number
  stackable: boolean; enabled: boolean; max_issues: number; per_user_limit: number; issued_count: number
}
export interface UserCoupon {
  id: number; template_id: number; user_id: number; user_email?: string; snapshot: CouponTemplate
  source: string; status: 'available' | 'reserved' | 'used' | 'expired' | 'revoked'
  order_id: number | null; order_status: string; discount_amount: number; bonus_amount: number
  expires_at: string; used_at: string | null; created_at: string
}
export interface CouponUsage {
 order_id: number; coupon_id: number; user_id: number; user_email?: string; name: string
 status: string; order_status: string; original_amount: number; discount_amount: number
 bonus_amount: number; currency: string; created_at: string; used_at: string | null
}
export interface CouponQuote {
  coupon_id: number; name: string; currency: string; original_amount: number
  pay_base: number; pay_amount: number; credited: number; bonus_amount: number
  discount_amount: number; coupon_bonus: number; fee_rate: number
}
export interface CouponFilters { page?: number; page_size?: number; status?: string; search?: string; template_id?: number; user_id?: number }
export const couponAPI = {
 usage: (params: CouponFilters = {}) => apiClient.get<BasePaginationResponse<CouponUsage>>('/payment/coupons/usage', { params }),
  mine: (params: CouponFilters = {}) => apiClient.get<BasePaginationResponse<UserCoupon>>('/payment/coupons', { params }),
  quote: (id: number, amount: number, payment_type: string) => apiClient.post<CouponQuote>('/payment/coupons/' + id + '/quote', { amount, payment_type }),
}
export const adminCouponAPI = {
 usage: (params: CouponFilters = {}) => apiClient.get<BasePaginationResponse<CouponUsage>>('/admin/payment/coupons/usage', { params }),
  templates: () => apiClient.get<CouponTemplate[]>('/admin/payment/coupons/templates'),
  save: (template: CouponTemplate) => template.id
    ? apiClient.put<CouponTemplate>('/admin/payment/coupons/templates/' + template.id, template)
    : apiClient.post<CouponTemplate>('/admin/payment/coupons/templates', template),
  issue: (data: { template_id: number; user_ids: number[]; quantity: number; request_id: string }) => apiClient.post<{ count: number; ids: number[] }>('/admin/payment/coupons/issue', data),
  list: (params: CouponFilters = {}) => apiClient.get<BasePaginationResponse<UserCoupon>>('/admin/payment/coupons', { params }),
  revoke: (id: number) => apiClient.post('/admin/payment/coupons/' + id + '/revoke'),
}
