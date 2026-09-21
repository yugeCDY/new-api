package service

import (
	"context"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

type UsageAttribution struct {
	Provider  string
	Subject   string
	RequestID string
	Verified  bool
}

func RecordRelayFinalizedUsage(c *gin.Context, relayInfo *relaycommon.RelayInfo, quota, logID int) {
	if relayInfo == nil || quota < 0 || logID <= 0 {
		return
	}
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = relayInfo.RequestId
	}
	if requestID == "" {
		return
	}
	RecordFinalizedUsage(c, UsageLifecycleEvent{
		SourceType:       "relay",
		SourceKey:        requestID,
		UserID:           relayInfo.UserId,
		TokenID:          relayInfo.TokenId,
		RequestID:        requestID,
		LogID: logID,
		Quota:            quota,
		OccurredAt:       time.Now().Unix(),
	})
}

func RecordTaskFinalizedUsage(ctx context.Context, task *model.Task, taskResult *relaycommon.TaskInfo) {
	if task == nil || taskResult == nil || task.Status != model.TaskStatusSuccess || task.Quota < 0 || task.PrivateData.UsageLogID <= 0 {
		return
	}
	requestID := ""
	if task.PrivateData.Execution != nil {
		requestID = task.PrivateData.Execution.RequestID
	}
	RecordFinalizedUsageContext(ctx, UsageLifecycleEvent{
		SourceType:       "task",
		SourceKey:        task.TaskID,
		UserID:           task.UserId,
		TokenID:          task.PrivateData.TokenId,
		RequestID: requestID,
		LogID: task.PrivateData.UsageLogID,
		Quota:            task.Quota,
		OccurredAt:       time.Now().Unix(),
	})
}

type UsageLifecycleEvent struct {
	Kind             string
	SourceType       string
	SourceKey        string
	UserID           int
	TokenID          int
	RequestID        string
	LogID            int
	Quota            int
	OccurredAt       int64
	Attribution      *UsageAttribution
}

type UsageLifecycleObserver func(context.Context, UsageLifecycleEvent) error

const usageAttributionContextKey = "usage_attribution"

var usageObserverState struct {
	sync.RWMutex
	observer UsageLifecycleObserver
}

func RegisterUsageLifecycleObserver(observer UsageLifecycleObserver) {
	usageObserverState.Lock()
	usageObserverState.observer = observer
	usageObserverState.Unlock()
}

func SetUsageAttribution(c *gin.Context, attribution UsageAttribution) {
	c.Set(usageAttributionContextKey, attribution)
}

func RecordUsageAttribution(c *gin.Context, sourceType, sourceKey string, userID, tokenID int) {
	attribution, ok := usageAttributionFromContext(c)
	if !ok {
		return
	}
	emitUsageLifecycle(c.Request.Context(), UsageLifecycleEvent{
		Kind:        "attribution",
		SourceType:  sourceType,
		SourceKey:   sourceKey,
		UserID:      userID,
		TokenID:     tokenID,
		Attribution: &attribution,
	})
}

func RecordFinalizedUsage(c *gin.Context, event UsageLifecycleEvent) {
	if attribution, ok := usageAttributionFromContext(c); ok {
		event.Attribution = &attribution
	}
	event.Kind = "finalized"
	emitUsageLifecycle(c.Request.Context(), event)
}

func RecordFinalizedUsageContext(ctx context.Context, event UsageLifecycleEvent) {
	event.Kind = "finalized"
	emitUsageLifecycle(ctx, event)
}

func usageAttributionFromContext(c *gin.Context) (UsageAttribution, bool) {
	value, exists := c.Get(usageAttributionContextKey)
	if !exists {
		return UsageAttribution{}, false
	}
	attribution, ok := value.(UsageAttribution)
	return attribution, ok && attribution.Verified
}

func emitUsageLifecycle(ctx context.Context, event UsageLifecycleEvent) {
	usageObserverState.RLock()
	observer := usageObserverState.observer
	usageObserverState.RUnlock()
	if observer == nil {
		return
	}
	if err := observer(ctx, event); err != nil {
		logger.LogError(ctx, "failed to persist usage lifecycle notification: "+err.Error())
	}
}
