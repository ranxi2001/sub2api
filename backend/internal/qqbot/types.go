// Package qqbot implements the operator-funded QQ group assistant.
// It has no access to member API keys and never debits member balances.
package qqbot

import (
	"encoding/json"
	"errors"
	"time"
)

var ErrDisabled = errors.New("QQ 机器人尚未启用")

type Config struct {
	InviteCouponTemplateID   int64                    `json:"invite_coupon_template_id,omitempty"`
	InviteCouponQuantity     int                      `json:"invite_coupon_quantity,omitempty"`
	SetupWarning             string                   `json:"setup_warning,omitempty"`
	RewardBudgetDaily        *float64                 `json:"reward_budget_daily"`
	AIConnection             string                   `json:"ai_connection"`
	SiteAPIKeyID             int64                    `json:"site_api_key_id"`
	SiteGroupID              int64                    `json:"site_group_id"`
	SiteOwnerID              int64                    `json:"site_owner_id"`
	NotificationQuietHours   QuietHours               `json:"notification_quiet_hours"`
	SectionVersions          map[string]string        `json:"section_versions,omitempty"`
	ChatDisabled             bool                     `json:"chat_disabled"`
	GroupPolicies            []GroupPolicy            `json:"group_policies"`
	RewardAlertDaily         float64                  `json:"reward_alert_daily"`
	Moderation               ModerationConfig         `json:"moderation"`
	HealthNotifications      HealthNotificationConfig `json:"health_notifications"`
	Autonomous               AutonomousConfig         `json:"autonomous"`
	Enabled                  bool                     `json:"enabled"`
	AppID                    string                   `json:"app_id"`
	AppSecret                string                   `json:"app_secret"`
	AppSecretSet             bool                     `json:"app_secret_set"`
	Sandbox                  bool                     `json:"sandbox"`
	AllowedGroups            []string                 `json:"allowed_groups"`
	PriceNotifications       PriceNotificationConfig  `json:"price_notifications"`
	CustomCommands           []CustomCommand          `json:"custom_commands"`
	GroupAdmins              []GroupAdmin             `json:"group_admins"`
	BaseURL                  string                   `json:"base_url"`
	APIKey                   string                   `json:"api_key"`
	APIKeySet                bool                     `json:"api_key_set"`
	ChatModel                string                   `json:"chat_model"`
	ChatProtocol             string                   `json:"chat_protocol"`
	ImageEnabled             bool                     `json:"image_enabled"`
	ImageAIConnection        string                   `json:"image_ai_connection"`
	ImageSiteAPIKeyID        int64                    `json:"image_site_api_key_id"`
	ImageSiteGroupID         int64                    `json:"image_site_group_id"`
	ImageSiteOwnerID         int64                    `json:"image_site_owner_id"`
	ImageBaseURL             string                   `json:"image_base_url"`
	ImageAPIKey              string                   `json:"image_api_key"`
	ImageAPIKeySet           bool                     `json:"image_api_key_set"`
	ImageModel               string                   `json:"image_model"`
	ImageSize                string                   `json:"image_size"`
	ImageOutputFormat        string                   `json:"image_output_format"`
	SystemPrompt             string                   `json:"system_prompt"`
	SiteKnowledge            SiteKnowledgeConfig      `json:"site_knowledge"`
	MaxOutputTokens          int                      `json:"max_output_tokens"`
	HistoryMessages          int                      `json:"history_messages"`
	DailyChatLimit           int                      `json:"daily_chat_limit"`
	DailyImageLimit          int                      `json:"daily_image_limit"`
	TotalDailyChatLimit      int                      `json:"total_daily_chat_limit"`
	TotalDailyImageLimit     int                      `json:"total_daily_image_limit"`
	CooldownSeconds          int                      `json:"cooldown_seconds"`
	CheckinReward            float64                  `json:"checkin_reward"`
	InviteEnabled            bool                     `json:"invite_enabled"`
	InviteReward             float64                  `json:"invite_reward"`
	InviteDailyLimit         int                      `json:"invite_daily_limit"`
	InviteMinRechargeTotal   float64                  `json:"invite_min_recharge_total"`
	InviteMinRecharge30d     float64                  `json:"invite_min_recharge_30d"`
	InviteRechargeWindowDays int                      `json:"invite_recharge_window_days"`
	TransferEnabled          bool                     `json:"transfer_enabled"`
	TransferFeePercent       float64                  `json:"transfer_fee_percent"`
	TransferMinAmount        float64                  `json:"transfer_min_amount"`
	TransferMaxAmount        float64                  `json:"transfer_max_amount"`
	ClearAppSecret           bool                     `json:"clear_app_secret,omitempty"`
	ClearAPIKey              bool                     `json:"clear_api_key,omitempty"`
	ClearImageAPIKey         bool                     `json:"clear_image_api_key,omitempty"`
}

func DefaultConfig() Config {
	return Config{NotificationQuietHours: QuietHours{StartHour: 23, EndHour: 8}, Moderation: DefaultModerationConfig(), HealthNotifications: DefaultHealthNotificationConfig(), Autonomous: DefaultAutonomousConfig(), PriceNotifications: PriceNotificationConfig{Scheduled: true, Groups: []string{}}, AllowedGroups: []string{}, CustomCommands: []CustomCommand{}, GroupAdmins: []GroupAdmin{}, ChatProtocol: "chat_completions", ImageSize: "1024x1024",
		SystemPrompt:    "你是 CTAV 群助手。用简洁、友好的中文回答。签到、绑定和奖励由固定指令处理，你不能修改账户或余额。",
		MaxOutputTokens: 2048, HistoryMessages: 12, DailyChatLimit: 30, DailyImageLimit: 2,
		TotalDailyChatLimit: 500, TotalDailyImageLimit: 20, CooldownSeconds: 5, InviteDailyLimit: 10,
		InviteRechargeWindowDays: DefaultRechargeWindowDays}
}

func (c Config) Public() Config {
	c.SectionVersions = configVersions(c)
	c.AppSecretSet = c.AppSecret != ""
	c.APIKeySet = c.APIKey != ""
	c.ImageAPIKeySet = c.ImageAPIKey != ""
	c.AppSecret = ""
	c.APIKey = ""
	c.ImageAPIKey = ""
	return c
}
func (c Config) allows(group string) bool {
	for _, g := range c.AllowedGroups {
		if g == group {
			return true
		}
	}
	return false
}

type Payload struct {
	ID   string          `json:"id"`
	Op   int             `json:"op"`
	Type string          `json:"t"`
	Data json.RawMessage `json:"d"`
}
type Event struct {
	HelpAction       string           `json:"help_action,omitempty"`
	ID               string           `json:"id"`
	GroupID          string           `json:"group_openid"`
	MemberID         string           `json:"member_openid"`
	Content          string           `json:"content"`
	Timestamp        json.RawMessage  `json:"timestamp"`
	Mentions         []Mention        `json:"mentions"`
	Attachments      []Attachment     `json:"attachments"`
	MessageScene     MessageScene     `json:"message_scene"`
	MessageType      int              `json:"message_type"`
	MsgElements      []MessageElement `json:"msg_elements"`
	MessageReference struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
	Author struct {
		MemberRole string `json:"member_role"`
		MemberID   string `json:"member_openid"`
		Username   string `json:"username"`
		Bot        bool   `json:"bot"`
	} `json:"author"`
	ApplySource   string `json:"apply_source"`
	InvitedBy     string `json:"invited_by"`
	ApplyAt       string `json:"apply_at"`
	JoinRequestID string `json:"join_request_id"`
}
type Attachment struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}
type MessageScene struct {
	Ext []string `json:"ext"`
}
type MessageElement struct {
	MsgIdx      string           `json:"msg_idx"`
	MessageType int              `json:"message_type"`
	Content     string           `json:"content"`
	Attachments []Attachment     `json:"attachments"`
	MsgElements []MessageElement `json:"msg_elements"`
}
type Mention struct {
	ID       string `json:"id"`
	MemberID string `json:"member_openid"`
}
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"-"`
}

// ChatExecution records bounded upstream chat attempts. UsageComplete is false
// when one or more attempts did not provide reliable token usage.
type ChatExecution struct {
	Attempts      int    `json:"attempts"`
	Retries       int    `json:"retries"`
	UsageComplete bool   `json:"usage_complete"`
	LastErrorCode string `json:"last_error_code,omitempty"`
}

// MarshalJSON keeps ordinary turns compatible with the existing API payload,
// while turning image-bearing turns into the Chat Completions multimodal shape.
// Responses requests use responsesMessages in client.go because that protocol
// names the equivalent parts input_text/input_image.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Images) == 0 {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: m.Role, Content: m.Content})
	}
	parts := make([]map[string]any, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	for _, imageURL := range m.Images {
		if imageURL != "" {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageURL}})
		}
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{Role: m.Role, Content: parts})
}

type Job struct {
	AutonomousData  json.RawMessage `json:"autonomous_data,omitempty"`
	SiteQuery       json.RawMessage `json:"site_query,omitempty"`
	ImageDecision   json.RawMessage `json:"image_decision,omitempty"`
	ChatExecution   json.RawMessage `json:"chat_execution,omitempty"`
	TransientReply  bool            `json:"-"`
	TriggerReason   string          `json:"trigger_reason"`
	DecisionReason  string          `json:"decision_reason"`
	SuppressMention bool            `json:"-"`
	ShareHistory    bool            `json:"-"`
	ReplyReference  string          `json:"-"`
	ID              string          `json:"id"`
	AppID           string          `json:"app_id"`
	EventType       string          `json:"event_type"`
	GroupID         string          `json:"group_id"`
	MemberID        string          `json:"member_id"`
	MessageID       string          `json:"-"`
	Payload         json.RawMessage `json:"-"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	ResultText      string          `json:"-"`
	Image           []byte          `json:"-"`
	ImageType       string          `json:"-"`
	Error           string          `json:"error"`
	InputTokens     int64           `json:"input_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	CreatedAt       time.Time       `json:"created_at"`
	FinishedAt      *time.Time      `json:"finished_at"`
}
type Binding struct {
	AppID     string    `json:"app_id"`
	MemberID  string    `json:"member_id"`
	GroupID   string    `json:"group_id"`
	Nickname  string    `json:"nickname"`
	UserID    int64     `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}
type BindRequest struct {
	Code      string    `json:"code,omitempty"`
	MemberID  string    `json:"member_id"`
	GroupID   string    `json:"group_id"`
	Nickname  string    `json:"nickname"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Reward struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Kind      string    `json:"kind"`
	Amount    float64   `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}
type Group struct {
	ID         string    `json:"id"`
	LastEvent  string    `json:"last_event"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
type Runtime struct {
	RecentMembers     []RecentMember     `json:"recent_members"`
	ModerationRecords []ModerationRecord `json:"moderation_records"`
	AutonomousStats   []AutonomousStat   `json:"autonomous_stats"`
	Groups            []Group            `json:"groups"`
	Jobs              []Job              `json:"jobs"`
	Rewards           []Reward           `json:"rewards"`
	Bindings          int                `json:"bindings"`
	EncryptionReady   bool               `json:"encryption_ready"`
}
