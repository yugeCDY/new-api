package nova

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	redisEnabled := common.RedisEnabled
	redisOK := !redisEnabled || redisConnected()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "database is unavailable",
			"data": gin.H{
				"status":   "down",
				"database": gin.H{"connected": false},
				"redis":    gin.H{"enabled": redisEnabled, "connected": redisOK && redisEnabled},
				"mq":       gin.H{"connected": false, "outbox_pending": 0, "outbox_dead": 0},
			},
		})
		return
	}

	var pending, dead int64
	databaseOK := db.Raw("SELECT 1").Error == nil
	if databaseOK {
		if err := db.Model(&Outbox{}).Where("status = ?", "pending").Count(&pending).Error; err != nil {
			databaseOK = false
			pending = 0
		}
	}
	if databaseOK {
		if err := db.Model(&Outbox{}).Where("status = ?", "dead").Count(&dead).Error; err != nil {
			databaseOK = false
			dead = 0
		}
	}

	mqConfigured, mqConnected := publisherHealth()
	mqOK := mqConfigured && mqConnected
	// Nova requires the main DB and RabbitMQ. Redis is host middleware used by
	// token/quota paths; only degrade when it is enabled but unreachable.
	healthy := databaseOK && mqOK && redisOK
	status := "up"
	httpStatus := http.StatusOK
	message := "ok"
	if !databaseOK {
		status = "down"
		httpStatus = http.StatusServiceUnavailable
		message = "database is unavailable"
	} else if !healthy {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
		switch {
		case !mqOK:
			message = "rabbitmq is unavailable"
		case !redisOK:
			message = "redis is unavailable"
		default:
			message = "service is degraded"
		}
	}
	c.JSON(httpStatus, gin.H{
		"success": healthy,
		"message": message,
		"data": gin.H{
			"status": status,
			"database": gin.H{
				"connected": databaseOK,
			},
			"redis": gin.H{
				"enabled":   redisEnabled,
				"connected": redisEnabled && redisOK,
			},
			"mq": gin.H{
				"connected":      mqOK,
				"outbox_pending": pending,
				"outbox_dead":    dead,
			},
		},
	})
}

func redisConnected() bool {
	if !common.RedisEnabled || common.RDB == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return common.RDB.Ping(ctx).Err() == nil
}
