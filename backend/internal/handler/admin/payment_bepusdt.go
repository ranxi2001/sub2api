package admin

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/bepusdt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/v03413/bepusdt/app/model"
)

func collectionError(c *gin.Context, err error) {
	if bepusdt.IsNotFound(err) {
		response.NotFound(c, "collection record not found")
		return
	}
	response.BadRequest(c, err.Error())
}

func (h *PaymentHandler) GetBEpusdt(c *gin.Context) {
	settings, err := bepusdt.Settings()
	if err != nil {
		collectionError(c, err)
		return
	}
	wallets, err := bepusdt.Wallets(c.Request.Context())
	if err != nil {
		collectionError(c, err)
		return
	}
	rates, err := bepusdt.Rates(c.Request.Context())
	if err != nil {
		collectionError(c, err)
		return
	}
	configured := make(map[string]bool)
	for _, key := range []string{"rpc_endpoint_tron_grid_api_key", "rate_sync_coingecko_api_key"} {
		configured[key] = settings[key] != ""
		delete(settings, key)
	}
	response.Success(c, gin.H{"settings": settings, "secret_configured": configured, "wallets": wallets, "rates": rates, "stats": bepusdt.Stats(), "node_checks": bepusdt.NodeChecks()})
}

func (h *PaymentHandler) CheckBEpusdtNodes(c *gin.Context) {
	checks, err := bepusdt.CheckNodes(c.Request.Context())
	if err != nil {
		collectionError(c, err)
		return
	}
	response.Success(c, gin.H{"node_checks": checks})
}

func (h *PaymentHandler) SaveBEpusdtSettings(c *gin.Context) {
	var req struct {
		Settings map[string]string `json:"settings" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	for _, key := range []string{"rpc_endpoint_tron_grid_api_key", "rate_sync_coingecko_api_key"} {
		if strings.TrimSpace(req.Settings[key]) == "" {
			delete(req.Settings, key)
		}
	}
	if err := bepusdt.SaveSettings(c.Request.Context(), req.Settings); err != nil {
		collectionError(c, err)
		return
	}
	response.Success(c, gin.H{"saved": true})
}

func (h *PaymentHandler) SaveBEpusdtWallet(c *gin.Context) {
	var req struct {
		Name      string `json:"name"`
		Address   string `json:"address" binding:"required"`
		TradeType string `json:"trade_type" binding:"required"`
		Status    uint8  `json:"status"`
		Remark    string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var id int64
	if c.Param("id") != "" {
		parsed, ok := parseIDParam(c, "id")
		if !ok {
			return
		}
		id = parsed
	}
	wallet := model.Wallet{Id: model.Id{ID: id}, Name: req.Name, Address: req.Address, TradeType: req.TradeType, Status: req.Status, Remark: req.Remark}
	if err := bepusdt.SaveWallet(c.Request.Context(), wallet); err != nil {
		collectionError(c, err)
		return
	}
	response.Success(c, gin.H{"saved": true})
}

func (h *PaymentHandler) DeleteBEpusdtWallet(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := bepusdt.DeleteWallet(c.Request.Context(), id); err != nil {
		collectionError(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

func (h *PaymentHandler) SyncBEpusdtRates(c *gin.Context) {
	if err := bepusdt.SyncRates(); err != nil {
		collectionError(c, err)
		return
	}
	response.Success(c, gin.H{"synced": true})
}

func (h *PaymentHandler) SaveBEpusdtRate(c *gin.Context) {
	var req struct {
		Fiat   string `json:"fiat" binding:"required"`
		Crypto string `json:"crypto" binding:"required"`
		Rate   string `json:"rate" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, fmt.Sprintf("invalid exchange rate: %v", err))
		return
	}
	if err := bepusdt.SaveRate(c.Request.Context(), req.Fiat, req.Crypto, req.Rate); err != nil {
		collectionError(c, err)
		return
	}
	response.Success(c, gin.H{"saved": true})
}
