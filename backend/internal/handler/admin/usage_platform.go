package admin

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/gin-gonic/gin"
)

func parseUsagePlatform(c *gin.Context) (string, error) {
	platform := strings.TrimSpace(c.Query("platform"))
	if platform == "" || platform == "unknown" || domain.IsConcretePlatform(platform) {
		return platform, nil
	}
	return "", fmt.Errorf("invalid platform: use a concrete provider or unknown")
}
