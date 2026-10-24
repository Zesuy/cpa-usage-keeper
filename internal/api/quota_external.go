package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/service"
	"github.com/gin-gonic/gin"
)

// This capability deliberately grants no refresh or credential management access.
type quotaSummaryReader interface {
	GetQuotaSummary(context.Context) (quota.SummaryResponse, error)
}

func writeQuotaSummary(c *gin.Context, provider QuotaProvider) {
	summary, ok := provider.(quotaSummaryReader)
	if !ok {
		writeInternalError(c, "quota summary provider is not configured", nil)
		return
	}
	response, err := summary.GetQuotaSummary(c.Request.Context())
	if err != nil {
		writeInternalError(c, "quota summary lookup failed", err)
		return
	}
	c.JSON(http.StatusOK, response)
}

func registerExternalQuotaSummaryRoute(router *gin.RouterGroup, provider QuotaProvider, key string, gate *service.CostReadGate, basePath string) {
	key = strings.TrimSpace(key)
	expected := sha256.Sum256([]byte(key))
	external := router.Group("/external")
	external.Use(func(c *gin.Context) {
		setNoStoreHeaders(c)
		if key == "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		// Cookies, query arguments and other credentials never authenticate this route.
		headers := c.Request.Header.Values("Authorization")
		fields := strings.Fields(c.GetHeader("Authorization"))
		if len(headers) != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			c.Header("WWW-Authenticate", `Bearer realm="quota-summary"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		actual := sha256.Sum256([]byte(fields[1]))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			c.Header("WWW-Authenticate", `Bearer realm="quota-summary"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	})
	external.Use(costReadMiddleware(gate, basePath))
	external.GET("/quota/summary", func(c *gin.Context) {
		writeQuotaSummary(c, provider)
	})
}
