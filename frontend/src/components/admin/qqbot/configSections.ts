import type { QQBotConfig } from '@/api/qqbot'
export const configSections = {
  connection: ['enabled', 'app_id', 'app_secret', 'sandbox', 'clear_app_secret'],
  groups: ['allowed_groups', 'group_policies'],
  chat: ['ai_connection', 'site_api_key_id', 'site_group_id', 'site_owner_id', 'chat_disabled', 'base_url', 'api_key', 'chat_model', 'chat_protocol', 'system_prompt', 'max_output_tokens', 'history_messages', 'clear_api_key'],
  knowledge: ['site_knowledge'],
  images: ['image_ai_connection', 'image_site_api_key_id', 'image_site_group_id', 'image_site_owner_id', 'image_enabled', 'image_base_url', 'image_api_key', 'image_model', 'image_size', 'image_output_format', 'clear_image_api_key'],
  'group-admins': ['group_admins'], moderation: ['moderation'], autonomous: ['autonomous'], commands: ['custom_commands'],
  notifications: ['price_notifications', 'health_notifications', 'notification_quiet_hours'],
  limits: ['daily_chat_limit', 'daily_image_limit', 'total_daily_chat_limit', 'total_daily_image_limit', 'cooldown_seconds', 'reward_alert_daily', 'reward_budget_daily']
} as const satisfies Record<string, readonly (keyof QQBotConfig)[]>
export type ConfigSection = keyof typeof configSections
export function pickSection(config: QQBotConfig, section: ConfigSection): Partial<QQBotConfig> {
  return Object.fromEntries(configSections[section].map(key => [key, config[key]]))
}
export function cloneConfig<T>(value: T): T { return JSON.parse(JSON.stringify(value)) as T }
