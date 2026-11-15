package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

func requireQQBotProbeConfirmation(c *gin.Context) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var input struct {
		Confirm bool `json:"confirm"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !input.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "reason": "QQ_BOT_CONFIRMATION_REQUIRED", "message": "Explicit confirmation is required before a capability test"})
		return false
	}
	return true
}
