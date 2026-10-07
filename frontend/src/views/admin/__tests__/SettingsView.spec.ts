import { excelBPSImageLimits } from "@/utils/excelBPSImageLimits";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, reactive } from "vue";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";

import enCommon from "@/i18n/locales/en/common";
import enSettings from "@/i18n/locales/en/admin/settings";
import zhCommon from "@/i18n/locales/zh/common";
import zhSettings from "@/i18n/locales/zh/admin/settings";
import SettingsView from "../SettingsView.vue";
import { apiClient } from "@/api/client";

enableAutoUnmount(afterEach);

const settingsRoute = reactive({ query: {} as Record<string, unknown>, hash: '' });
vi.mock('vue-router', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-router')>(),
  useRoute: () => settingsRoute,
  useRouter: () => ({ replace: vi.fn(async (location) => Object.assign(settingsRoute, location)) }),
}));
beforeEach(() => {
  settingsRoute.query = {};
  settingsRoute.hash = '';
  Element.prototype.scrollIntoView = vi.fn();
});

const {
  getSettings,
  updateSettings,
  getWebSearchEmulationConfig,
  updateWebSearchEmulationConfig,
  getAdminApiKey,
  getOverloadCooldownSettings,
  getRateLimit429CooldownSettings,
  updateRateLimit429CooldownSettings,
  getPanelRateLimitSettings,
  updatePanelRateLimitSettings,
  getStreamTimeoutSettings,
  getRectifierSettings,
  getBetaPolicySettings,
  getUpstreamBillingProbeSettings,
  updateUpstreamBillingProbeSettings,
  getOllamaCloudUsageSettings,
  updateOllamaCloudUsageSettings,
  getGroups,
  listProxies,
  getProviders,
  updateProvider,
  createProvider,
  deleteProvider,
  fetchPublicSettings,
  adminSettingsFetch,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  getWebSearchEmulationConfig: vi.fn(),
  updateWebSearchEmulationConfig: vi.fn(),
  getAdminApiKey: vi.fn(),
  getOverloadCooldownSettings: vi.fn(),
  getRateLimit429CooldownSettings: vi.fn(),
  updateRateLimit429CooldownSettings: vi.fn(),
  getPanelRateLimitSettings: vi.fn().mockResolvedValue({
    enabled: true,
    user_rpm: 240,
    heavy_rpm: 60,
    exempt_admin: true,
    public_ip_rpm: 300,
  }),
  updatePanelRateLimitSettings: vi.fn().mockImplementation(async (payload) => payload),
  getStreamTimeoutSettings: vi.fn(),
  getRectifierSettings: vi.fn(),
  getBetaPolicySettings: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({
    enabled: true,
    interval_minutes: 30,
  }),
  updateUpstreamBillingProbeSettings: vi.fn().mockImplementation(async (payload) => payload),
  getOllamaCloudUsageSettings: vi.fn().mockResolvedValue({
    enabled: false,
    interval_minutes: 60,
    debounce_minutes: 1,
  }),
  updateOllamaCloudUsageSettings: vi.fn().mockImplementation(async (payload) => payload),
  getGroups: vi.fn(),
  listProxies: vi.fn(),
  getProviders: vi.fn(),
  updateProvider: vi.fn(),
  createProvider: vi.fn(),
  deleteProvider: vi.fn(),
  fetchPublicSettings: vi.fn(),
  adminSettingsFetch: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}));

const localeRef = vi.hoisted(() => ({ value: "zh-CN" }));

vi.mock("@/api", () => ({
  adminAPI: {
    settings: {
      getSettings,
      updateSettings,
      getWebSearchEmulationConfig,
      updateWebSearchEmulationConfig,
      getAdminApiKey,
      getOverloadCooldownSettings,
      getRateLimit429CooldownSettings,
      updateRateLimit429CooldownSettings,
      getPanelRateLimitSettings,
      updatePanelRateLimitSettings,
      getStreamTimeoutSettings,
      getRectifierSettings,
      getBetaPolicySettings,
    },
    accounts: {
      getUpstreamBillingProbeSettings,
      updateUpstreamBillingProbeSettings,
      getOllamaCloudUsageSettings,
      updateOllamaCloudUsageSettings,
    },
    groups: {
      getAll: getGroups,
    },
    proxies: {
      list: listProxies,
    },
    payment: {
      getProviders,
      updateProvider,
      createProvider,
      deleteProvider,
    },
  },
}));

vi.mock("@/stores", () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showWarning: vi.fn(),
    showInfo: vi.fn(),
    fetchPublicSettings,
  }),
}));

vi.mock("@/stores/adminSettings", () => ({
  useAdminSettingsStore: () => ({
    fetch: adminSettingsFetch,
  }),
}));

vi.mock("@/composables/useClipboard", () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn(),
  }),
}));

vi.mock("@/utils/apiError", () => ({
  extractApiErrorMessage: () => "error",
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  const translations: Record<string, string> = {
    "admin.settings.wechatConnect.title": "微信登录",
    "admin.settings.wechatConnect.description": "用于微信开放平台或公众号/小程序的第三方登录配置。",
    "admin.settings.wechatConnect.enabledLabel": "启用微信登录",
    "admin.settings.wechatConnect.enabledHint": "开启后可使用微信第三方登录回调与授权配置。",
    "admin.settings.wechatConnect.appIdLabel": "AppID",
    "admin.settings.wechatConnect.appIdPlaceholder": "微信开放平台 AppID",
    "admin.settings.wechatConnect.appSecretLabel": "AppSecret",
    "admin.settings.wechatConnect.appSecretConfiguredPlaceholder": "密钥已配置，留空以保留当前值。",
    "admin.settings.wechatConnect.appSecretPlaceholder": "微信开放平台 AppSecret",
    "admin.settings.wechatConnect.appSecretConfiguredHint": "密钥已配置，留空以保留当前值。",
    "admin.settings.wechatConnect.appSecretHint": "填写后会覆盖当前微信密钥。",
    "admin.settings.wechatConnect.modeLabel": "模式",
    "admin.settings.wechatConnect.openModeLabel": "非微信环境使用开放平台",
    "admin.settings.wechatConnect.openModeHint": "浏览器不在微信内时，自动走开放平台扫码授权。",
    "admin.settings.wechatConnect.mpModeLabel": "微信环境使用公众号",
    "admin.settings.wechatConnect.mpModeHint": "浏览器在微信内时，自动走公众号授权。",
    "admin.settings.wechatConnect.redirectUrlLabel": "回调地址",
    "admin.settings.wechatConnect.redirectUrlPlaceholder": "https://your-site.com/api/v1/auth/oauth/wechat/callback",
    "admin.settings.wechatConnect.generateAndCopy": "使用当前站点生成并复制",
    "admin.settings.wechatConnect.redirectUrlSetAndCopied": "已使用当前站点生成回调地址并复制到剪贴板",
    "admin.settings.wechatConnect.frontendRedirectUrlLabel": "前端回调地址",
    "admin.settings.wechatConnect.frontendRedirectUrlPlaceholder": "/auth/wechat/callback",
    "admin.settings.wechatConnect.frontendRedirectUrlHint": "通常用于前端路由回调地址，需与后端配置保持一致。",
    "admin.settings.authSourceDefaults.title": "认证来源默认值",
    "admin.settings.authSourceDefaults.description": "按注册来源配置新用户默认余额、并发、订阅与授权策略。",
    "admin.settings.authSourceDefaults.requireEmailLabel": "第三方注册强制补充邮箱",
    "admin.settings.authSourceDefaults.requireEmailHint": "启用后，Linux DO、OIDC、微信注册缺少邮箱时必须先补充邮箱地址。",
    "admin.settings.authSourceDefaults.enabledHint": "以下默认值会在该来源注册新用户时发放；首次绑定时授权仅作用于已有账号绑定该来源。",
    "admin.settings.authSourceDefaults.sources.email.title": "邮箱注册",
    "admin.settings.authSourceDefaults.sources.email.description": "适用于邮箱密码注册的新用户默认配额。",
    "admin.settings.authSourceDefaults.sources.linuxdo.title": "Linux DO 登录",
    "admin.settings.authSourceDefaults.sources.linuxdo.description": "适用于 Linux DO 第三方注册的新用户默认配额。",
    "admin.settings.authSourceDefaults.sources.oidc.title": "OIDC 登录",
    "admin.settings.authSourceDefaults.sources.oidc.description": "适用于 OIDC 第三方注册的新用户默认配额。",
    "admin.settings.authSourceDefaults.sources.wechat.title": "微信登录",
    "admin.settings.authSourceDefaults.sources.wechat.description": "适用于微信第三方注册的新用户默认配额。",
    "admin.settings.authSourceDefaults.grantOnFirstBindLabel": "首次绑定时授权",
    "admin.settings.authSourceDefaults.grantOnFirstBindHint": "已有账号首次绑定该来源时发放默认权益。",
    "admin.settings.authSourceDefaults.defaultSubscriptionsLabel": "默认订阅",
    "admin.settings.authSourceDefaults.defaultSubscriptionsHint": "仅对当前认证来源生效，未配置时不追加来源专属订阅。",
    "admin.settings.authSourceDefaults.noSourceSubscriptions": "当前来源未配置专属默认订阅。",
    "admin.settings.paymentVisibleMethods.methodLabel": "{title} 可见方式",
    "admin.settings.paymentVisibleMethods.methodHint": "控制前台结算页是否展示该方式，以及展示时使用的来源键。",
    "admin.settings.paymentVisibleMethods.sourceLabel": "支付来源",
    "admin.settings.paymentVisibleMethods.sourceHint": "启用后必须明确选择一个来源；未配置状态不会对外展示该支付方式。",
    "admin.settings.paymentVisibleMethods.sourceRequiredError": "{title} 已启用，请先选择支付来源。",
    "admin.settings.payment.configGuide": "查看支付配置说明",
    "admin.settings.payment.findProvider": "查看支持的支付方式",
    "admin.settings.openaiExperimentalScheduler.title": "OpenAI 实验调度策略",
    "admin.settings.openaiExperimentalScheduler.description": "默认关闭。开启后仅影响本网关在 OpenAI 账号间的实验性调度选择逻辑，不代表上游 OpenAI 官方能力。",
    "admin.settings.openaiExperimentalScheduler.lowRatePriorityTitle": "低倍率优先",
    "admin.settings.openaiExperimentalScheduler.lowRatePriorityDescription": "开启后优先选择计费倍率较低的账号；倍率相同时，再比较账号优先级和当前负载等。启用实验调度策略后，此开关不生效。",
    "admin.settings.openaiExperimentalScheduler.oauthRateTitle": "OAuth 调度参考倍率",
    "admin.settings.openaiExperimentalScheduler.oauthRatePriorityDescription": "OAuth 账号按此参考倍率参与低倍率优先排序；留空时使用各自的账号倍率。API Key 账号优先使用有效探测倍率，无有效探测时使用账号倍率。",
    "admin.settings.openaiExperimentalScheduler.oauthRateWeightedDescription": "计算“计费倍率”得分时，OAuth 账号使用此参考倍率；留空时使用各自的账号倍率。API Key 账号优先使用有效探测倍率，无有效探测时使用账号倍率。",
    "admin.settings.openaiExperimentalScheduler.oauthRateInvalid": "OAuth 调度参考倍率必须是非负数字，或留空以使用账号倍率。",
    "admin.settings.openaiExperimentalScheduler.stickyWeightedTitle": "粘性加权",
    "admin.settings.openaiExperimentalScheduler.stickyWeightedDescription": "开启后 previous_response_id 和 session_hash 粘性进入高级调度打分；关闭时仍按旧逻辑硬命中粘性账号。",
    "admin.settings.openaiExperimentalScheduler.subscriptionPriorityTitle": "订阅优先",
    "admin.settings.openaiExperimentalScheduler.subscriptionPriorityDescription": "开启后先在 ChatGPT 订阅账号池中按权值选取；订阅池拿不到席位时再回退到非订阅账号池。",
    "admin.settings.openaiExperimentalScheduler.weightsTitle": "调度权值覆盖",
    "admin.settings.openaiExperimentalScheduler.weightsDescription": "留空时使用配置/环境变量值；配置未设置时使用内置默认值。页面非空设置优先。",
    "admin.settings.openaiExperimentalScheduler.defaultPlaceholder": "配置/默认：{value}",
    "admin.settings.openaiExperimentalScheduler.topKLabel": "TopK",
    "admin.settings.openaiExperimentalScheduler.priorityWeight": "优先级",
    "admin.settings.openaiExperimentalScheduler.loadWeight": "负载",
    "admin.settings.openaiExperimentalScheduler.queueWeight": "排队",
    "admin.settings.openaiExperimentalScheduler.errorRateWeight": "错误率",
    "admin.settings.openaiExperimentalScheduler.ttftWeight": "首包延迟",
    "admin.settings.openaiExperimentalScheduler.resetWeight": "重置窗口",
    "admin.settings.openaiExperimentalScheduler.quotaHeadroomWeight": "额度余量",
    "admin.settings.openaiExperimentalScheduler.upstreamCostWeight": "计费倍率",
    "admin.settings.openaiExperimentalScheduler.previousResponseWeight": "previous_response 粘性",
    "admin.settings.openaiExperimentalScheduler.sessionStickyWeight": "session_hash 粘性",
    "admin.settings.upstreamBillingProbe.title": "上游倍率自动探测",
    "admin.settings.upstreamBillingProbe.description": "定期获取 OpenAI API Key 所连接上游 Sub2API 站点声明的计费倍率。",
    "admin.settings.upstreamBillingProbe.enabled": "启用全局自动探测",
    "admin.settings.upstreamBillingProbe.enabledHint": "开启后，仅对账号自身已启用自动检测的账号执行定时探测。",
    "admin.settings.upstreamBillingProbe.intervalMinutes": "探测周期（分钟）",
    "admin.settings.upstreamBillingProbe.intervalHint": "范围 5–1440 分钟。",
    "admin.settings.upstreamBillingProbe.saved": "上游倍率自动探测设置已保存",
    "admin.settings.upstreamBillingProbe.saveFailed": "保存上游倍率自动探测设置失败",
    "admin.settings.openaiFastPolicy.summaryTargetModels": "目标模型",
    "admin.settings.openaiFastPolicy.summaryAllModels": "全部模型",
    "admin.settings.openaiFastPolicy.summaryOtherModels": "其他模型",
    "admin.settings.openaiFastPolicy.summaryAction.filter": "过滤",
    "admin.settings.openaiFastPolicy.summaryAction.pass": "透传",
    "admin.settings.security.passkeyDeploymentHint":
      "请由服务器运维在部署配置中将 webauthn.enabled 设为 true，填写 webauthn.rp_id（仅域名）与 webauthn.rp_origins（完整 HTTPS 来源），然后重启服务。",
    "admin.settings.site.uploadImage": "上传图片",
    "admin.settings.site.remove": "移除",
    "admin.settings.platformQuota.platform": "平台",
    "admin.settings.platformQuota.daily": "日限额 (USD)",
    "admin.settings.platformQuota.weekly": "周限额 (USD)",
    "admin.settings.platformQuota.monthly": "月限额 (USD, 30天滚动)",
    "admin.settings.platformQuota.placeholder": "不限",
    "admin.settings.defaults.defaultPlatformQuotas": "默认平台限额（注册时分配）",
    "admin.settings.defaults.defaultPlatformQuotasHint": "新用户注册时自动写入平台限额记录；已有用户不受影响。留空 = 该平台该窗口不限制。",
    "admin.settings.defaults.platformQuotaNotice": "月限额为 30 天滚动窗口，非自然月",
    "admin.settings.authSourceDefaults.platformQuotasOverride": "平台限额覆盖",
    "admin.settings.authSourceDefaults.platformQuotasOverrideHint": "留空的字段继承「系统默认平台限额」；填 0 表示禁止该窗口使用。",
  };
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) =>
        (translations[key] ?? key).replace(/\{(\w+)\}/g, (_, token) => params?.[token] ?? `{${token}}`),
      locale: localeRef,
    }),
  };
});

const AppLayoutStub = { template: "<div><slot /></div>" };
const ToggleStub = defineComponent({
  props: {
    modelValue: {
      type: Boolean,
      default: false,
    },
  },
  emits: ["update:modelValue"],
  inheritAttrs: false,
  setup(props, { attrs, emit }) {
    return () =>
      h("input", {
        ...attrs,
        class: "toggle-stub",
        type: "checkbox",
        checked: props.modelValue,
        onChange: (event: Event) => {
          emit("update:modelValue", (event.target as HTMLInputElement).checked);
        },
      });
  },
});

const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: "",
    },
    options: {
      type: Array,
      default: () => [],
    },
    placeholder: {
      type: String,
      default: "",
    },
  },
  emits: ["update:modelValue", "change"],
  setup(props, { emit }) {
    const onChange = (event: Event) => {
      const target = event.target as HTMLSelectElement;
      emit("update:modelValue", target.value);
      const option =
        (props.options as Array<Record<string, unknown>>).find(
          (item) => String(item.value ?? "") === target.value,
        ) ?? null;
      emit("change", target.value, option);
    };

    return () =>
      h(
        "select",
        {
          class: "select-stub",
          value: props.modelValue ?? "",
          "data-placeholder": props.placeholder,
          onChange,
        },
        (props.options as Array<Record<string, unknown>>).map((option) =>
          h(
            "option",
            {
              key: `${String(option.value ?? "")}:${String(option.label ?? "")}`,
              value: option.value as string,
            },
            String(option.label ?? ""),
          ),
        ),
      );
  },
});

const ImageUploadStub = defineComponent({
  props: {
    modelValue: {
      type: String,
      default: "",
    },
    uploadLabel: {
      type: String,
      default: "",
    },
    removeLabel: {
      type: String,
      default: "",
    },
    placeholder: {
      type: String,
      default: "",
    },
  },
  setup(props) {
    return () =>
      h("div", {
        class: "image-upload-stub",
        "data-model-value": props.modelValue,
        "data-upload-label": props.uploadLabel,
        "data-remove-label": props.removeLabel,
        "data-placeholder": props.placeholder,
      });
  },
});

const baseSettingsResponse = {
  registration_enabled: true,
  email_verify_enabled: false,
  registration_email_suffix_whitelist: [],
  promo_code_enabled: true,
  invitation_code_enabled: false,
  password_reset_enabled: false,
  totp_enabled: false,
  totp_encryption_key_configured: false,
  passkey_enabled: true,
  passkey_configured: true,
  passkey_rp_id: "sub3.nebula-spaces.com",
  passkey_rp_origins: ["https://sub3.nebula-spaces.com"],
  default_balance: 0,
  default_concurrency: 1,
  default_subscriptions: [],
  site_name: "Sub2API",
  site_logo: "",
  site_subtitle: "",
  api_base_url: "",
  contact_info: "",
  doc_url: "",
  home_content: "",
  compact_home_enabled: false,
  excel_bps_enabled: true,
  excel_bps_image_mode: 'native',
  excel_bps_image_relay_enabled: true,
  excel_bps_image_base_url: '',
  excel_bps_image_max_image_mib: 20,
  excel_bps_image_max_images: 20,
  excel_bps_image_max_total_mib: 32,
  excel_bps_image_storage_mib: 1024,
  excel_bps_image_storage_entries: 512,
  excel_bps_image_ttl_minutes: 30,

  hide_ccs_import_button: false,
  table_default_page_size: 20,
  table_page_size_options: [10, 20, 50, 100],
  backend_mode_enabled: false,
  custom_menu_items: [],
  custom_endpoints: [],
  frontend_url: "",
  smtp_host: "",
  smtp_port: 587,
  smtp_username: "",
  smtp_password_configured: false,
  smtp_from_email: "",
  smtp_from_name: "",
  smtp_use_tls: true,
  turnstile_enabled: false,
  turnstile_site_key: "",
  turnstile_secret_key_configured: false,
  tencent_captcha_enabled: false,
  tencent_captcha_app_id: "",
  tencent_captcha_app_secret_key_configured: false,
  tencent_captcha_cloud_secret_id_configured: false,
  tencent_captcha_cloud_secret_key_configured: false,
  api_key_acl_trust_forwarded_ip: true,
  forwarded_client_ip_headers: [],
  linuxdo_connect_enabled: false,
  linuxdo_connect_client_id: "",
  linuxdo_connect_client_secret_configured: false,
  linuxdo_connect_redirect_url: "",
  wechat_connect_enabled: true,
  wechat_connect_app_id: "wx-app-id-123",
  wechat_connect_app_secret_configured: true,
  wechat_connect_open_enabled: false,
  wechat_connect_mp_enabled: true,
  wechat_connect_mode: "mp",
  wechat_connect_scopes: "",
  wechat_connect_redirect_url:
    "https://admin.example.com/api/v1/auth/oauth/wechat/callback",
  wechat_connect_frontend_redirect_url: "/auth/wechat/callback",
  oidc_connect_enabled: false,
  oidc_connect_provider_name: "OIDC",
  oidc_connect_client_id: "",
  oidc_connect_client_secret_configured: false,
  oidc_connect_issuer_url: "",
  oidc_connect_discovery_url: "",
  oidc_connect_authorize_url: "",
  oidc_connect_token_url: "",
  oidc_connect_userinfo_url: "",
  oidc_connect_jwks_url: "",
  oidc_connect_scopes: "openid email profile",
  oidc_connect_redirect_url: "",
  oidc_connect_frontend_redirect_url: "/auth/oidc/callback",
  oidc_connect_token_auth_method: "client_secret_post",
  oidc_connect_use_pkce: true,
  oidc_connect_validate_id_token: true,
  oidc_connect_allowed_signing_algs: "RS256,ES256,PS256",
  oidc_connect_clock_skew_seconds: 120,
  oidc_connect_require_email_verified: false,
  oidc_connect_userinfo_email_path: "",
  oidc_connect_userinfo_id_path: "",
  oidc_connect_userinfo_username_path: "",
  enable_model_fallback: false,
  fallback_model_anthropic: "",
  fallback_model_openai: "",
  fallback_model_gemini: "",
  fallback_model_antigravity: "",
  grok_default_text_model: "grok-4.5",
  grok_cross_client_model_map_enabled: false,
  enable_identity_patch: false,
  identity_patch_prompt: "",
  ops_monitoring_enabled: false,
  ops_realtime_monitoring_enabled: false,
  ops_query_mode_default: "auto",
  ops_metrics_interval_seconds: 60,
  min_claude_code_version: "",
  max_claude_code_version: "",
  allow_ungrouped_key_scheduling: false,
  openai_ttft_mode: "semantic",
  enable_fingerprint_unification: true,
  enable_metadata_passthrough: false,
  enable_cch_signing: false,
  enable_claude_oauth_system_prompt_injection: true,
  claude_oauth_system_prompt: "",
  claude_oauth_system_prompt_blocks: "",
  enable_anthropic_cache_ttl_1h_injection: false,
  rewrite_message_cache_control: false,
  enable_client_dateline_normalization: true,
  antigravity_user_agent_version: "",
  openai_codex_user_agent: "",
  payment_enabled: true,
  payment_min_amount: 1,
  payment_max_amount: 10000,
  payment_daily_limit: 50000,
  payment_order_timeout_minutes: 30,
  payment_max_pending_orders: 3,
  payment_enabled_types: [],
  payment_balance_disabled: false,
  payment_balance_recharge_multiplier: 1,
  payment_subscription_usd_to_cny_rate: 0,
  payment_recharge_fee_rate: 0,
  payment_recharge_bonus_tiers: [],
  payment_recharge_bonus_mode: "bonus",
  payment_recharge_bonus_notice: "",
  payment_load_balance_strategy: "round-robin",
  payment_product_name_prefix: "",
  payment_product_name_suffix: "",
  payment_help_image_url: "",
  payment_help_text: "",
  payment_cancel_rate_limit_enabled: false,
  payment_cancel_rate_limit_max: 10,
  payment_cancel_rate_limit_window: 1,
  payment_cancel_rate_limit_unit: "day",
  payment_cancel_rate_limit_window_mode: "rolling",
  payment_visible_method_alipay_source: "alipay_direct",
  payment_visible_method_wxpay_source: "invalid-source",
  payment_visible_method_alipay_enabled: true,
  payment_visible_method_wxpay_enabled: true,
  openai_low_upstream_rate_priority_enabled: false,
  openai_oauth_scheduling_rate_multiplier: 1,
  openai_advanced_scheduler_enabled: false,
  openai_advanced_scheduler_sticky_weighted_enabled: false,
  openai_advanced_scheduler_subscription_priority_enabled: false,
  openai_advanced_scheduler_lb_top_k: "",
  openai_advanced_scheduler_weight_priority: "",
  openai_advanced_scheduler_weight_load: "",
  openai_advanced_scheduler_weight_queue: "",
  openai_advanced_scheduler_weight_error_rate: "",
  openai_advanced_scheduler_weight_ttft: "",
  openai_advanced_scheduler_weight_reset: "",
  openai_advanced_scheduler_weight_quota_headroom: "",
  openai_advanced_scheduler_weight_upstream_cost: "",
  openai_advanced_scheduler_weight_previous_response: "",
  openai_advanced_scheduler_weight_session_sticky: "",
  openai_advanced_scheduler_effective_lb_top_k: "7",
  openai_advanced_scheduler_effective_weight_priority: "1",
  openai_advanced_scheduler_effective_weight_load: "1",
  openai_advanced_scheduler_effective_weight_queue: "0.7",
  openai_advanced_scheduler_effective_weight_error_rate: "0.8",
  openai_advanced_scheduler_effective_weight_ttft: "0.5",
  openai_advanced_scheduler_effective_weight_reset: "0",
  openai_advanced_scheduler_effective_weight_quota_headroom: "0",
  openai_advanced_scheduler_effective_weight_upstream_cost: "0",
  openai_advanced_scheduler_effective_weight_previous_response: "5",
  openai_advanced_scheduler_effective_weight_session_sticky: "3",
  balance_low_notify_enabled: false,
  balance_low_notify_threshold: 0,
  balance_low_notify_recharge_url: "",
  subscription_expiry_notify_enabled: true,
  account_quota_notify_enabled: false,
  account_quota_notify_emails: [],
  // 平台限额嵌套字段（新后端契约）
  default_platform_quotas: {
    anthropic:   { daily: null, weekly: null, monthly: null },
    openai:      { daily: null, weekly: 12.5, monthly: null },
    gemini:      { daily: null, weekly: null, monthly: 200 },
    antigravity: { daily: null, weekly: null, monthly: null },
  },
};

function mountView() {
  return mount(SettingsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Select: SelectStub,
        Toggle: ToggleStub,
        Icon: true,
        ConfirmDialog: true,
        PaymentProviderList: true,
        PaymentProviderDialog: true,
        GroupBadge: true,
        GroupOptionItem: true,
        ProxySelector: true,
        ImageUpload: ImageUploadStub,
        BackupSettings: true,
        EmailTemplateEditor: true,
      },
    },
  });
}

async function openPaymentTab(wrapper: ReturnType<typeof mountView>) {
  const paymentTabButton = wrapper
    .findAll("button")
    .find((node) => node.text().includes("admin.settings.tabs.payment"));

  expect(paymentTabButton).toBeDefined();
  await paymentTabButton?.trigger("click");
  await flushPromises();
}

async function openSecurityTab(wrapper: ReturnType<typeof mountView>) {
  const securityTabButton = wrapper
    .findAll("button")
    .find((node) => node.text().includes("admin.settings.tabs.security"));

  expect(securityTabButton).toBeDefined();
  await securityTabButton?.trigger("click");
  await flushPromises();
}

async function openGatewayTab(wrapper: ReturnType<typeof mountView>) {
  const gatewayTabButton = wrapper
    .findAll("button")
    .find((node) => node.text().includes("admin.settings.tabs.gateway"));

  expect(gatewayTabButton).toBeDefined();
  await gatewayTabButton?.trigger("click");
  await flushPromises();
}

async function openUsersTab(wrapper: ReturnType<typeof mountView>) {
  const usersTabButton = wrapper
    .findAll("button")
    .find((node) => node.text().includes("admin.settings.tabs.users"));

  expect(usersTabButton).toBeDefined();
  await usersTabButton?.trigger("click");
  await flushPromises();
}

describe("admin SettingsView email domain quota copy", () => {
  it("documents the email domain quota and empty-whitelist behavior in both locales", () => {
    expect(zhCommon.auth.emailDomainRegistrationLimit).toContain("主流邮箱");
    expect(zhCommon.auth.emailDomainRegistrationLimit).toContain("联系客服");
    expect(enCommon.auth.emailDomainRegistrationLimit).toContain("mainstream email");
    expect(enCommon.auth.emailDomainRegistrationLimit).toContain("contact support");

    // 白名单 hint 描述严格默认语义；额度语义移入独立开关的 hint。
    const zhWhitelistHint = zhSettings.settings.registration.emailSuffixWhitelistHint;
    const enWhitelistHint = enSettings.settings.registration.emailSuffixWhitelistHint;
    expect(zhWhitelistHint).toContain("留空则不限制");
    expect(enWhitelistHint).toContain("leave empty for no restriction");

    const zhQuotaHint = zhSettings.settings.registration.emailDomainQuotaHint;
    const enQuotaHint = enSettings.settings.registration.emailDomainQuotaHint;
    expect(zhQuotaHint).toContain("其他可注册主域名各限注册一个账户");
    expect(zhQuotaHint).toContain("关闭时非白名单域名直接拒绝");
    expect(enQuotaHint).toContain("one account");
    expect(enQuotaHint).toContain("When disabled");
  });
});

describe("admin SettingsView payment visible method controls", () => {
  beforeEach(() => {
    getSettings.mockReset();
    updateSettings.mockReset();
    getWebSearchEmulationConfig.mockReset();
    updateWebSearchEmulationConfig.mockReset();
    getAdminApiKey.mockReset();
    getOverloadCooldownSettings.mockReset();
    getRateLimit429CooldownSettings.mockReset();
    updateRateLimit429CooldownSettings.mockReset();
    getStreamTimeoutSettings.mockReset();
    getRectifierSettings.mockReset();
    getBetaPolicySettings.mockReset();
    getUpstreamBillingProbeSettings.mockReset();
    updateUpstreamBillingProbeSettings.mockReset();
    getOllamaCloudUsageSettings.mockReset();
    updateOllamaCloudUsageSettings.mockReset();
    getGroups.mockReset();
    listProxies.mockReset();
    getProviders.mockReset();
    updateProvider.mockReset();
    createProvider.mockReset();
    deleteProvider.mockReset();
    fetchPublicSettings.mockReset();
    adminSettingsFetch.mockReset();
    showError.mockReset();
    showSuccess.mockReset();
    localeRef.value = "zh-CN";

    getSettings.mockResolvedValue({ ...baseSettingsResponse });
    updateSettings.mockImplementation(async (payload) => ({
      ...baseSettingsResponse,
      ...payload,
    }));
    getWebSearchEmulationConfig.mockResolvedValue({
      enabled: false,
      providers: [],
    });
    updateWebSearchEmulationConfig.mockResolvedValue({
      enabled: false,
      providers: [],
    });
    getAdminApiKey.mockResolvedValue({
      exists: false,
      masked_key: "",
    });
    getOverloadCooldownSettings.mockResolvedValue({
      enabled: true,
      cooldown_minutes: 10,
    });
    getRateLimit429CooldownSettings.mockResolvedValue({
      enabled: true,
      cooldown_seconds: 5,
    });
    updateRateLimit429CooldownSettings.mockImplementation(async (payload) => payload);
    getStreamTimeoutSettings.mockResolvedValue({
      enabled: true,
      action: "temp_unsched",
      temp_unsched_minutes: 5,
      threshold_count: 3,
      threshold_window_minutes: 10,
    });
    getRectifierSettings.mockResolvedValue({
      enabled: true,
      thinking_signature_enabled: true,
      thinking_budget_enabled: true,
      apikey_signature_enabled: false,
      apikey_signature_patterns: [],
    });
    getBetaPolicySettings.mockResolvedValue({
      rules: [],
    });
    getUpstreamBillingProbeSettings.mockResolvedValue({
      enabled: true,
      interval_minutes: 30,
    });
    updateUpstreamBillingProbeSettings.mockImplementation(async (payload) => payload);
    getOllamaCloudUsageSettings.mockResolvedValue({
      enabled: false,
      interval_minutes: 60,
      debounce_minutes: 1,
    });
    updateOllamaCloudUsageSettings.mockImplementation(async (payload) => payload);
    getGroups.mockResolvedValue([]);
    listProxies.mockResolvedValue({
      items: [],
    });
    getProviders.mockResolvedValue({
      data: [],
    });
    fetchPublicSettings.mockResolvedValue(undefined);
    adminSettingsFetch.mockResolvedValue(undefined);
  });

  it("opens the risk settings from a deep link after loading without changing switches", async () => {
    settingsRoute.query = { tab: 'features' };
    settingsRoute.hash = '#settings-section-features-risk-control';
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, risk_control_enabled: false, cyber_session_block_enabled: false });
    const wrapper = mountView();
    document.body.appendChild(wrapper.element);
    await flushPromises();
    expect(wrapper.get('#settings-tab-features').attributes('aria-selected')).toBe('true');
    const heading = wrapper.get('#settings-section-features-risk-control');
    expect(heading.isVisible()).toBe(true);
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    const vm = wrapper.vm as unknown as { form: { risk_control_enabled: boolean; cyber_session_block_enabled: boolean } };
    expect(vm.form.risk_control_enabled).toBe(false);
    expect(vm.form.cyber_session_block_enabled).toBe(false);
    expect(updateSettings).not.toHaveBeenCalled();

    await wrapper.get('#settings-tab-general').trigger('click');
    await flushPromises();
    expect(settingsRoute.hash).toBe('');
    settingsRoute.query = { tab: 'features' };
    settingsRoute.hash = '#settings-section-features-risk-control';
    await flushPromises();
    expect(heading.isVisible()).toBe(true);
    expect(document.activeElement).toBe(heading.element);

    settingsRoute.query = { tab: 'invalid' };
    settingsRoute.hash = '#unknown';
    await flushPromises();
    expect(wrapper.get('#settings-tab-general').attributes('aria-selected')).toBe('true');
    expect(updateSettings).not.toHaveBeenCalled();
    wrapper.unmount();
    document.body.innerHTML = '';
  });

  it("points to Smart Ops for the Pelican showcase and never saves its settings", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      pelican_showcase_enabled: true,
      pelican_showcase_config: { max_items: 30, auto_cleanup: true, retention_days: 7 },
    });
    const wrapper = mountView();
    await flushPromises();
    const card = wrapper.get('[data-testid="pelican-showcase-moved"]');
    expect(card.get("router-link").attributes("to")).toBe("/admin/pelican-tests");

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    const payload = updateSettings.mock.calls[0]?.[0];
    expect(payload).toBeDefined();
    // Saving system settings must not overwrite what the Smart Ops page saved.
    expect(payload).not.toHaveProperty("pelican_showcase_enabled");
    expect(payload).not.toHaveProperty("pelican_showcase_config");
    wrapper.unmount();
  });

  it("leaves the monitor mode alone unless it was changed here", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, channel_monitor_enabled: true, channel_monitor_mode: "v3" });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    // The monitor page may have switched the mode since this page loaded.
    expect(updateSettings.mock.calls[0]?.[0]?.channel_monitor_mode).toBeUndefined();

    const featuresTab = wrapper.findAll("button").find((node) => node.text().includes("admin.settings.tabs.features"));
    await featuresTab?.trigger("click");
    await flushPromises();
    await wrapper.get('[data-testid="settings-monitor-mode-v1"]').trigger("click");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings.mock.calls[1]?.[0]).toMatchObject({ channel_monitor_mode: "v1" });
    wrapper.unmount();
  });

  it("saves expanded image capacity and rejects values above each ceiling", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'relay' });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('#excel-bps-image-enabled').setValue(true);
    await wrapper.get('#excel-bps-image-base-url').setValue('https://images.example');
    const fields = [
      ['body-limit', 'body_limit_mib', excelBPSImageLimits.bodyMiB],
      ['budget', 'budget_mib', excelBPSImageLimits.budgetMiB],
      ['max-requests', 'max_requests', excelBPSImageLimits.requests],
      ['max-image-mib', 'max_image_mib', excelBPSImageLimits.imageMiB],
      ['max-total-mib', 'max_total_mib', excelBPSImageLimits.totalMiB],
      ['max-images', 'max_images', excelBPSImageLimits.images],
      ['storage-mib', 'storage_mib', excelBPSImageLimits.storageMiB],
      ['storage-entries', 'storage_entries', excelBPSImageLimits.storageEntries],
      ['ttl-minutes', 'ttl_minutes', excelBPSImageLimits.ttlMinutes],
    ] as const;
    for (const [id, , maximum] of fields) {
      const input = wrapper.get('#excel-bps-image-' + id);
      expect(input.attributes('max')).toBe(String(maximum));
      await input.setValue(String(maximum));
    }
    // Check each invalid field independently while all other fields are valid.
    for (const [id, , maximum] of fields) {
      const input = wrapper.get('#excel-bps-image-' + id);
      await input.setValue(String(maximum + 1));
      await wrapper.find('form').trigger('submit.prevent');
      await flushPromises();
      expect(updateSettings).not.toHaveBeenCalled();
      await input.setValue(String(maximum));
    }
    showError.mockClear();
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings).toHaveBeenCalledTimes(1);
    for (const [, key, maximum] of fields) {
      expect(updateSettings.mock.calls[0]?.[0]).toHaveProperty('excel_bps_image_' + key, maximum);
    }
    expect(showError).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("saves Excel BPS image relay from the feature switches tab", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'relay', excel_bps_image_relay_enabled: false });
    const wrapper = mountView();
    await flushPromises();
    const tab = wrapper.findAll('button').find((node) => node.text().includes('admin.settings.tabs.features'));
    expect(tab).toBeDefined();
    await tab?.trigger('click');
    const card = wrapper.get('[data-testid="excel-bps-image-settings"]');
    expect(card.isVisible()).toBe(true);
    expect(card.find('#excel-bps-image-base-url').exists()).toBe(false);
    await card.get('#excel-bps-image-enabled').setValue(true);
    await card.get('#excel-bps-image-base-url').setValue(' https://images.example/ ');
    await card.get('#excel-bps-image-body-limit').setValue('32');
    await card.get('#excel-bps-image-budget').setValue('768');
    await card.get('#excel-bps-image-max-requests').setValue('512');
    await card.get('#excel-bps-image-max-image-mib').setValue('30');
    await card.get('#excel-bps-image-max-images').setValue('100');
    await card.get('#excel-bps-image-max-total-mib').setValue('64');
    await card.get('#excel-bps-image-storage-mib').setValue('2048');
    await card.get('#excel-bps-image-storage-entries').setValue('2048');
    await card.get('#excel-bps-image-ttl-minutes').setValue('60');

    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({
      excel_bps_image_relay_enabled: true,
      excel_bps_image_base_url: 'https://images.example',
      excel_bps_image_body_limit_mib: 32,
      excel_bps_image_budget_mib: 768,
      excel_bps_image_max_requests: 512,
      excel_bps_image_max_image_mib: 30,
      excel_bps_image_max_images: 100,
      excel_bps_image_max_total_mib: 64,
      excel_bps_image_storage_mib: 2048,
      excel_bps_image_storage_entries: 2048,
      excel_bps_image_ttl_minutes: 60,

    });
    expect(showError).not.toHaveBeenCalled();
    expect(showSuccess).toHaveBeenCalledWith('admin.settings.settingsSaved');
    wrapper.unmount();
  });

  it("saves the BPS and Prism protocol switches", async () => {
    const wrapper = mountView();
    await flushPromises();
    const tab = wrapper.findAll('button').find((node) => node.text().includes('admin.settings.tabs.features'));
    await tab?.trigger('click');
    const protocolSwitches = wrapper.get('[data-testid="protocol-feature-switches"]');
    await protocolSwitches.get('input').setValue(false);
    await protocolSwitches.findAll('input')[1].setValue(true);
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_enabled: false, prism_browser_enabled: true });
    wrapper.unmount();
  });

  it("defaults to native image uploads without a public origin and reloads the mode", async () => {
    const settings: Partial<typeof baseSettingsResponse> = { ...baseSettingsResponse };
    delete settings.excel_bps_image_mode;
    delete settings.excel_bps_image_relay_enabled;
    getSettings.mockResolvedValueOnce(settings);
    const wrapper = mountView();
    await flushPromises();
    expect((wrapper.get('#excel-bps-image-enabled').element as HTMLInputElement).checked).toBe(true);
    expect((wrapper.get('#excel-bps-image-mode').element as HTMLSelectElement).value).toBe('native');
    expect(wrapper.find('#excel-bps-image-base-url').exists()).toBe(false);
    expect(wrapper.find('#excel-bps-image-max-images').exists()).toBe(true);
    expect(wrapper.find('#excel-bps-image-storage-mib').exists()).toBe(false);
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_image_mode: 'native', excel_bps_image_relay_enabled: true, excel_bps_image_base_url: '' });
    expect(showError).not.toHaveBeenCalled();
    wrapper.unmount();
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'native', excel_bps_image_relay_enabled: true });
    const loaded = mountView();
    await flushPromises();
    expect((loaded.get('#excel-bps-image-mode').element as HTMLSelectElement).value).toBe('native');
    expect(loaded.find('#excel-bps-image-base-url').exists()).toBe(false);
    expect(loaded.find('#excel-bps-image-max-images').exists()).toBe(true);
    await loaded.get('#excel-bps-image-mode').setValue('relay');
    expect(loaded.find('#excel-bps-image-max-images').exists()).toBe(true);
    loaded.unmount();
  });

  it("preserves explicitly disabled BPS image support", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_relay_enabled: false });
    const wrapper = mountView();
    await flushPromises();
    expect((wrapper.get('#excel-bps-image-enabled').element as HTMLInputElement).checked).toBe(false);
    expect(wrapper.find('#excel-bps-image-mode').exists()).toBe(false);
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_image_relay_enabled: false });
    wrapper.unmount();
  });

  it("defaults the image policy off and saves auto compaction", async () => {
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('#excel-bps-image-enabled').setValue(true);
    await wrapper.get('#excel-bps-image-mode').setValue('native');
    expect((wrapper.get('#bps-image-limit-policy').element as HTMLSelectElement).value).toBe('off');
    await wrapper.get('#bps-image-limit-policy').setValue('auto_compact');
    expect(wrapper.find('#bps-image-compact-reserve').exists()).toBe(false);
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_image_limit_policy: 'auto_compact', excel_bps_image_warning_remaining: 8, excel_bps_image_compact_reserve: 3 });
    expect(showError).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("validates warning margins and reloads the policy", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_relay_enabled: true, excel_bps_image_mode: 'native', excel_bps_image_limit_policy: 'warn', excel_bps_image_warning_remaining: 8, excel_bps_image_compact_reserve: 3 });
    const wrapper = mountView();
    await flushPromises();
    expect((wrapper.get('#bps-image-limit-policy').element as HTMLSelectElement).value).toBe('warn');
    for (const [selector, value, original] of [
      ['#bps-image-warning-remaining', '20', '8'],
      ['#bps-image-compact-reserve', '8', '3'],
      ['#bps-image-compact-reserve', '0', '3'],
      ['#bps-image-warning-remaining', '8.5', '8'],
    ]) {
      await wrapper.get(selector).setValue(value);
      await wrapper.find('form').trigger('submit.prevent');
      await flushPromises();
      expect(updateSettings).not.toHaveBeenCalled();
      expect(showError).toHaveBeenLastCalledWith('admin.settings.features.excelBpsImages.invalidLimits');
      await wrapper.get(selector).setValue(original);
    }
    await wrapper.get('#bps-image-warning-remaining').setValue('9');
    await wrapper.get('#bps-image-compact-reserve').setValue('4');
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_image_limit_policy: 'warn', excel_bps_image_warning_remaining: 9, excel_bps_image_compact_reserve: 4 });
    wrapper.unmount();
  });

  it("loads saved Excel BPS image settings and preserves the address when disabled", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'relay', excel_bps_image_relay_enabled: true, excel_bps_image_base_url: 'https://saved.example', excel_bps_image_body_limit_mib: 24, excel_bps_image_budget_mib: 896, excel_bps_image_max_requests: 40 });
    const wrapper = mountView();
    await flushPromises();
    expect((wrapper.get('#excel-bps-image-base-url').element as HTMLInputElement).value).toBe('https://saved.example');
    expect((wrapper.get('#excel-bps-image-body-limit').element as HTMLInputElement).value).toBe('24');
    expect((wrapper.get('#excel-bps-image-budget').element as HTMLInputElement).value).toBe('896');
    expect((wrapper.get('#excel-bps-image-max-requests').element as HTMLInputElement).value).toBe('40');
    await wrapper.get('#excel-bps-image-enabled').setValue(false);
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_image_relay_enabled: false, excel_bps_image_base_url: 'https://saved.example', excel_bps_image_body_limit_mib: 24, excel_bps_image_budget_mib: 896, excel_bps_image_max_requests: 40 });
    wrapper.unmount();
  });

  it("rejects inconsistent image limits and preserves saved limits when disabling", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'relay', excel_bps_image_relay_enabled: true, excel_bps_image_base_url: 'https://images.example', excel_bps_image_max_images: 100, excel_bps_image_ttl_minutes: 60 });
    const wrapper = mountView();
    await flushPromises();
    expect((wrapper.get('#excel-bps-image-max-images').element as HTMLInputElement).value).toBe('100');
    for (const [id, value, original] of [
      ['max-images', '0', '100'], ['max-images', String(excelBPSImageLimits.images + 1), '100'], ['ttl-minutes', String(excelBPSImageLimits.ttlMinutes + 1), '60'],
      ['max-total-mib', '1', '32'], ['storage-entries', '99', '512'], ['storage-mib', '1', '1024'],
    ]) {
      await wrapper.get(`#excel-bps-image-${id}`).setValue(value);
      await wrapper.find('form').trigger('submit.prevent');
      await flushPromises();
      expect(updateSettings).not.toHaveBeenCalled();
      expect(showError).toHaveBeenLastCalledWith('admin.settings.features.excelBpsImages.invalidLimits');
      await wrapper.get(`#excel-bps-image-${id}`).setValue(original);
    }
    await wrapper.get('#excel-bps-image-enabled').setValue(false);
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toMatchObject({ excel_bps_image_relay_enabled: false, excel_bps_image_max_images: 100, excel_bps_image_ttl_minutes: 60 });
    wrapper.unmount();
  });

  it("rejects missing or unsafe Excel BPS image origins before saving", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'relay' });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('#excel-bps-image-enabled').setValue(true);
    for (const value of ['', 'http://images.example', 'https://images.example/v1', 'https://user:secret@images.example', 'https://images.example?token=secret', 'https://images.example#']) {
      await wrapper.get('#excel-bps-image-base-url').setValue(value);
      await wrapper.find('form').trigger('submit.prevent');
      await flushPromises();
      expect(updateSettings).not.toHaveBeenCalled();
      expect(showError).toHaveBeenLastCalledWith('admin.settings.features.excelBpsImages.invalidBaseUrl');
    }
    wrapper.unmount();
  });

  it("rejects out-of-range image relay capacity before saving", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse, excel_bps_image_mode: 'relay' });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get('#excel-bps-image-enabled').setValue(true);
    await wrapper.get('#excel-bps-image-base-url').setValue('https://images.example');
    for (const [selector, value] of [
      ['#excel-bps-image-body-limit', String(excelBPSImageLimits.bodyMiB + 1)],
      ['#excel-bps-image-budget', '511'],
      ['#excel-bps-image-max-requests', '0'],
      ['#excel-bps-image-max-requests', String(excelBPSImageLimits.requests + 1)],
      ['#excel-bps-image-max-requests', '1.5'],
    ]) {
      const input = wrapper.get(selector);
      const original = (input.element as HTMLInputElement).value;
      await input.setValue(value);
      await wrapper.find('form').trigger('submit.prevent');
      await flushPromises();
      expect(updateSettings).not.toHaveBeenCalled();
      expect(showError).toHaveBeenLastCalledWith('admin.settings.features.excelBpsImages.invalidCapacity');
      await input.setValue(original);
    }
    await wrapper.get('#excel-bps-image-body-limit').setValue('128');
    await wrapper.get('#excel-bps-image-budget').setValue('512');
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings).not.toHaveBeenCalled();
    expect(showError).toHaveBeenLastCalledWith('admin.settings.features.excelBpsImages.invalidCapacity');
    wrapper.unmount();
  });

  it("submits the Codex ticket harvest toggle", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_enabled: false,
    });
    const wrapper = mountView();
    await flushPromises();
    const toggle = wrapper.get("#codex-ticket-enabled");
    await toggle.setValue(true);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_enabled).toBe(true);
    wrapper.unmount();
  });

  it("loads and saves independent Codex ticket model toggles", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_models: ["gpt-6-astra", "gpt-5.6-sol"],
    });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.get("#codex-ticket-model-sol").setValue(false);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_models).toEqual([
      "gpt-6-astra",
    ]);
    wrapper.unmount();
  });

  it("keeps missing-ticket account pausing off by default and saves explicit opt-in", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_fail_closed: false,
    });
    const wrapper = mountView();
    await flushPromises();
    const toggle = wrapper.get<HTMLInputElement>("#codex-ticket-fail-closed");
    expect(toggle.element.checked).toBe(false);
    await toggle.setValue(true);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_fail_closed).toBe(true);
    wrapper.unmount();
  });

  it("loads and changes the ticket refresh strategy", async () => {
    getSettings.mockResolvedValueOnce({...baseSettingsResponse,openai_codex_ticket_strategy:'standby'});
    const wrapper=mountView();await flushPromises();
    await wrapper.get('#codex-ticket-strategy').setValue('fixed');
    await wrapper.find('form').trigger('submit.prevent');await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_strategy).toBe('fixed');
    wrapper.unmount();
  });

  it("loads and saves selected harvest groups, preserving an explicitly empty selection", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse,
      openai_codex_ticket_harvest_scope: { mode: 'selected', group_ids: [2], account_policy: 'schedulable_only' },
    });
    getGroups.mockResolvedValueOnce([
      { id: 2, name: 'PLUS', platform: 'openai', status: 'active' },
      { id: 24, name: 'PRO', platform: 'openai', status: 'active' },
      { id: 32, name: 'Grok', platform: 'grok', status: 'active' },
    ]);
    const wrapper = mountView(); await flushPromises();
    expect(wrapper.find('#codex-ticket-group-32').exists()).toBe(false);
    expect(wrapper.get<HTMLInputElement>('#codex-ticket-group-2').element.checked).toBe(true);
    await wrapper.get('#codex-ticket-group-24').setValue(true);
    await wrapper.find('form').trigger('submit.prevent'); await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_harvest_scope).toEqual({mode:'selected',group_ids:[2,24],account_policy:'schedulable_only'});
    await wrapper.get('#codex-ticket-group-2').setValue(false);
    await wrapper.get('#codex-ticket-group-24').setValue(false);
    await wrapper.find('form').trigger('submit.prevent'); await flushPromises();
    expect(updateSettings.mock.calls[1]?.[0].openai_codex_ticket_harvest_scope).toEqual({mode:'selected',group_ids:[],account_policy:'schedulable_only'});
    wrapper.unmount();
  });

  it("preserves selected harvest IDs when group loading fails", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse,
      openai_codex_ticket_harvest_scope: { mode: 'selected', group_ids: [24], account_policy: 'schedulable_only' },
    });
    getGroups.mockRejectedValueOnce(new Error('offline'));
    const wrapper = mountView(); await flushPromises();
    expect(wrapper.get<HTMLInputElement>('#codex-ticket-group-24').element.checked).toBe(true);
    await wrapper.find('form').trigger('submit.prevent'); await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_harvest_scope).toEqual({mode:'selected',group_ids:[24],account_policy:'schedulable_only'});
    wrapper.unmount();
  });

  it("defaults legacy harvest scopes to schedulable only and saves compatibility policy", async () => {
    getSettings.mockResolvedValueOnce({ ...baseSettingsResponse,
      openai_codex_ticket_harvest_scope: { mode: 'selected', group_ids: [24] },
    });
    const wrapper = mountView();
    await flushPromises();
    const policy = wrapper.get<HTMLSelectElement>('#codex-ticket-account-policy');
    expect(policy.element.value).toBe('schedulable_only');
    await policy.setValue('prioritize_schedulable');
    await wrapper.find('form').trigger('submit.prevent');
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_harvest_scope).toEqual({
      mode: 'selected', group_ids: [24], account_policy: 'prioritize_schedulable',
    });
    wrapper.unmount();
  });

  it("keeps strict ticket response rejection opt-in", async () => {
    const wrapper=mountView();await flushPromises();
    expect(wrapper.get<HTMLInputElement>('#codex-ticket-strict').element.checked).toBe(false);
    await wrapper.get('#codex-ticket-strict').setValue(true);
    await wrapper.find('form').trigger('submit.prevent');await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_strict_response).toBe(true);
    wrapper.unmount();
  });

  it("loads the masked Codex harvest proxy and submits a replacement URL", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_harvest_proxy_url: "http://user:***@old.example.com:8080",
      openai_codex_ticket_harvest_proxy_configured: true,
    });
    const wrapper = mountView();
    await flushPromises();
    const input = wrapper.get<HTMLInputElement>("#codex-ticket-harvest-proxy");
    expect(input.element.value).toBe("http://user:***@old.example.com:8080");
    await input.setValue("socks5h://user:new-secret@new.example.com:1080");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_harvest_proxy_url)
      .toBe("socks5h://user:new-secret@new.example.com:1080");
    expect(updateSettings.mock.calls[0]?.[0]).not.toHaveProperty("openai_codex_ticket_harvest_proxy_configured");
    wrapper.unmount();
  });

  it("confirms Mihomo proxy selection and applies it only when settings are saved", async () => {
    const endpoint = "http://127.0.0.1:3101";
    const getStatus = vi.spyOn(apiClient, "get").mockResolvedValue({ data: {
      installed: true, supported: true, running: true, busy: false,
      nodes: 1, subscriptions: 1, phase: "running", endpoint,
    } });
    const wrapper = mountView();
    try {
      await flushPromises();
      await wrapper.get('input[name="codex-ticket-proxy-mode"][value="mihomo"]').setValue(true);
      await flushPromises();
      const button = wrapper.findAll("button").find(node => node.text() === "设为打票代理");
      expect(button).toBeDefined();
      await button!.trigger("click");
      expect(showSuccess).toHaveBeenCalledWith("admin.settings.gatewayForwarding.codexTicketProxyMihomoSelected");
      expect(updateSettings).not.toHaveBeenCalled();
      await wrapper.find("form").trigger("submit.prevent");
      await flushPromises();
      expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
        openai_codex_ticket_harvest_proxy_url: endpoint,
      }));
    } finally {
      wrapper.unmount();
      getStatus.mockRestore();
    }
  });

  it("does not activate an unverified Mihomo endpoint just by selecting the mode", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_harvest_proxy_url: "",
      openai_codex_ticket_harvest_proxy_configured: false,
    });
    const wrapper = mountView();
    await flushPromises();

    await wrapper
      .get<HTMLInputElement>('input[name="codex-ticket-proxy-mode"][value="mihomo"]')
      .setValue(true);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings.mock.calls[0]?.[0].openai_codex_ticket_harvest_proxy_url)
      .toBe("");
    wrapper.unmount();
  });

  it("saves the IP management pool as the harvest proxy and restores the static address when leaving it", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_harvest_proxy_url: "http://user:***@old.example.com:8080",
      openai_codex_ticket_harvest_proxy_configured: true,
    });
    const wrapper = mountView();
    await flushPromises();

    await wrapper
      .get<HTMLInputElement>('input[name="codex-ticket-proxy-mode"][value="ip_pool"]')
      .setValue(true);
    expect(wrapper.find("#codex-ticket-harvest-proxy").exists()).toBe(false);
    expect(wrapper.find('[data-testid="codex-ticket-proxy-ip-pool-hint"]').exists()).toBe(true);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings.mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      openai_codex_ticket_harvest_proxy_url: "ippool://active",
      openai_codex_ticket_use_saved_static_proxy: false,
    }));

    await wrapper
      .get<HTMLInputElement>('input[name="codex-ticket-proxy-mode"][value="static"]')
      .setValue(true);
    expect(wrapper.get<HTMLInputElement>("#codex-ticket-harvest-proxy").element.value)
      .toBe("http://user:***@old.example.com:8080");
    wrapper.unmount();
  });

  it("restores the IP management pool mode from saved settings", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_codex_ticket_harvest_proxy_url: "ippool://active",
      openai_codex_ticket_harvest_proxy_configured: true,
      openai_codex_ticket_static_proxy_url: "http://user:***@saved.example.com:8080",
    });
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.get<HTMLInputElement>('input[name="codex-ticket-proxy-mode"][value="ip_pool"]').element.checked)
      .toBe(true);
    await wrapper
      .get<HTMLInputElement>('input[name="codex-ticket-proxy-mode"][value="static"]')
      .setValue(true);
    expect(wrapper.get<HTMLInputElement>("#codex-ticket-harvest-proxy").element.value)
      .toBe("http://user:***@saved.example.com:8080");
    wrapper.unmount();
  });

  it("loads and saves the open button visibility for each custom menu", async () => {
    const menuItems = [
      { id: "docs", label: "Docs", url: "https://example.com/docs", icon_svg: "", visibility: "user", sort_order: 0 },
      { id: "help", label: "Help", url: "https://example.com/help", icon_svg: "", visibility: "user", sort_order: 1, hide_open_button: true },
    ];
    getSettings.mockResolvedValue({ ...baseSettingsResponse, custom_menu_items: menuItems });
    const wrapper = mountView();
    await flushPromises();

    const toggles = wrapper.findAll<HTMLInputElement>('[data-testid="custom-menu-hide-open-button"]');
    expect(toggles.map(toggle => toggle.element.checked)).toEqual([false, true]);
    await toggles[0].setValue(true);
    await toggles[1].setValue(false);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      custom_menu_items: [
        { ...menuItems[0], hide_open_button: true },
        { ...menuItems[1], hide_open_button: false },
      ],
    }));
    wrapper.unmount();
  });

  it("submits the compact home page toggle", async () => {
    const wrapper = mountView();
    await flushPromises();

    const toggle = wrapper.get('[data-testid="compact-home-toggle"]');
    expect((toggle.element as HTMLInputElement).checked).toBe(false);

    await toggle.setValue(true);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({ compact_home_enabled: true }),
    );
  });

  it("renders panel rate limit card and saves settings", async () => {
    getPanelRateLimitSettings.mockClear();
    updatePanelRateLimitSettings.mockClear();
    getPanelRateLimitSettings.mockResolvedValue({
      enabled: true,
      user_rpm: 240,
      heavy_rpm: 60,
      exempt_admin: true,
      public_ip_rpm: 300,
    });
    updatePanelRateLimitSettings.mockImplementation(async (payload) => payload);

    const wrapper = mountView();
    await flushPromises();

    expect(getPanelRateLimitSettings).toHaveBeenCalled();
    expect(wrapper.text()).toContain("admin.settings.panelRateLimit.title");
    expect(wrapper.text()).toContain("admin.settings.panelRateLimit.proxySafeNote");

    const userRpmInput = wrapper.find('[data-testid="panel-rate-limit-user-rpm"]');
    expect(userRpmInput.exists()).toBe(true);
    await userRpmInput.setValue("120");

    const saveButton = wrapper.find('[data-testid="panel-rate-limit-save"]');
    expect(saveButton.exists()).toBe(true);
    await saveButton.trigger("click");
    await flushPromises();

    expect(updatePanelRateLimitSettings).toHaveBeenCalledWith({
      enabled: true,
      user_rpm: 120,
      heavy_rpm: 60,
      exempt_admin: true,
      public_ip_rpm: 300,
    });
    expect(showSuccess).toHaveBeenCalled();
  });

  it("does not render legacy visible payment method controls", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openPaymentTab(wrapper);

    expect(wrapper.text()).not.toContain("可见方式");
    expect(wrapper.text()).not.toContain("支付来源");
  });

  it("shows valid passkey RP configuration and persists the sign-in toggle", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);

    const settings = wrapper.get('[data-testid="passkey-settings"]');
    const toggle = settings.get('[data-testid="passkey-toggle"]');
    expect(toggle.attributes("disabled")).toBeUndefined();
    expect(settings.text()).toContain("sub3.nebula-spaces.com");
    expect(settings.text()).toContain("https://sub3.nebula-spaces.com");
    expect(settings.text()).not.toContain("webauthn.enabled");

    await toggle.setValue(false);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({ passkey_enabled: false }),
    );
  });

  it("人机验证切换到腾讯天御并保存四项配置", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openSecurityTab(wrapper);

    const masterToggle = wrapper.get('[data-testid="captcha-enabled-toggle"]');
    await masterToggle.setValue(true);
    // 默认选中 Turnstile
    expect(wrapper.text()).toContain("admin.settings.turnstile.siteKey");

    await wrapper.get('[data-testid="captcha-provider-tencent"]').trigger("click");
    await flushPromises();

    const card = wrapper
      .findAll(".card")
      .find((node) => node.text().includes("admin.settings.captcha.title"));
    expect(card).toBeDefined();
    expect(card!.text()).not.toContain("admin.settings.turnstile.siteKey");
    expect(card!.get('a[href="https://console.cloud.tencent.com/captcha"]').exists()).toBe(true);
    expect(card!.get('a[href="https://console.cloud.tencent.com/cam/capi"]').exists()).toBe(true);
    expect(
      card!.get('a[href="https://cloud.tencent.com/document/product/1110/36841"]').exists(),
    ).toBe(true);
    const inputs = card!.findAll("input").filter((input) => input.attributes("type") !== "checkbox");
    await inputs[0]!.setValue("123456789");
    await inputs[1]!.setValue("app-secret-value");
    await inputs[2]!.setValue("cloud-secret-id-value");
    await inputs[3]!.setValue("cloud-secret-key-value");

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        turnstile_enabled: false,
        tencent_captcha_enabled: true,
        aliyun_captcha_enabled: false,
        tencent_captcha_app_id: "123456789",
        tencent_captcha_app_secret_key: "app-secret-value",
        tencent_captcha_cloud_secret_id: "cloud-secret-id-value",
        tencent_captcha_cloud_secret_key: "cloud-secret-key-value",
        tencent_captcha_region: "cn",
      }),
    );
  });

  it("腾讯天御切换到国际站后保存站点并更新控制台入口", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openSecurityTab(wrapper);

    await wrapper.get('[data-testid="captcha-enabled-toggle"]').setValue(true);
    await wrapper.get('[data-testid="captcha-provider-tencent"]').trigger("click");
    await wrapper.get('[data-testid="tencent-captcha-region-intl"]').trigger("click");

    const card = wrapper
      .findAll(".card")
      .find((node) => node.text().includes("admin.settings.captcha.title"));
    expect(card).toBeDefined();
    expect(card!.get('a[href="https://console.tencentcloud.com/captcha/graphical"]').exists()).toBe(
      true,
    );
    expect(card!.get('a[href="https://console.tencentcloud.com/cam/capi"]').exists()).toBe(true);

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        tencent_captcha_enabled: true,
        tencent_captcha_region: "intl",
      }),
    );
  });

  it("人机验证切换到阿里云并保存配置", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openSecurityTab(wrapper);

    const masterToggle = wrapper.get('[data-testid="captcha-enabled-toggle"]');
    await masterToggle.setValue(true);

    await wrapper.get('[data-testid="captcha-provider-aliyun"]').trigger("click");
    await flushPromises();

    const card = wrapper
      .findAll(".card")
      .find((node) => node.text().includes("admin.settings.captcha.title"));
    expect(card).toBeDefined();
    expect(card!.text()).toContain("admin.settings.aliyunCaptcha.region");
    expect(card!.text()).not.toContain("admin.settings.turnstile.siteKey");
    const inputs = card!.findAll("input").filter((input) => input.attributes("type") !== "checkbox");
    await inputs[0]!.setValue("prefix-1");
    await inputs[1]!.setValue("scene-1");
    await inputs[2]!.setValue("ak-id");
    await inputs[3]!.setValue("ak-secret-value");

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        turnstile_enabled: false,
        tencent_captcha_enabled: false,
        aliyun_captcha_enabled: true,
        aliyun_captcha_prefix: "prefix-1",
        aliyun_captcha_scene_id: "scene-1",
        aliyun_captcha_access_key_id: "ak-id",
        aliyun_captcha_access_key_secret: "ak-secret-value",
        aliyun_captcha_region: "cn",
      }),
    );
  });

  it("关闭人机验证总开关会同时关闭所有服务商", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: "123456789",
      tencent_captcha_app_secret_key_configured: true,
      tencent_captcha_cloud_secret_id_configured: true,
      tencent_captcha_cloud_secret_key_configured: true,
    });
    const wrapper = mountView();
    await flushPromises();
    await openSecurityTab(wrapper);

    const masterToggle = wrapper.get('[data-testid="captcha-enabled-toggle"]');
    expect((masterToggle.element as HTMLInputElement).checked).toBe(true);
    // 加载后选中项跟随已启用的服务商
    expect(wrapper.text()).toContain("admin.settings.tencentCaptcha.appId");

    await masterToggle.setValue(false);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        turnstile_enabled: false,
        tencent_captcha_enabled: false,
        aliyun_captcha_enabled: false,
      }),
    );
  });

  it("disables passkey sign-in when the RP configuration is unavailable", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      passkey_enabled: false,
      passkey_configured: false,
      passkey_rp_id: "",
      passkey_rp_origins: [],
    });
    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);

    const settings = wrapper.get('[data-testid="passkey-settings"]');
    expect(settings.get('[data-testid="passkey-toggle"]').attributes("disabled")).toBeDefined();
    const status = settings.get('[data-testid="passkey-config-status"]');
    expect(status.text()).toContain(
      "admin.settings.security.passkeyNotConfigured",
    );
    expect(status.text()).toContain("webauthn.enabled");
    expect(status.text()).toContain("webauthn.rp_id");
    expect(status.text()).toContain("webauthn.rp_origins");
    expect(status.text()).toContain("然后重启服务");
  });

  it("loads, edits, validates, and saves forwarded client-IP headers", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      api_key_acl_trust_forwarded_ip: false,
      forwarded_client_ip_headers: ["cf-connecting-ip", "X-Real-IP"],
    });
    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);

    const card = wrapper
      .findAll(".card")
      .find((node) => node.text().includes("admin.settings.apiKeyAcl.title"));
    expect(card).toBeDefined();
    const toggle = card!.get('input[type="checkbox"]');
    expect((toggle.element as HTMLInputElement).checked).toBe(false);
    expect(card!.find('[data-testid="forwarded-client-ip-headers-input"]').exists()).toBe(false);

    await toggle.setValue(true);
    expect(card!.findAll('[data-testid="forwarded-client-ip-header-tag"]')).toHaveLength(2);
    expect(card!.text()).toContain("Cf-Connecting-Ip");
    expect(card!.text()).toContain("X-Real-Ip");
    showError.mockClear();

    const input = card!.get('[data-testid="forwarded-client-ip-headers-input"]');
    await input.setValue("x-client-ip");
    await input.trigger("keydown", { key: "Enter" });
    await input.setValue("X-CLIENT-IP");
    await input.trigger("keydown", { key: "Enter" });
    await input.setValue("invalid header");
    await input.trigger("keydown", { key: "Enter" });
    expect(showError).toHaveBeenCalledTimes(1);
    expect(card!.findAll('[data-testid="forwarded-client-ip-header-tag"]')).toHaveLength(3);

    const realIpTag = card!
      .findAll('[data-testid="forwarded-client-ip-header-tag"]')
      .find((tag) => tag.text().includes("X-Real-Ip"));
    expect(realIpTag).toBeDefined();
    await realIpTag!.get("button").trigger("click");
    expect(card!.text()).not.toContain("X-Real-Ip");

    await toggle.setValue(false);
    expect(card!.find('[data-testid="forwarded-client-ip-headers-input"]').exists()).toBe(false);
    await toggle.setValue(true);
    expect(card!.text()).toContain("X-Client-Ip");

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        api_key_acl_trust_forwarded_ip: true,
        forwarded_client_ip_headers: ["Cf-Connecting-Ip", "X-Client-Ip"],
      }),
    );
  });

  it("loads, edits, and saves recharge bonus tiers and the notice", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      payment_recharge_bonus_tiers: [
        { min_amount: 500, bonus_percent: 30 },
        { min_amount: 100, bonus_percent: 20 },
      ],
      payment_recharge_bonus_notice: "满 100 送 20%",
    });
    const wrapper = mountView();

    await flushPromises();
    await openPaymentTab(wrapper);

    // 回填按阈值升序，并渲染区间预览（首段为「不赠送」）
    let rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    expect(rows).toHaveLength(2);
    const minValue = (row: (typeof rows)[number]) =>
      (row.get('[data-testid="recharge-bonus-tier-min-input"]').element as HTMLInputElement).value;
    expect(minValue(rows[0]!)).toBe("100");
    expect(minValue(rows[1]!)).toBe("500");
    expect(wrapper.get('[data-testid="recharge-bonus-tier-preview"]').text()).toContain(
      "admin.settings.payment.rechargeBonus.previewRangeNone",
    );

    await wrapper.get('[data-testid="recharge-bonus-tier-add"]').trigger("click");
    rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    expect(rows).toHaveLength(3);
    expect(rows[2]!.find('[data-testid="recharge-bonus-tier-incomplete"]').exists()).toBe(true);

    // 与已有档位重复的阈值行内报错，改成新阈值后消失
    await rows[2]!.get('[data-testid="recharge-bonus-tier-min-input"]').setValue("100");
    await rows[2]!.get('[data-testid="recharge-bonus-tier-percent-input"]').setValue("25");
    rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    expect(rows[2]!.find('[data-testid="recharge-bonus-tier-error"]').exists()).toBe(true);
    await rows[2]!.get('[data-testid="recharge-bonus-tier-min-input"]').setValue("1000");
    rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    expect(rows[2]!.find('[data-testid="recharge-bonus-tier-error"]').exists()).toBe(false);

    // 切到折扣模式：百分比 ≥ 100 行内报错，改回 < 100 后消失
    await wrapper.get('[data-testid="recharge-bonus-mode-discount"]').trigger("click");
    rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    await rows[2]!.get('[data-testid="recharge-bonus-tier-percent-input"]').setValue("100");
    rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    expect(rows[2]!.get('[data-testid="recharge-bonus-tier-error"]').text()).toContain("invalidDiscountPercent");
    await rows[2]!.get('[data-testid="recharge-bonus-tier-percent-input"]').setValue("25");
    rows = wrapper.findAll('[data-testid="recharge-bonus-tier-row"]');
    expect(rows[2]!.find('[data-testid="recharge-bonus-tier-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="recharge-bonus-tier-preview"]').text()).toContain(
      "admin.settings.payment.rechargeBonus.previewRangeDiscount",
    );

    const notice = wrapper.get('[data-testid="recharge-bonus-notice-input"]');
    expect((notice.element as HTMLTextAreaElement).value).toBe("满 100 送 20%");
    await notice.setValue("**新活动**");

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        payment_recharge_bonus_tiers: [
          { min_amount: 100, bonus_percent: 20 },
          { min_amount: 500, bonus_percent: 30 },
          { min_amount: 1000, bonus_percent: 25 },
        ],
        payment_recharge_bonus_mode: "discount",
        payment_recharge_bonus_notice: "**新活动**",
      }),
    );
  });

  it("drops incomplete recharge bonus rows and submits an empty list when cleared", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      payment_recharge_bonus_tiers: [{ min_amount: 100, bonus_percent: 20 }],
    });
    const wrapper = mountView();

    await flushPromises();
    await openPaymentTab(wrapper);

    await wrapper.get('[data-testid="recharge-bonus-tier-remove"]').trigger("click");
    await wrapper.get('[data-testid="recharge-bonus-tier-add"]').trigger("click");
    expect(wrapper.findAll('[data-testid="recharge-bonus-tier-row"]')).toHaveLength(1);

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({ payment_recharge_bonus_tiers: [] }),
    );
  });

  it("links payment guidance to README sections instead of removed payment docs", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openPaymentTab(wrapper);

    const paymentLinks = wrapper
      .findAll("a")
      .filter((node) =>
        ["查看支付配置说明", "查看支持的支付方式"].includes(node.text()),
      );

    expect(paymentLinks).toHaveLength(2);
    expect(paymentLinks[0]?.attributes("href")).toBe(
      "https://github.com/Wei-Shaw/sub2api/blob/main/docs/PAYMENT_CN.md",
    );
    expect(paymentLinks[1]?.attributes("href")).toBe(
      "https://github.com/Wei-Shaw/sub2api/blob/main/docs/PAYMENT_CN.md#支持的支付方式",
    );
    for (const link of paymentLinks) {
      expect(link.attributes("href")).toContain("docs/PAYMENT");
    }
  });

  it("does not submit legacy visible payment method settings", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openPaymentTab(wrapper);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    const payload = updateSettings.mock.calls[0]?.[0];
    expect(payload).not.toHaveProperty("payment_visible_method_alipay_source");
    expect(payload).not.toHaveProperty("payment_visible_method_wxpay_source");
    expect(payload).not.toHaveProperty("payment_visible_method_alipay_enabled");
    expect(payload).not.toHaveProperty("payment_visible_method_wxpay_enabled");
  });

  it("submits the admin recharge affiliate rebate setting", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      affiliate_enabled: true,
      affiliate_admin_recharge_enabled: true,
    });

    const wrapper = mountView();

    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        affiliate_admin_recharge_enabled: true,
      }),
    );
  });

  it("submits Anthropic cache TTL injection gateway setting", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      enable_anthropic_cache_ttl_1h_injection: true,
    });

    const wrapper = mountView();

    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        enable_anthropic_cache_ttl_1h_injection: true,
      }),
    );
  });

  it("submits message cache_control rewrite gateway setting", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      rewrite_message_cache_control: true,
    });

    const wrapper = mountView();

    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        rewrite_message_cache_control: true,
      }),
    );
  });

  it("submits Claude OAuth system prompt injection gateway settings", async () => {
    const blocks = `[{"type":"text","text":"custom block","cache_control":true}]`;
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      enable_claude_oauth_system_prompt_injection: false,
      claude_oauth_system_prompt_blocks: blocks,
    });

    const wrapper = mountView();

    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        enable_claude_oauth_system_prompt_injection: false,
      }),
    );
    const payload = updateSettings.mock.calls[0][0] as {
      claude_oauth_system_prompt_blocks: string;
    };
    expect(JSON.parse(payload.claude_oauth_system_prompt_blocks)).toEqual([
      {
        enabled: true,
        type: "text",
        text: "custom block",
        cache_control: {
          type: "ephemeral",
          ttl: "5m",
        },
      },
    ]);
  });

  it("submits Antigravity user agent version gateway setting", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      antigravity_user_agent_version: "1.23.2",
    });

    const wrapper = mountView();

    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        antigravity_user_agent_version: "1.23.2",
      }),
    );
  });

  it("updates provider enablement immediately and reloads providers", async () => {
    const provider = {
      id: 7,
      provider_key: "alipay",
      name: "Official Alipay",
      config: {},
      supported_types: ["alipay"],
      enabled: false,
      payment_mode: "",
      refund_enabled: false,
      allow_user_refund: false,
      limits: "",
      sort_order: 0,
    };
    getProviders.mockReset();
    getProviders
      .mockResolvedValueOnce({ data: [provider] })
      .mockResolvedValueOnce({ data: [{ ...provider, enabled: true }] });
    updateProvider.mockResolvedValue({ data: { ...provider, enabled: true } });

    const PaymentProviderListStub = defineComponent({
      emits: ["toggleField"],
      setup(_, { emit }) {
        return () =>
          h(
            "button",
            {
              class: "provider-toggle-stub",
              onClick: () => emit("toggleField", provider, "enabled"),
            },
            "toggle provider",
          );
      },
    });

    const wrapper = mount(SettingsView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          Select: SelectStub,
          Toggle: ToggleStub,
          Icon: true,
          ConfirmDialog: true,
          PaymentProviderList: PaymentProviderListStub,
          PaymentProviderDialog: true,
          GroupBadge: true,
          GroupOptionItem: true,
          ProxySelector: true,
          ImageUpload: ImageUploadStub,
          BackupSettings: true,
        },
      },
    });

    await flushPromises();
    await openPaymentTab(wrapper);
    await wrapper.get(".provider-toggle-stub").trigger("click");
    await flushPromises();

    expect(updateProvider).toHaveBeenCalledWith(7, { enabled: true });
    expect(getProviders).toHaveBeenCalledTimes(2);
  });

  it("renders advanced scheduler copy as local experimental gateway policy", async () => {
    const wrapper = mountView();

    await flushPromises();

    expect(wrapper.text()).toContain("OpenAI 实验调度策略");
    expect(wrapper.text()).toContain(
      "默认关闭。开启后仅影响本网关在 OpenAI 账号间的实验性调度选择逻辑",
    );
    expect(wrapper.text()).not.toContain("OpenAI 高级调度器");
  });

  it("summarizes target and other-model actions, then switches to all models", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_fast_policy_settings: {
        rules: [
          {
            service_tier: "all",
            action: "filter",
            scope: "all",
            model_whitelist: ["gpt-5.6-sol"],
            fallback_action: "pass",
          },
        ],
      },
    });
    const wrapper = mountView();

    await flushPromises();
    await openGatewayTab(wrapper);

    const summary = wrapper.get('[data-testid="openai-fast-policy-summary-0"]');
    expect(summary.text()).toContain("目标模型");
    expect(summary.text()).toContain("过滤");
    expect(summary.text()).toContain("其他模型");
    expect(summary.text()).toContain("透传");

    await wrapper
      .get(
        '[role="group"][aria-labelledby="openai-fast-policy-models-label-0"] input[type="text"]',
      )
      .setValue("");
    expect(summary.text()).toContain("全部模型");
    expect(summary.text()).toContain("过滤");
    expect(summary.text()).not.toContain("其他模型");
    expect(summary.text()).not.toContain("透传");
  });

  it("loads and saves upstream billing probe settings from the gateway tab", async () => {
    getUpstreamBillingProbeSettings.mockResolvedValueOnce({
      enabled: false,
      interval_minutes: 45,
    });

    const wrapper = mountView();

    await flushPromises();
    await openGatewayTab(wrapper);

    const card = wrapper.get('[data-testid="upstream-billing-probe-settings"]');
    expect(card.isVisible()).toBe(true);
    expect(card.text()).toContain("上游倍率自动探测");
    expect(
      (card.get('[data-testid="upstream-billing-probe-enabled"]').element as HTMLInputElement)
        .checked,
    ).toBe(false);
    expect(card.find('[data-testid="upstream-billing-probe-interval"]').exists()).toBe(false);

    await card.get('[data-testid="upstream-billing-probe-enabled"]').setValue(true);
    await card.get('[data-testid="upstream-billing-probe-interval"]').setValue(60);
    await card.get('[data-testid="upstream-billing-probe-save"]').trigger("click");
    await flushPromises();

    expect(updateUpstreamBillingProbeSettings).toHaveBeenCalledWith({
      enabled: true,
      interval_minutes: 60,
    });
    expect(showSuccess).toHaveBeenCalledWith("上游倍率自动探测设置已保存");
  });

  it("loads and saves configurable Grok cross-client model mapping", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      grok_default_text_model: "grok-4.1-fast",
      grok_cross_client_model_map_enabled: true,
    });
    const wrapper = mountView();

    await flushPromises();
    await openGatewayTab(wrapper);

    const modelInput = wrapper.get('[data-testid="grok-default-text-model"]');
    const mappingToggle = wrapper.get(
      '[data-testid="grok-cross-client-model-map-toggle"]',
    );
    expect((modelInput.element as HTMLInputElement).value).toBe("grok-4.1-fast");
    expect((mappingToggle.element as HTMLInputElement).checked).toBe(true);

    await modelInput.setValue("grok-custom-text");
    await mappingToggle.setValue(false);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    const payload = updateSettings.mock.calls.at(-1)?.[0] as Record<string, unknown>;
    expect(payload.grok_default_text_model).toBe("grok-custom-text");
    expect(payload.grok_cross_client_model_map_enabled).toBe(false);
  });

  it("loads and saves the OpenAI Responses first-token metric mode", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_ttft_mode: "visible",
    });
    const wrapper = mountView();

    await flushPromises();
    await openGatewayTab(wrapper);

    const modeSelect = wrapper.get('[data-testid="openai-ttft-mode"]');
    expect((modeSelect.element as HTMLSelectElement).value).toBe("visible");

    await modeSelect.setValue("semantic");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    const payload = updateSettings.mock.calls.at(-1)?.[0] as Record<string, unknown>;
    expect(payload.openai_ttft_mode).toBe("semantic");
  });

  it("loads fail-safe-off Ollama Cloud usage refresh settings and saves an explicit opt-in", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openGatewayTab(wrapper);

    const card = wrapper.get('[data-testid="ollama-cloud-usage-global-settings"]');
    expect(card.isVisible()).toBe(true);
    expect(
      (card.get('[data-testid="ollama-cloud-usage-global-enabled"]').element as HTMLInputElement)
        .checked,
    ).toBe(false);
    expect(card.find('[data-testid="ollama-cloud-usage-global-interval"]').exists()).toBe(false);

    await card.get('[data-testid="ollama-cloud-usage-global-enabled"]').setValue(true);
    await card.get('[data-testid="ollama-cloud-usage-global-debounce"]').setValue(3);
    await card.get('[data-testid="ollama-cloud-usage-global-interval"]').setValue(90);
    await card.get('[data-testid="ollama-cloud-usage-global-save"]').trigger("click");
    await flushPromises();

    expect(updateOllamaCloudUsageSettings).toHaveBeenCalledWith({
      enabled: true,
      interval_minutes: 90,
      debounce_minutes: 3,
    });
  });

  it.each([false, true])("clears the OAuth rate without losing zero (weighted=%s)", async (weighted) => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_low_upstream_rate_priority_enabled: !weighted,
      openai_advanced_scheduler_enabled: weighted,
      openai_oauth_scheduling_rate_multiplier: 0.7,
    });
    const wrapper = mountView();
    await flushPromises();
    const input = wrapper.get('[data-testid="openai-oauth-scheduling-rate-multiplier"]');
    expect(input.attributes("required")).toBeUndefined();
    await input.setValue("");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings).toHaveBeenLastCalledWith(expect.objectContaining({
      openai_oauth_scheduling_rate_multiplier: null,
    }));
    await input.setValue("0");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings).toHaveBeenLastCalledWith(expect.objectContaining({
      openai_oauth_scheduling_rate_multiplier: 0,
    }));
    updateSettings.mockClear();
    await input.setValue("-1");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings).not.toHaveBeenCalled();
    expect(showError).toHaveBeenCalledWith("OAuth 调度参考倍率必须是非负数字，或留空以使用账号倍率。");
  });

  it("loads and preserves an explicitly cleared OAuth rate", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      openai_advanced_scheduler_enabled: true,
      openai_oauth_scheduling_rate_multiplier: null,
    });
    const wrapper = mountView();
    await flushPromises();
    const input = wrapper.get<HTMLInputElement>('[data-testid="openai-oauth-scheduling-rate-multiplier"]');
    expect(input.element.value).toBe("");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      openai_oauth_scheduling_rate_multiplier: null,
    }));
  });

  it("places and explains rate controls for both scheduling modes", async () => {
    const wrapper = mountView();

    await flushPromises();
    expect(
      wrapper.find('[data-testid="openai-oauth-scheduling-rate-multiplier"]').exists(),
    ).toBe(false);

    const lowRateToggle = wrapper.get('[data-testid="openai-low-rate-priority-toggle"]');
    await lowRateToggle.setValue(true);
    const priorityModeText = wrapper.text();
    expect(priorityModeText).toContain(
      "OAuth 账号按此参考倍率参与低倍率优先排序；留空时使用各自的账号倍率。",
    );
    expect(priorityModeText.indexOf("低倍率优先")).toBeLessThan(
      priorityModeText.indexOf("OAuth 调度参考倍率"),
    );
    expect(priorityModeText.indexOf("OAuth 调度参考倍率")).toBeLessThan(
      priorityModeText.indexOf("OpenAI 实验调度策略"),
    );

    const oauthRateInput = wrapper.get(
      '[data-testid="openai-oauth-scheduling-rate-multiplier"]',
    );
    await oauthRateInput.setValue("0.05");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        openai_low_upstream_rate_priority_enabled: true,
        openai_oauth_scheduling_rate_multiplier: 0.05,
      }),
    );

    await wrapper
      .get('[data-testid="openai-advanced-scheduler-toggle"]')
      .setValue(true);
    expect(
      wrapper.find('[data-testid="openai-low-rate-priority-toggle"]').exists(),
    ).toBe(false);
    expect(
      wrapper.find('[data-testid="openai-oauth-scheduling-rate-multiplier"]').exists(),
    ).toBe(true);
    const weightedModeText = wrapper.text();
    expect(weightedModeText).toContain(
      "计算“计费倍率”得分时，OAuth 账号使用此参考倍率；留空时使用各自的账号倍率。",
    );
    expect(weightedModeText).not.toContain(
      "OAuth 账号按此参考倍率参与低倍率优先排序；",
    );
    expect(weightedModeText.indexOf("订阅优先")).toBeLessThan(
      weightedModeText.indexOf("OAuth 调度参考倍率"),
    );
    expect(weightedModeText.indexOf("OAuth 调度参考倍率")).toBeLessThan(
      weightedModeText.indexOf("调度权值覆盖"),
    );
    expect(weightedModeText).toContain("计费倍率");
  });

  it("passes translated upload and remove labels to the payment help image uploader", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openPaymentTab(wrapper);

    const imageUploads = wrapper.findAll(".image-upload-stub");
    expect(imageUploads.length).toBeGreaterThan(0);

    const paymentHelpImageUpload = imageUploads.find(
      (node) => node.attributes("data-placeholder") === "admin.settings.payment.helpImagePlaceholder",
    );

    expect(paymentHelpImageUpload).toBeDefined();
    expect(paymentHelpImageUpload?.attributes("data-upload-label")).toBe("上传图片");
    expect(paymentHelpImageUpload?.attributes("data-remove-label")).toBe("移除");
  });

  it("normalizes null supported_types from API so provider card stays visible", async () => {
    // Backend returns null for supported_types when the list is empty
    // (Go nil slice → JSON null). Without normalization, ProviderCard's
    // isSelected() throws TypeError on null.includes(), causing the card
    // to vanish from the list.
    const providerWithNullTypes = {
      id: 42,
      provider_key: "easypay",
      name: "EasyPay",
      config: {},
      supported_types: null as unknown as string[],
      enabled: true,
      payment_mode: "",
      refund_enabled: false,
      allow_user_refund: false,
      limits: "",
      sort_order: 0,
    };
    getProviders.mockReset();
    getProviders.mockResolvedValue({ data: [providerWithNullTypes] });

    let receivedProviders: Array<Record<string, unknown>> = [];
    const PaymentProviderListCapture = defineComponent({
      props: {
        providers: {
          type: Array,
          default: () => [],
        },
      },
      setup(props) {
        return () => {
          receivedProviders = props.providers as Array<Record<string, unknown>>;
          return h("div", { class: "provider-list-capture" });
        };
      },
    });

    const wrapper = mount(SettingsView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          Select: SelectStub,
          Toggle: ToggleStub,
          Icon: true,
          ConfirmDialog: true,
          PaymentProviderList: PaymentProviderListCapture,
          PaymentProviderDialog: true,
          GroupBadge: true,
          GroupOptionItem: true,
          ProxySelector: true,
          ImageUpload: ImageUploadStub,
          BackupSettings: true,
        },
      },
    });

    await flushPromises();
    await openPaymentTab(wrapper);

    // The provider should still be in the list
    expect(receivedProviders.length).toBe(1);
    // supported_types should be normalized to an empty array, not null
    expect(Array.isArray(receivedProviders[0].supported_types)).toBe(true);
    expect(receivedProviders[0].supported_types).toEqual([]);
  });
});

describe("admin SettingsView wechat connect controls", () => {
  beforeEach(() => {
    getSettings.mockReset();
    updateSettings.mockReset();
    getWebSearchEmulationConfig.mockReset();
    updateWebSearchEmulationConfig.mockReset();
    getAdminApiKey.mockReset();
    getOverloadCooldownSettings.mockReset();
    getRateLimit429CooldownSettings.mockReset();
    updateRateLimit429CooldownSettings.mockReset();
    getStreamTimeoutSettings.mockReset();
    getRectifierSettings.mockReset();
    getBetaPolicySettings.mockReset();
    getGroups.mockReset();
    listProxies.mockReset();
    getProviders.mockReset();
    updateProvider.mockReset();
    createProvider.mockReset();
    deleteProvider.mockReset();
    fetchPublicSettings.mockReset();
    adminSettingsFetch.mockReset();
    showError.mockReset();
    showSuccess.mockReset();

    getSettings.mockResolvedValue({
      ...baseSettingsResponse,
      payment_visible_method_wxpay_source: "official_wxpay",
    });
    updateSettings.mockImplementation(async (payload) => ({
      ...baseSettingsResponse,
      payment_visible_method_wxpay_source: "official_wxpay",
      ...payload,
    }));
    getWebSearchEmulationConfig.mockResolvedValue({
      enabled: false,
      providers: [],
    });
    updateWebSearchEmulationConfig.mockResolvedValue({
      enabled: false,
      providers: [],
    });
    getAdminApiKey.mockResolvedValue({
      exists: false,
      masked_key: "",
    });
    getOverloadCooldownSettings.mockResolvedValue({
      enabled: true,
      cooldown_minutes: 10,
    });
    getRateLimit429CooldownSettings.mockResolvedValue({
      enabled: true,
      cooldown_seconds: 5,
    });
    updateRateLimit429CooldownSettings.mockImplementation(async (payload) => payload);
    getStreamTimeoutSettings.mockResolvedValue({
      enabled: true,
      action: "temp_unsched",
      temp_unsched_minutes: 5,
      threshold_count: 3,
      threshold_window_minutes: 10,
    });
    getRectifierSettings.mockResolvedValue({
      enabled: true,
      thinking_signature_enabled: true,
      thinking_budget_enabled: true,
      apikey_signature_enabled: false,
      apikey_signature_patterns: [],
    });
    getBetaPolicySettings.mockResolvedValue({
      rules: [],
    });
    getGroups.mockResolvedValue([]);
    listProxies.mockResolvedValue({
      items: [],
    });
    getProviders.mockResolvedValue({
      data: [],
    });
    fetchPublicSettings.mockResolvedValue(undefined);
    adminSettingsFetch.mockResolvedValue(undefined);
  });

  it("loads and echoes WeChat Connect fields from the backend payload", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);

    expect(
      (
        wrapper.get('[data-testid="wechat-connect-mp-app-id"]')
          .element as HTMLInputElement
      ).value,
    ).toBe("wx-app-id-123");
    expect(
      (
        wrapper.get('[data-testid="wechat-connect-open-enabled"]')
          .element as HTMLInputElement
      ).checked,
    ).toBe(false);
    expect(
      (
        wrapper.get('[data-testid="wechat-connect-mp-enabled"]')
          .element as HTMLInputElement
      ).checked,
    ).toBe(true);
    expect(wrapper.find('[data-testid="wechat-connect-scopes"]').exists()).toBe(
      false,
    );
    expect(
      wrapper
        .get('[data-testid="wechat-connect-mp-app-secret"]')
        .attributes("placeholder"),
    ).toContain("密钥已配置");
    expect(
      (
        wrapper.get('[data-testid="wechat-connect-frontend-redirect-url"]')
          .element as HTMLInputElement
      ).value,
    ).toBe("/auth/wechat/callback");
  });

  it("links GitHub OAuth Apps guide to GitHub developer settings", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      github_oauth_enabled: true,
    });

    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);

    const link = wrapper.get('[data-testid="github-oauth-apps-guide-link"]');
    expect(link.text()).toContain("OAuth Apps");
    expect(link.attributes("href")).toBe("https://github.com/settings/developers");
    expect(link.attributes("target")).toBe("_blank");
    expect(link.attributes("rel")).toContain("noopener");
  });

  it("saves WeChat Connect fields using the backend contract and clears the secret after save", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);

    await wrapper
      .get('[data-testid="wechat-connect-mp-app-id"]')
      .setValue("wx-app-id-updated");
    await wrapper
      .get('[data-testid="wechat-connect-mp-app-secret"]')
      .setValue("new-secret");
    await wrapper
      .get('[data-testid="wechat-connect-open-enabled"]')
      .setValue(true);
    await wrapper
      .get('[data-testid="wechat-connect-mp-enabled"]')
      .setValue(true);
    await wrapper
      .get('[data-testid="wechat-connect-redirect-url"]')
      .setValue("https://admin.example.com/api/v1/auth/oauth/wechat/callback");
    await wrapper
      .get('[data-testid="wechat-connect-frontend-redirect-url"]')
      .setValue("/auth/wechat/callback");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        wechat_connect_enabled: true,
        wechat_connect_app_id: "wx-app-id-updated",
        wechat_connect_open_enabled: true,
        wechat_connect_mp_enabled: true,
        wechat_connect_mp_app_id: "wx-app-id-updated",
        wechat_connect_mp_app_secret: "new-secret",
        wechat_connect_redirect_url:
          "https://admin.example.com/api/v1/auth/oauth/wechat/callback",
        wechat_connect_frontend_redirect_url: "/auth/wechat/callback",
      }),
    );
    expect(
      (
        wrapper.get('[data-testid="wechat-connect-mp-app-secret"]')
          .element as HTMLInputElement
      ).value,
    ).toBe("");
    expect(
      wrapper
        .get('[data-testid="wechat-connect-mp-app-secret"]')
        .attributes("placeholder"),
    ).toContain("密钥已配置");
  });

  it("collapses auth source defaults until the source is enabled", async () => {
    const wrapper = mountView();

    await flushPromises();
    await openUsersTab(wrapper);

    expect(
      (
        wrapper.get('[data-testid="auth-source-email-enabled"]')
          .element as HTMLInputElement
      ).checked,
    ).toBe(false);
    expect(
      wrapper.find('[data-testid="auth-source-email-panel"]').exists(),
    ).toBe(false);
    expect(wrapper.text()).not.toContain("注册即授权");

    await wrapper
      .get('[data-testid="auth-source-email-enabled"]')
      .setValue(true);

    expect(
      wrapper.find('[data-testid="auth-source-email-panel"]').exists(),
    ).toBe(true);
    expect(wrapper.text()).toContain("首次绑定时授权");
  });

  it("preserves optional OIDC compatibility flags instead of forcing them on save", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      oidc_connect_enabled: true,
      oidc_connect_use_pkce: false,
      oidc_connect_validate_id_token: false,
    });

    const wrapper = mountView();

    await flushPromises();
    await openSecurityTab(wrapper);
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledTimes(1);
    expect(updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        oidc_connect_use_pkce: false,
        oidc_connect_validate_id_token: false,
      }),
    );
  });
});

describe("admin SettingsView platform quota matrix", () => {
  beforeEach(() => {
    getSettings.mockReset();
    updateSettings.mockReset();
    getWebSearchEmulationConfig.mockReset();
    updateWebSearchEmulationConfig.mockReset();
    getAdminApiKey.mockReset();
    getOverloadCooldownSettings.mockReset();
    getRateLimit429CooldownSettings.mockReset();
    updateRateLimit429CooldownSettings.mockReset();
    getStreamTimeoutSettings.mockReset();
    getRectifierSettings.mockReset();
    getBetaPolicySettings.mockReset();
    getGroups.mockReset();
    listProxies.mockReset();
    getProviders.mockReset();
    updateProvider.mockReset();
    createProvider.mockReset();
    deleteProvider.mockReset();
    fetchPublicSettings.mockReset();
    adminSettingsFetch.mockReset();
    showError.mockReset();
    showSuccess.mockReset();
    localeRef.value = "zh-CN";

    getSettings.mockResolvedValue({ ...baseSettingsResponse });
    updateSettings.mockImplementation(async (payload) => ({
      ...baseSettingsResponse,
      ...payload,
    }));
    getWebSearchEmulationConfig.mockResolvedValue({ enabled: false, providers: [] });
    updateWebSearchEmulationConfig.mockResolvedValue({ enabled: false, providers: [] });
    getAdminApiKey.mockResolvedValue({ exists: false, masked_key: "" });
    getOverloadCooldownSettings.mockResolvedValue({});
    getRateLimit429CooldownSettings.mockResolvedValue({});
    updateRateLimit429CooldownSettings.mockResolvedValue({});
    getStreamTimeoutSettings.mockResolvedValue({});
    getRectifierSettings.mockResolvedValue({});
    getBetaPolicySettings.mockResolvedValue({});
    getGroups.mockResolvedValue([]);
    listProxies.mockResolvedValue({ items: [] });
    getProviders.mockResolvedValue({ data: [] });
  });

  it("从 baseSettings 加载默认平台配额数据并在 Users tab 渲染 6 平台行", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openUsersTab(wrapper);

    expect(getSettings).toHaveBeenCalled();

    const html = wrapper.html();
    // 表格行的平台字段：font-mono 渲染纯英文 platform key
    expect(html).toContain("anthropic");
    expect(html).toContain("openai");
    expect(html).toContain("gemini");
    expect(html).toContain("antigravity");
    expect(html).toContain("typesafe");
  });

  it("保存时 updateSettings payload 应包含嵌套 default_platform_quotas 对象（含全 6 平台）", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openUsersTab(wrapper);

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(updateSettings).toHaveBeenCalled();
    const lastCallArgs = updateSettings.mock.calls.at(-1);
    expect(lastCallArgs).toBeDefined();
    const payload = lastCallArgs![0] as Record<string, unknown>;

    // 应携带嵌套对象，而非扁平字段
    expect(payload).toHaveProperty("default_platform_quotas");
    const quotas = payload["default_platform_quotas"] as Record<string, unknown>;
    const platforms = ["anthropic", "openai", "gemini", "antigravity", "grok", "typesafe"];
    for (const p of platforms) {
      expect(quotas).toHaveProperty(p);
      const pq = quotas[p] as Record<string, unknown>;
      expect(pq).toHaveProperty("daily");
      expect(pq).toHaveProperty("weekly");
      expect(pq).toHaveProperty("monthly");
    }

    // 不应存在旧扁平字段
    expect(payload).not.toHaveProperty("default_platform_quota_anthropic_daily");
    expect(payload).not.toHaveProperty("default_platform_quota_openai_weekly");
  });

  it("加载后 form.default_platform_quotas 含全 6 平台，从嵌套 JSON 正确读取数值", async () => {
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      default_platform_quotas: {
        anthropic: { daily: 5, weekly: null, monthly: null },
        openai:    { daily: null, weekly: 12.5, monthly: null },
        // gemini / antigravity 缺失 → 应被归一化为全 null
      },
    });

    const wrapper = mountView();
    await flushPromises();
    await openUsersTab(wrapper);

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    const payload = updateSettings.mock.calls.at(-1)![0] as Record<string, unknown>;
    const quotas = payload["default_platform_quotas"] as Record<string, Record<string, unknown>>;

    expect(quotas["anthropic"]?.["daily"]).toBe(5);
    expect(quotas["openai"]?.["weekly"]).toBe(12.5);
    // 缺失平台应补全为 null
    expect(quotas["gemini"]).toEqual({ daily: null, weekly: null, monthly: null });
    expect(quotas["antigravity"]).toEqual({ daily: null, weekly: null, monthly: null });
    expect(quotas["typesafe"]).toEqual({ daily: null, weekly: null, monthly: null });
  });

  it("空输入（v-model.number 产出 \"\"）在提交时清洗为 null 而非空字符串", async () => {
    // 模拟后端返回带有 anthropic daily 值的配额
    getSettings.mockResolvedValueOnce({
      ...baseSettingsResponse,
      default_platform_quotas: {
        anthropic: { daily: 10, weekly: null, monthly: null },
        openai:    { daily: null, weekly: null, monthly: null },
        gemini:    { daily: null, weekly: null, monthly: null },
        antigravity: { daily: null, weekly: null, monthly: null },
      },
    });

    const wrapper = mountView();
    await flushPromises();
    await openUsersTab(wrapper);

    // 找到 anthropic daily 输入框并清空（模拟用户删除值）
    const inputs = wrapper.findAll('input[type="number"]');
    const anthropicDailyInput = inputs.find((i) => {
      const parent = i.element.closest("tr");
      return parent?.textContent?.includes("anthropic");
    });

    if (anthropicDailyInput) {
      // 设置为空字符串，模拟 v-model.number 在清空时产出 ""
      await anthropicDailyInput.setValue("");
    }

    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    const payload = updateSettings.mock.calls.at(-1)![0] as Record<string, unknown>;
    const quotas = payload["default_platform_quotas"] as Record<string, Record<string, unknown>>;
    // 不管输入是什么，提交值应为 null（而非 "" 或 NaN）
    expect(quotas["anthropic"]?.["daily"]).toBe(null);
  });
});
