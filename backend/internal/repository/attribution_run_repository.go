package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"industrial-noise-source-attribution/backend/internal/model"
)

type AttributionRunRepository struct{ db *gorm.DB }

func NewAttributionRunRepository(db *gorm.DB) *AttributionRunRepository {
	return &AttributionRunRepository{db: db}
}

func (r *AttributionRunRepository) List(ctx context.Context) ([]model.AttributionRun, error) {
	var runs []model.AttributionRun
	if err := r.db.WithContext(ctx).Order("id DESC").Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list attribution runs: %w", err)
	}
	return runs, nil
}

func (r *AttributionRunRepository) Get(ctx context.Context, id uint) (model.AttributionRun, error) {
	return GetAttributionRunTx(r.db.WithContext(ctx), id)
}

func GetAttributionRunTx(tx *gorm.DB, id uint) (model.AttributionRun, error) {
	var run model.AttributionRun
	if err := tx.First(&run, id).Error; err != nil {
		return run, fmt.Errorf("get attribution run: %w", err)
	}
	return run, nil
}

// ListByMeasurementIDsTx 返回冻结了任一给定测量的全部归因运行（含已失效），
// 供级联失效在事务内判定匹配范围。
func ListAttributionRunsTx(tx *gorm.DB) ([]model.AttributionRun, error) {
	var runs []model.AttributionRun
	if err := tx.Order("id ASC").Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list attribution runs in tx: %w", err)
	}
	return runs, nil
}

// FindActiveByInput 返回同一 input_hash + algorithm_version 的有效（未失效）运行。
// 已失效结果不允许被幂等复用，重新计算必须生成新运行。
func (r *AttributionRunRepository) FindActiveByInput(ctx context.Context, hash, version string) (model.AttributionRun, error) {
	return FindActiveAttributionByInputTx(r.db.WithContext(ctx), hash, version)
}

func FindActiveAttributionByInputTx(tx *gorm.DB, hash, version string) (model.AttributionRun, error) {
	var run model.AttributionRun
	if err := tx.Where("input_hash = ? AND algorithm_version = ? AND attribution_state <> ?", hash, version, "invalidated").
		First(&run).Error; err != nil {
		return run, fmt.Errorf("find attribution input: %w", err)
	}
	return run, nil
}

// ListInvalidatedByInputTx 返回该输入的历史失效运行，供重算审计保留完整链路。
func ListInvalidatedByInputTx(tx *gorm.DB, hash, version string) ([]model.AttributionRun, error) {
	var runs []model.AttributionRun
	if err := tx.Where("input_hash = ? AND algorithm_version = ? AND attribution_state = ?", hash, version, "invalidated").
		Order("id ASC").Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list invalidated attribution inputs: %w", err)
	}
	return runs, nil
}

func (r *AttributionRunRepository) ListInvalidatedByInput(ctx context.Context, hash, version string) ([]model.AttributionRun, error) {
	return ListInvalidatedByInputTx(r.db.WithContext(ctx), hash, version)
}

// ListInvalidatedByFrozenIDs 返回冻结了同一组测量与声源 ID 的历史失效运行，
// 即使坐标/背景谱变化导致 input_hash 改变，也能把“失效 -> 重算”链路串起来。
func (r *AttributionRunRepository) ListInvalidatedByFrozenIDs(ctx context.Context, measurementIDsJSON, sourceProfileIDsJSON string) ([]model.AttributionRun, error) {
	var runs []model.AttributionRun
	if err := r.db.WithContext(ctx).
		Where("measurement_ids_json = ? AND source_profile_ids_json = ? AND attribution_state = ?",
			measurementIDsJSON, sourceProfileIDsJSON, "invalidated").
		Order("id ASC").Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list invalidated runs by frozen ids: %w", err)
	}
	return runs, nil
}

func (r *AttributionRunRepository) CountByInput(ctx context.Context, hash, version string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.AttributionRun{}).
		Where("input_hash = ? AND algorithm_version = ?", hash, version).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count attribution input runs: %w", err)
	}
	return count, nil
}

// CreateCalculatedTx 在调用方事务内完成 queued -> calculating -> completed 的条件迁移与审计写入。
func CreateCalculatedTx(tx *gorm.DB, run *model.AttributionRun, audit *model.AuditLog) error {
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
}

func (r *AttributionRunRepository) Transition(ctx context.Context, id, expectedVersion uint, from, to string, reviewedBy *uint, reviewNote string, audit *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return TransitionAttributionTx(tx, id, expectedVersion, from, to, reviewedBy, reviewNote, audit)
	})
}

// TransitionAttributionTx 在调用方事务内执行归因运行条件状态迁移。
func TransitionAttributionTx(tx *gorm.DB, id, expectedVersion uint, from, to string, reviewedBy *uint, reviewNote string, audit *model.AuditLog) error {
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
}

// InvalidateAttributionTx 将单个未确认运行条件更新为 invalidated，并写入失效审计。
// 条件包含当前状态必须属于可失效状态，保证已确认结果永不被覆盖。
func InvalidateAttributionTx(tx *gorm.DB, run model.AttributionRun, updates map[string]any, audit *model.AuditLog) error {
	result := tx.Model(&model.AttributionRun{}).
		Where("id = ? AND version = ? AND attribution_state IN ?",
			run.ID, run.Version, []string{"completed", "reviewed"}).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("invalidate attribution run: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrInvalidData
	}
	if err := tx.Create(audit).Error; err != nil {
		return fmt.Errorf("audit attribution invalidation: %w", err)
	}
	return nil
}
