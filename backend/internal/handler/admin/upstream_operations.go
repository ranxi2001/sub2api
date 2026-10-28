package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *ChannelHandler) UpstreamOperations(c *gin.Context) { response.Success(c, gin.H{}) }
