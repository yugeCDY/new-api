package nova

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const outboxLeaseDuration = 30 * time.Second

var publisherState struct {
	sync.Mutex
	cancel        context.CancelFunc
	done          chan struct{}
	conn          *amqp.Connection
	channel       *amqp.Channel
	confirms      chan amqp.Confirmation
	healthy       atomic.Bool
	failureLogged atomic.Bool
}

func startPublisher(config Config, db *gorm.DB) {
	if config.RabbitMQURL == "" || db == nil {
		return
	}
	stopPublisher()
	ctx, cancel := context.WithCancel(context.Background())
	publisherState.Lock()
	publisherState.cancel = cancel
	publisherState.done = make(chan struct{})
	done := publisherState.done
	publisherState.Unlock()
	go runPublisher(ctx, done, config, db)
}

func stopPublisher() {
	publisherState.Lock()
	cancel := publisherState.cancel
	done := publisherState.done
	publisherState.cancel = nil
	publisherState.done = nil
	publisherState.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	closePublisherConnection()
}

func runPublisher(ctx context.Context, done chan struct{}, config Config, db *gorm.DB) {
	defer close(done)
	ticker := time.NewTicker(config.PollInterval)
	defer ticker.Stop()
	nextMaintenance := time.Time{}
	for {
		if _, _, err := publisherChannel(config); err != nil {
			publisherState.healthy.Store(false)
			logPublisherFailure(ctx, "Nova RabbitMQ connection failed; usage events will remain in outbox")
		} else {
			publisherState.failureLogged.Store(false)
		}
		if time.Now().After(nextMaintenance) {
			if err := cleanupExpiredRecords(db, config, time.Now()); err != nil {
				logger.LogWarn(ctx, "Nova integration retention cleanup failed")
			}
			if err := reconcileMissingOutbox(db, config, time.Now()); err != nil {
				logger.LogWarn(ctx, "Nova usage outbox reconciliation failed")
			}
			nextMaintenance = time.Now().Add(time.Hour)
		}
		publishOutboxBatch(ctx, config, db)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func reconcileMissingOutbox(db *gorm.DB, config Config, now time.Time) error {
	var refs []LogRef
	if err := db.Table("nova_log_ref").
		Select("nova_log_ref.*").
		Joins("LEFT JOIN nova_outbox ON nova_outbox.event_id = nova_log_ref.event_id").
		Where("nova_outbox.id IS NULL").
		Order("nova_log_ref.id ASC").
		Limit(config.BatchSize).
		Find(&refs).Error; err != nil {
		return err
	}
	for _, ref := range refs {
		payload, err := marshalUsageMessage(ref)
		if err != nil {
			return err
		}
		outbox := Outbox{
			EventID: ref.EventID, ExchangeName: config.Exchange, RoutingKey: config.RoutingKey,
			Payload: string(payload), Status: "pending", NextAttemptAt: now.Unix(),
			CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox).Error; err != nil {
			return err
		}
	}
	return nil
}

func cleanupExpiredRecords(db *gorm.DB, config Config, now time.Time) error {
	nowUnix := now.Unix()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("expires_at < ?", nowUnix).Delete(&ReplayNonce{}).Error; err != nil {
			return err
		}
		if err := tx.Where("expires_at < ?", nowUnix).Delete(&IdempotencyRecord{}).Error; err != nil {
			return err
		}
		if config.OutboxRetention > 0 {
			publishedBefore := now.Add(-config.OutboxRetention).Unix()
			if err := tx.Where("status = ? AND published_at > 0 AND published_at < ?", "published", publishedBefore).Delete(&Outbox{}).Error; err != nil {
				return err
			}
		}
		if config.DeadRetention > 0 {
			deadBefore := now.Add(-config.DeadRetention).Unix()
			if err := tx.Where("status = ? AND updated_at < ?", "dead", deadBefore).Delete(&Outbox{}).Error; err != nil {
				return err
			}
		}
		if config.EventRetention > 0 {
			eventsBefore := now.Add(-config.EventRetention).Unix()
			if err := tx.Where("occurred_at < ?", eventsBefore).Delete(&LogRef{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func publishOutboxBatch(ctx context.Context, config Config, db *gorm.DB) {
	now := time.Now().Unix()
	var candidates []Outbox
	err := db.Where("(status = ? AND next_attempt_at <= ?) OR (status = ? AND locked_until < ?)", "pending", now, "publishing", now).
		Order("id ASC").Limit(config.BatchSize).Find(&candidates).Error
	if err != nil {
		logPublisherFailure(ctx, "Nova outbox query failed")
		return
	}
	workerID := uuid.NewString()
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		claim := db.Model(&Outbox{}).
			Where("id = ? AND ((status = ? AND next_attempt_at <= ?) OR (status = ? AND locked_until < ?))", candidate.ID, "pending", now, "publishing", now).
			Updates(map[string]any{"status": "publishing", "locked_by": workerID, "locked_until": now + int64(outboxLeaseDuration/time.Second), "updated_at": now})
		if claim.Error != nil || claim.RowsAffected != 1 {
			continue
		}

		err := publishMessage(ctx, config, candidate)
		if err == nil {
			publisherState.healthy.Store(true)
			publisherState.failureLogged.Store(false)
			_ = db.Model(&Outbox{}).Where("id = ? AND locked_by = ?", candidate.ID, workerID).Updates(map[string]any{
				"status":       "published",
				"published_at": time.Now().Unix(),
				"locked_by":    "",
				"locked_until": 0,
				"last_error":   "",
				"updated_at":   time.Now().Unix(),
			}).Error
			continue
		}

		publisherState.healthy.Store(false)
		logPublisherFailure(ctx, "Nova RabbitMQ publish failed; event remains in outbox")
		attempts := candidate.Attempts + 1
		status := "pending"
		if attempts >= config.MaxAttempts {
			status = "dead"
		}
		backoff := min(1<<min(attempts, 8), 300)
		jitterWindow := max(backoff/2, 1)
		backoff += int(candidate.ID % int64(jitterWindow))
		_ = db.Model(&Outbox{}).Where("id = ? AND locked_by = ?", candidate.ID, workerID).Updates(map[string]any{
			"status":          status,
			"attempts":        attempts,
			"next_attempt_at": time.Now().Unix() + int64(backoff),
			"locked_by":       "",
			"locked_until":    0,
			"last_error":      "RabbitMQ publish failed",
			"updated_at":      time.Now().Unix(),
		}).Error
	}
}

func publishMessage(ctx context.Context, config Config, outbox Outbox) error {
	channel, confirmations, err := publisherChannel(config)
	if err != nil {
		return err
	}
	publishTimeout := config.PublishTimeout
	if publishTimeout <= 0 {
		publishTimeout = 5 * time.Second
	}
	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	err = channel.PublishWithContext(publishCtx, outbox.ExchangeName, outbox.RoutingKey, false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		ContentType:  "application/json",
		MessageId:    outbox.EventID,
		Timestamp:    time.Now().UTC(),
		Body:         []byte(outbox.Payload),
	})
	if err != nil {
		closePublisherConnection()
		return errors.New("RabbitMQ publish failed")
	}
	select {
	case confirmation, ok := <-confirmations:
		if !ok || !confirmation.Ack {
			closePublisherConnection()
			return errors.New("RabbitMQ publish was not acknowledged")
		}
		return nil
	case <-publishCtx.Done():
		closePublisherConnection()
		return errors.New("RabbitMQ publish confirmation timed out")
	}
}

func publisherChannel(config Config) (*amqp.Channel, <-chan amqp.Confirmation, error) {
	publisherState.Lock()
	defer publisherState.Unlock()
	if publisherState.conn != nil && !publisherState.conn.IsClosed() && publisherState.channel != nil && !publisherState.channel.IsClosed() {
		return publisherState.channel, publisherState.confirms, nil
	}

	conn, err := amqp.Dial(config.RabbitMQURL)
	if err != nil {
		return nil, nil, errors.New("RabbitMQ connection failed")
	}
	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, errors.New("RabbitMQ channel creation failed")
	}
	if err := channel.ExchangeDeclare(config.Exchange, "topic", true, false, false, false, nil); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return nil, nil, errors.New("RabbitMQ exchange declaration failed")
	}
	if err := channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return nil, nil, errors.New("RabbitMQ publisher confirm setup failed")
	}
	confirmations := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	publisherState.conn = conn
	publisherState.channel = channel
	publisherState.confirms = confirmations
	publisherState.healthy.Store(true)
	publisherState.failureLogged.Store(false)
	return channel, confirmations, nil
}

func closePublisherConnection() {
	publisherState.Lock()
	defer publisherState.Unlock()
	if publisherState.channel != nil {
		_ = publisherState.channel.Close()
	}
	if publisherState.conn != nil {
		_ = publisherState.conn.Close()
	}
	publisherState.channel = nil
	publisherState.conn = nil
	publisherState.confirms = nil
	publisherState.healthy.Store(false)
	publisherState.failureLogged.Store(false)
}

func publisherHealth() (configured, connected bool) {
	config, _ := currentState()
	return config.RabbitMQURL != "", publisherState.healthy.Load()
}

func logPublisherFailure(ctx context.Context, message string) {
	if publisherState.failureLogged.CompareAndSwap(false, true) {
		logger.LogWarn(ctx, message)
	}
}
