package nova

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(apiRouter *gin.RouterGroup) {
	if !Enabled() {
		return
	}

	novaRouter := apiRouter.Group("/novapay")
	novaRouter.Use(
		middleware.AnonymousRequestBodyLimit(),
		middleware.CriticalRateLimit(),
		middleware.DisableCache(),
		HMACAuth(),
	)
	novaRouter.GET("/health", health)
	novaRouter.POST("/tenant", createTenant)
	novaRouter.GET("/tenant/:tenant_key", getTenant)
	novaRouter.PUT("/tenant/:tenant_key", updateTenant)
	novaRouter.DELETE("/tenant/:tenant_key", deleteTenant)
	novaRouter.GET("/tenants", listTenants)
	novaRouter.POST("/tenant/:tenant_key/quota", adjustTenantQuota)
	novaRouter.POST("/tenant/:tenant_key/disable", func(c *gin.Context) { setTenantStatus(c, tenantStatusDisabled) })
	novaRouter.POST("/tenant/:tenant_key/enable", func(c *gin.Context) { setTenantStatus(c, tenantStatusEnabled) })
	novaRouter.POST("/tenant/:tenant_key/token/rotate", rotateToken)
	novaRouter.GET("/tenant/:tenant_key/tokens", listTokens)
	novaRouter.POST("/tenant/:tenant_key/tokens/:token_name/quota", adjustTokenQuota)
	novaRouter.DELETE("/tenant/:tenant_key/tokens/:token_name", deleteToken)
	novaRouter.GET("/tenant/:tenant_key/logs", listUsageLogs)
	novaRouter.GET("/models", listModels)
}

func health(c *gin.Context) {
	_, db := currentState()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"status":  "unavailable",
		})
		return
	}

	var pending int64
	var dead int64
	var oldestPendingAt int64
	var unresolvedAttributions int64
	var oldestAttributionAt int64
	databaseOK := db.Raw("SELECT 1").Error == nil
	if databaseOK {
		databaseOK = db.Model(&Outbox{}).Where("status = ?", "pending").Count(&pending).Error == nil &&
			db.Model(&Outbox{}).Where("status = ?", "dead").Count(&dead).Error == nil &&
			db.Model(&Outbox{}).Where("status IN ?", []string{"pending", "publishing"}).Select("COALESCE(MIN(created_at), 0)").Scan(&oldestPendingAt).Error == nil &&
			db.Model(&Attribution{}).Count(&unresolvedAttributions).Error == nil &&
			db.Model(&Attribution{}).Select("COALESCE(MIN(created_at), 0)").Scan(&oldestAttributionAt).Error == nil
	}
	status := "ok"
	httpStatus := http.StatusOK
	if !databaseOK {
		status = "unavailable"
		httpStatus = http.StatusServiceUnavailable
	}
	mqConfigured, mqConnected := publisherHealth()
	healthy := databaseOK && mqConfigured && mqConnected
	if databaseOK && !healthy {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}
	c.JSON(httpStatus, gin.H{
		"success": healthy,
		"status":  status,
		"components": gin.H{
			"database": gin.H{"ok": databaseOK},
			"outbox": gin.H{
				"pending":           pending,
				"dead":              dead,
				"oldest_pending_at": oldestPendingAt,
			},
			"usage_candidates": gin.H{"unresolved": unresolvedAttributions, "oldest_created_at": oldestAttributionAt},
			"rabbitmq":         gin.H{"configured": mqConfigured, "connected": mqConnected},
		},
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
