package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// Routes adapted from DeanZFC/sub2api-custom 5876d7d864dae405c2a3b65074fb556f6985adb8 (LGPL v3).
func registerQQBotAdminRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.QQBot != nil {
		bot := admin.Group("/qqbot")
		bot.GET("/config", h.QQBot.GetConfig)
		bot.GET("/setup/options", h.QQBot.SetupOptions)
		bot.GET("/activity-templates", h.QQBot.ActivityTemplates)
		bot.POST("/activity-templates", h.QQBot.ApplyActivityTemplate)
		bot.GET("/setup/checks", h.QQBot.SetupChecks)
		bot.PUT("/setup", h.QQBot.SaveSetup)
		bot.POST("/setup/probe/:kind", h.QQBot.SetupProbe)
		bot.PUT("/config", h.QQBot.SaveConfig)
		bot.GET("/runtime", h.QQBot.Runtime)
		bot.POST("/announcements", h.QQBot.SendAnnouncement)
		bot.GET("/members", h.QQBot.ListMembers)
		bot.PUT("/config/:section", h.QQBot.SaveConfigSection)
		bot.GET("/config-history", h.QQBot.ConfigHistory)
		bot.GET("/overview", h.QQBot.Overview)
		bot.GET("/jobs", h.QQBot.ListJobs)
		bot.GET("/moderation-records", h.QQBot.ListModerationRecords)
		bot.GET("/diagnosis", h.QQBot.Diagnose)
		bot.GET("/commands", h.QQBot.Commands)
		bot.GET("/activity-preview", h.QQBot.PreviewActivities)
		bot.DELETE("/groups", h.QQBot.DeleteDiscoveredGroup)
		bot.GET("/groups/:group_id/info", h.QQBot.GroupInfo)
		bot.GET("/records/:kind", h.QQBot.ListActivityRecords)
		bot.GET("/checkin", h.QQBot.GetCheckinConfig)
		bot.PUT("/checkin", h.QQBot.SaveCheckinConfig)
		bot.GET("/rankings", h.QQBot.GetRankingConfig)
		bot.PUT("/rankings", h.QQBot.SaveRankingConfig)
		bot.GET("/invite", h.QQBot.GetInviteConfig)
		bot.PUT("/invite", h.QQBot.SaveInviteConfig)
		bot.GET("/transfer", h.QQBot.GetTransferConfig)
		bot.GET("/transfers", h.QQBot.TransferHistory)
		bot.PUT("/transfer", h.QQBot.SaveTransferConfig)
		bot.GET("/lottery", h.QQBot.GetLotteryConfig)
		bot.PUT("/lottery", h.QQBot.SaveLotteryConfig)
		bot.GET("/lottery/draws", h.QQBot.ListLotteryDraws)
		bot.GET("/lottery/users/:user_id", h.QQBot.LotteryState)
		bot.GET("/reward-codes", h.QQBot.ListRewardCodes)
		bot.PATCH("/reward-codes/:id", h.QQBot.UpdateRewardCode)
		bot.GET("/bindings", h.QQBot.ListBindings)
		bot.DELETE("/bindings/:user_id", h.QQBot.Unbind)
		bot.POST("/test/:kind", h.QQBot.Probe)
		bot.POST("/chat-preview", h.QQBot.PreviewChat)
	}
}
