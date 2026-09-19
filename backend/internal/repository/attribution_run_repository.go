package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"industrial-noise-source-attribution/backend/internal/constants"
	"industrial-noise-source-attribution/backend/internal/model"
)

type AttributionRunRepository struct{ db *gorm.DB }

func NewAttributionRunRepository(db *gorm.DB) *AttributionRunRepository {
	return &AttributionRunRepository{db: db}
}

// Invalidation carries the frozen-input change that must invalidate every
// unconfirmed run referencing the trigger entity within the same transaction.
// When EntityType is MonitoringPoint, MeasurementIDs holds that point's
// measurements because runs freeze measurements rather than points directly.
type Invalidation struct {
	EntityType     string
	EntityID       uint
	EntityCode     string
	MeasurementIDs []uint
	Reason         string
	Actor          model.Actor
}

type invalidationHook struct{ spec *Invalidation }

// AsTransactionHook exposes the closed loop to other entity repositories so
// the triggering write and the run invalidations share one transaction.
func (r *AttributionRunRepository) AsTransactionHook(spec *Invalidation) func(*gorm.DB) error {
	if spec == nil {
		return nil
	}
	return (&invalidationHook{spec: spec}).apply
}

func (r *AttributionRunRepository) List(ctx context.Context) ([]model.AttributionRun, error) {
	var runs []model.AttributionRun
	if err := r.db.WithContext(ctx).Order("id DESC").Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list attribution runs: %w", err)
	}
	return runs, nil
}

func (r *AttributionRunRepository) Get(ctx context.Context, id uint) (model.AttributionRun, error) {
	var run model.AttributionRun
	if err := r.db.WithContext(ctx).First(&run, id).Error; err != nil {
		return run, fmt.Errorf("get attribution run: %w", err)
	}
	return run, nil
}

// FindByInput returns the reusable run for a frozen input. Invalidated runs
// are deliberately excluded: recomputation must never reuse an invalidated
// conclusion and must insert a fresh run under the partial unique index.
func (r *AttributionRunRepository) FindByInput(ctx context.Context, hash, version string) (model.AttributionRun, error) {
	var run model.AttributionRun
	if err := r.db.WithContext(ctx).
		Where("input_hash = ? AND algorithm_version = ? AND attribution_state <> ?", hash, version, string(constants.AttributionInvalidated)).
		First(&run).Error; err != nil {
		return run, fmt.Errorf("find attribution input: %w", err)
	}
	return run, nil
}

func (r *AttributionRunRepository) CreateCalculated(ctx context.Context, run *model.AttributionRun, audit *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		finalState := run.AttributionState
		finishedAt := run.FinishedAt
		run.AttributionState = "queued"
		run.FinishedAt = nil
		if err := tx.Create(run).Error; err != nil {
			return fmt.Errorf("create queued attribution run: %w", err)
		}
		calculating := tx.Model(&model.AttributionRun{}).Where("id = ? AND attribution_state = ?", run.ID, "queued").
			Updates(map[string]any{"attribution_state": "calculating", "version": gorm.Expr("version + 1")})
		if calculating.Error != nil || calculating.RowsAffected != 1 {
			return fmt.Errorf("start attribution calculation: %w", calculating.Error)
		}
		completed := tx.Model(&model.AttributionRun{}).Where("id = ? AND attribution_state = ?", run.ID, "calculating").
			Updates(map[string]any{
				"attribution_state": finalState, "finished_at": finishedAt,
				"normalized_bands_json": run.NormalizedBandsJSON, "contributions_json": run.ContributionsJSON,
				"evidence_json": run.EvidenceJSON, "residual_error": run.ResidualError,
				"explanation": run.Explanation, "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
			})
		if completed.Error != nil || completed.RowsAffected != 1 {
			return fmt.Errorf("complete attribution calculation: %w", completed.Error)
		}
		run.AttributionState = finalState
		run.FinishedAt = finishedAt
		run.Version += 2
		audit.EntityID = run.ID
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("audit attribution calculation: %w", err)
		}
		return nil
	})
}

func (r *AttributionRunRepository) Transition(ctx context.Context, id, expectedVersion uint, from, to string, reviewedBy *uint, reviewNote string, audit *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"attribution_state": to, "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
		}
		if reviewedBy != nil {
			updates["reviewed_by"] = *reviewedBy
		}
		if reviewNote != "" {
			updates["review_note"] = reviewNote
		}
		result := tx.Model(&model.AttributionRun{}).
			Where("id = ? AND version = ? AND attribution_state = ?", id, expectedVersion, from).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("transition attribution run: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return gorm.ErrInvalidData
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("audit attribution transition: %w", err)
		}
		return nil
	})
}

// apply invalidates every unconfirmed run referencing the trigger entity, in
// the caller's transaction. Confirmed, failed, voided and already-invalidated
// runs are left untouched. Each affected run gets its own audit record in the
// same transaction, so the chain from the triggering write to every affected
// conclusion is complete and atomic.
func (h *invalidationHook) apply(tx *gorm.DB) error {
	if h.spec.EntityType == constants.InvalidationEntityMonitoringPoint && h.spec.MeasurementIDs == nil {
		if err := tx.Model(&model.NoiseMeasurement{}).
			Where("monitoring_point_id = ?", h.spec.EntityID).
			Pluck("id", &h.spec.MeasurementIDs).Error; err != nil {
			return fmt.Errorf("load point measurements for invalidation: %w", err)
		}
	}
	var runs []model.AttributionRun
	if err := tx.Where("attribution_state IN ?", constants.InvalidationUnconfirmedStates).
		Find(&runs).Error; err != nil {
		return fmt.Errorf("load invalidation candidates: %w", err)
	}
	targets := make([]model.AttributionRun, 0, len(runs))
	for _, run := range runs {
		matches, err := h.matches(run)
		if err != nil {
			return err
		}
		if matches {
			targets = append(targets, run)
		}
	}
	now := time.Now().UTC()
	for index := range targets {
		run := targets[index]
		before := run
		result := tx.Model(&model.AttributionRun{}).
			Where("id = ? AND attribution_state IN ?", run.ID, constants.InvalidationUnconfirmedStates).
			Updates(map[string]any{
				"attribution_state":   string(constants.AttributionInvalidated),
				"invalidated_at":      now,
				"invalidation_reason": h.spec.Reason,
				"trigger_entity_type": h.spec.EntityType,
				"trigger_entity_id":   h.spec.EntityID,
				"trigger_entity_code": h.spec.EntityCode,
				"invalidated_by":      h.spec.Actor.ID,
				"version":             gorm.Expr("version + 1"),
				"updated_at":          now,
			})
		if result.Error != nil {
			return fmt.Errorf("invalidate attribution run %d: %w", run.ID, result.Error)
		}
		if result.RowsAffected != 1 {
			// The run left an invalidatable state between selection and update.
			return gorm.ErrInvalidData
		}
		after := before
		after.AttributionState = string(constants.AttributionInvalidated)
		after.InvalidatedAt = &now
		after.InvalidationReason = h.spec.Reason
		after.TriggerEntityType = h.spec.EntityType
		after.TriggerEntityID = h.spec.EntityID
		after.TriggerEntityCode = h.spec.EntityCode
		after.InvalidatedBy = h.spec.Actor.ID
		after.Version++
		audit := &model.AuditLog{
			RequestID: h.spec.Actor.RequestID, ActorID: h.spec.Actor.ID, ActorName: h.spec.Actor.DisplayName,
			Action: "attribution_run.invalidated", EntityType: "AttributionRun", EntityID: run.ID,
			BeforeJSON: mustAuditJSON(before), AfterJSON: mustAuditJSON(after),
			MetadataJSON: mustAuditJSON(map[string]any{
				"reason": h.spec.Reason, "trigger_entity_type": h.spec.EntityType,
				"trigger_entity_id": h.spec.EntityID, "trigger_entity_code": h.spec.EntityCode,
				"cascade_index": index, "cascade_total": len(targets),
			}),
			CreatedAt: now,
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("audit attribution invalidation: %w", err)
		}
	}
	return nil
}

func (h *invalidationHook) matches(run model.AttributionRun) (bool, error) {
	var measurementIDs, sourceIDs []uint
	if err := json.Unmarshal([]byte(run.MeasurementIDsJSON), &measurementIDs); err != nil {
		return false, fmt.Errorf("decode frozen measurement ids: %w", err)
	}
	if err := json.Unmarshal([]byte(run.SourceProfileIDsJSON), &sourceIDs); err != nil {
		return false, fmt.Errorf("decode frozen source ids: %w", err)
	}
	switch h.spec.EntityType {
	case constants.InvalidationEntitySourceProfile:
		return containsID(sourceIDs, h.spec.EntityID), nil
	case constants.InvalidationEntityNoiseMeasurement:
		return containsID(measurementIDs, h.spec.EntityID), nil
	case constants.InvalidationEntityMonitoringPoint:
		return containsAnyID(measurementIDs, h.spec.MeasurementIDs), nil
	default:
		return false, nil
	}
}

func containsID(ids []uint, target uint) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func containsAnyID(ids, targets []uint) bool {
	set := make(map[uint]struct{}, len(targets))
	for _, id := range targets {
		set[id] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := set[id]; ok {
			return true
		}
	}
	return false
}

func mustAuditJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(payload)
}
