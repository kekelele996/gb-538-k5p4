package service

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"industrial-noise-source-attribution/backend/internal/constants"
	"industrial-noise-source-attribution/backend/internal/model"
	"industrial-noise-source-attribution/backend/internal/repository"
)

// FrozenInputInvalidationService 在触发实体所在的同一事务内，
// 把引用了变化冻结输入的未确认归因运行级联失效，并为每条运行写入失效审计。
type FrozenInputInvalidationService struct {
	transactor *repository.Transactor
}

func NewFrozenInputInvalidationService(transactor *repository.Transactor) *FrozenInputInvalidationService {
	return &FrozenInputInvalidationService{transactor: transactor}
}

// InvalidationScope 描述一次失效闭环的触发来源与原因。
type InvalidationScope struct {
	ReasonCode constants.InvalidationReasonCode
	Reason     string
	EntityType string
	EntityID   uint
	EntityCode string
}

// InvalidateForPointTx 供监测点坐标/背景谱更新、停用调用：
// 先把监测点展开为其下全部测量，再匹配冻结了这些测量的未确认运行。
// 返回被失效的归因运行 ID 列表，供触发实体审计记录同事务影响范围。
func (s *FrozenInputInvalidationService) InvalidateForPointTx(tx *gorm.DB, pointID uint, scope InvalidationScope, actor model.Actor) ([]uint, error) {
	measurementIDs, err := repository.MeasurementIDsByPointTx(tx, pointID)
	if err != nil {
		return nil, err
	}
	return invalidateForMeasurementIDsTx(tx, measurementIDs, scope, actor)
}

// InvalidateForMeasurementTx 供测量被替代（ready -> superseded）调用。
func (s *FrozenInputInvalidationService) InvalidateForMeasurementTx(tx *gorm.DB, measurementID uint, scope InvalidationScope, actor model.Actor) ([]uint, error) {
	return invalidateForMeasurementIDsTx(tx, []uint{measurementID}, scope, actor)
}

// InvalidateForSourceTx 供冻结声源谱版本停用（active -> retired）调用。
func (s *FrozenInputInvalidationService) InvalidateForSourceTx(tx *gorm.DB, sourceProfileID uint, scope InvalidationScope, actor model.Actor) ([]uint, error) {
	runs, err := repository.ListAttributionRunsTx(tx)
	if err != nil {
		return nil, err
	}
	return invalidateRunsTx(tx, runs, func(run model.AttributionRun) bool {
		sourceIDs, decodeErr := decodeUintList(run.SourceProfileIDsJSON)
		return decodeErr == nil && containsUint(sourceIDs, sourceProfileID)
	}, scope, actor)
}

func invalidateForMeasurementIDsTx(tx *gorm.DB, measurementIDs []uint, scope InvalidationScope, actor model.Actor) ([]uint, error) {
	if len(measurementIDs) == 0 {
		return []uint{}, nil
	}
	runs, err := repository.ListAttributionRunsTx(tx)
	if err != nil {
		return nil, err
	}
	return invalidateRunsTx(tx, runs, func(run model.AttributionRun) bool {
		frozenIDs, decodeErr := decodeUintList(run.MeasurementIDsJSON)
		if decodeErr != nil {
			return false
		}
		for _, id := range measurementIDs {
			if containsUint(frozenIDs, id) {
				return true
			}
		}
		return false
	}, scope, actor)
}

func invalidateRunsTx(tx *gorm.DB, runs []model.AttributionRun, match func(model.AttributionRun) bool, scope InvalidationScope, actor model.Actor) ([]uint, error) {
	invalidatedAt := time.Now().UTC()
	invalidatedIDs := make([]uint, 0)
	for _, run := range runs {
		state := constants.AttributionState(run.AttributionState)
		if !constants.CanInvalidateAttribution(state) || !match(run) {
			continue
		}
		after := run
		after.AttributionState = string(constants.AttributionInvalidated)
		after.InvalidationReasonCode = string(scope.ReasonCode)
		after.InvalidationReason = scope.Reason
		after.InvalidationEntityType = scope.EntityType
		after.InvalidationEntityID = scope.EntityID
		after.InvalidationEntityCode = scope.EntityCode
		after.InvalidatedBy = actor.ID
		after.InvalidatedByName = actor.DisplayName
		after.InvalidatedAt = &invalidatedAt
		after.Version = run.Version + 1
		audit := newAudit(actor, "attribution_run.invalidated", "AttributionRun", run.ID, run, after, map[string]any{
			"from":                run.AttributionState,
			"to":                  constants.AttributionInvalidated,
			"reason_code":         scope.ReasonCode,
			"reason":              scope.Reason,
			"trigger_entity_type": scope.EntityType,
			"trigger_entity_id":   scope.EntityID,
			"trigger_entity_code": scope.EntityCode,
			"triggered_at":        invalidatedAt,
			"expected_version":    run.Version,
		})
		updates := map[string]any{
			"attribution_state":        constants.AttributionInvalidated,
			"invalidation_reason_code": scope.ReasonCode,
			"invalidation_reason":      scope.Reason,
			"invalidation_entity_type": scope.EntityType,
			"invalidation_entity_id":   scope.EntityID,
			"invalidation_entity_code": scope.EntityCode,
			"invalidated_by":           actor.ID,
			"invalidated_by_name":      actor.DisplayName,
			"invalidated_at":           invalidatedAt,
			"version":                  gorm.Expr("version + 1"),
			"updated_at":               invalidatedAt,
		}
		if err := repository.InvalidateAttributionTx(tx, run, updates, audit); err != nil {
			return invalidatedIDs, err
		}
		invalidatedIDs = append(invalidatedIDs, run.ID)
	}
	return invalidatedIDs, nil
}

func decodeUintList(raw string) ([]uint, error) {
	var ids []uint
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func containsUint(values []uint, target uint) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
