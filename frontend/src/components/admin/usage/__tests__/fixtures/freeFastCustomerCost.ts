// Synthetic arithmetic fixture; not a customer record or evidence of a live flag.
export const freeFastCustomerCostFixture = {
  id: 1, request_id: 'synthetic-free-fast-cost', model: 'synthetic-astra',
  input_tokens: 8345, output_tokens: 136, cache_creation_tokens: 1197,
  cache_creation_30m_tokens: 1197, cache_creation_12h_tokens: 0,
  cache_creation_5m_tokens: 0, cache_creation_1h_tokens: 0, cache_read_tokens: 37469,
  image_input_tokens: 0, image_output_tokens: 0, image_input_cost: 0, image_output_cost: 0,
  input_cost: 0.1669, output_cost: 0.0136, cache_creation_cost: 0.0149625,
  cache_read_cost: 0.074938, total_cost: 0.2704005, actual_cost: 0.07134075,
  service_tier: 'priority', rate_multiplier: 0.5, account_rate_multiplier: 1,
  image_count: 0, billing_mode: 'token', stream: true,
  customer_cost_details: {
    version: 1, free_fast: true, input_cost: 0.08345, output_cost: 0.0068,
    cache_creation_cost: 0.0149625, cache_read_cost: 0.037469,
    image_input_cost: 0, image_output_cost: 0,
    total_cost: 0.1426815, actual_cost: 0.07134075, multiplier: 0.5,
  },
}
