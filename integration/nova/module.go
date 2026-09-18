package nova

import (
	"context"
	"errors"
	"sync"

	"github.com/QuantumNous/new-api/service"
	"gorm.io/gorm"
)

var runtimeState struct {
	sync.RWMutex
	config Config
	db     *gorm.DB
}

func Initialize(db *gorm.DB) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	if config.Enabled && db == nil {
		return errors.New("Nova integration requires the main database")
	}
	if config.Enabled {
		if err := migrate(db); err != nil {
			return err
		}
	}

	runtimeState.Lock()
	runtimeState.config = config
	runtimeState.db = db
	runtimeState.Unlock()
	if config.Enabled {
		service.RegisterUsageLifecycleObserver(recordUsageLifecycle)
		startPublisher(config, db)
	}
	return nil
}

func Close() {
	service.RegisterUsageLifecycleObserver(nil)
	stopPublisher()
}

func recordUsageLifecycle(ctx context.Context, event service.UsageLifecycleEvent) error {
	if event.Kind == "attribution" {
		return recordAttribution(event)
	}
	if event.Kind == "finalized" {
		return recordFinalizedUsage(event)
	}
	return nil
}

func Enabled() bool {
	runtimeState.RLock()
	defer runtimeState.RUnlock()
	return runtimeState.config.Enabled
}

func currentState() (Config, *gorm.DB) {
	runtimeState.RLock()
	defer runtimeState.RUnlock()
	return runtimeState.config, runtimeState.db
}
