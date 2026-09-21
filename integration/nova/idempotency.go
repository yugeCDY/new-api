package nova

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

var (
	errIdempotencyConflict = errors.New("request_id was already used for a different request")
	errIdempotencyRunning  = errors.New("idempotent operation is still processing")
)

type mutationResult struct {
	FreshResponse  any
	StoredResponse any
	ResourceRef    string
	AfterCommit    func()
}

type idempotentResponse struct {
	Status   int
	Body     []byte
	Replayed bool
}

func executeIdempotent(c *gin.Context, scope, requestID string, mutate func(*gorm.DB) (mutationResult, error)) (idempotentResponse, error) {
	if !requestIDPattern.MatchString(requestID) {
		return idempotentResponse{}, newAPIError(http.StatusBadRequest, "invalid_request", "request_id is invalid")
	}
	requestHash := c.GetString("nova_body_hash")
	_, db := currentState()

	var existing IdempotencyRecord
	err := db.Where("scope = ? AND idempotency_key = ?", scope, requestID).First(&existing).Error
	if err == nil {
		return replayIdempotent(existing, requestHash)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return idempotentResponse{}, err
	}

	var result mutationResult
	var body []byte
	err = db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		record := IdempotencyRecord{
			Scope:          scope,
			IdempotencyKey: requestID,
			RequestHash:    requestHash,
			Status:         "processing",
			ExpiresAt:      now + int64((24*time.Hour)/time.Second),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			return errIdempotencyRunning
		}

		var mutationErr error
		result, mutationErr = mutate(tx)
		if mutationErr != nil {
			return mutationErr
		}
		storedBody, err := common.Marshal(result.StoredResponse)
		if err != nil {
			return err
		}
		body, err = common.Marshal(result.FreshResponse)
		if err != nil {
			return err
		}
		return tx.Model(&record).Updates(map[string]any{
			"status":        "completed",
			"http_status":   http.StatusOK,
			"response_body": string(storedBody),
			"resource_ref":  result.ResourceRef,
			"updated_at":    time.Now().Unix(),
		}).Error
	})
	if errors.Is(err, errIdempotencyRunning) {
		var concurrent IdempotencyRecord
		if queryErr := db.Where("scope = ? AND idempotency_key = ?", scope, requestID).First(&concurrent).Error; queryErr != nil {
			return idempotentResponse{}, err
		}
		return replayIdempotent(concurrent, requestHash)
	}
	if err != nil {
		return idempotentResponse{}, err
	}
	if result.AfterCommit != nil {
		result.AfterCommit()
	}
	return idempotentResponse{Status: http.StatusOK, Body: body}, nil
}

func replayIdempotent(record IdempotencyRecord, requestHash string) (idempotentResponse, error) {
	if record.RequestHash != requestHash {
		return idempotentResponse{}, errIdempotencyConflict
	}
	if record.Status != "completed" {
		return idempotentResponse{}, errIdempotencyRunning
	}
	return idempotentResponse{
		Status:   record.HTTPStatus,
		Body:     []byte(record.ResponseBody),
		Replayed: true,
	}, nil
}
