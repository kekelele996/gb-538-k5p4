package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"industrial-noise-source-attribution/backend/internal/model"
)

type PointSummary struct {
	MeasurementCount      int64
	LatestQuality         string
	LatestMeasurementTime *time.Time
}

type MonitoringPointRepository struct{ db *gorm.DB }

func NewMonitoringPointRepository(db *gorm.DB) *MonitoringPointRepository {
	return &MonitoringPointRepository{db: db}
}

func (r *MonitoringPointRepository) List(ctx context.Context) ([]model.MonitoringPoint, error) {
	var points []model.MonitoringPoint
	if err := r.db.WithContext(ctx).Order("point_code ASC").Find(&points).Error; err != nil {
		return nil, fmt.Errorf("list monitoring points: %w", err)
	}
	return points, nil
}

func (r *MonitoringPointRepository) Get(ctx context.Context, id uint) (model.MonitoringPoint, error) {
	var point model.MonitoringPoint
	if err := r.db.WithContext(ctx).First(&point, id).Error; err != nil {
		return point, fmt.Errorf("get monitoring point: %w", err)
	}
	return point, nil
}

func (r *MonitoringPointRepository) Summary(ctx context.Context, id uint) (PointSummary, error) {
	var summary PointSummary
	if err := r.db.WithContext(ctx).Model(&model.NoiseMeasurement{}).
		Where("monitoring_point_id = ?", id).Count(&summary.MeasurementCount).Error; err != nil {
		return summary, fmt.Errorf("count point measurements: %w", err)
	}
	var latest model.NoiseMeasurement
	err := r.db.WithContext(ctx).Where("monitoring_point_id = ?", id).
		Order("measured_at DESC").First(&latest).Error
	if err == nil {
		summary.LatestQuality = latest.MeasurementQuality
		summary.LatestMeasurementTime = &latest.MeasuredAt
	} else if err != gorm.ErrRecordNotFound {
		return summary, fmt.Errorf("load latest point measurement: %w", err)
	}
	return summary, nil
}

func (r *MonitoringPointRepository) Create(ctx context.Context, point *model.MonitoringPoint, audit *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(point).Error; err != nil {
			return fmt.Errorf("create monitoring point: %w", err)
		}
		audit.EntityID = point.ID
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("audit monitoring point creation: %w", err)
		}
		return nil
	})
}

func (r *MonitoringPointRepository) Update(ctx context.Context, point *model.MonitoringPoint, expectedVersion uint, audit *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return UpdatePointTx(tx, point, expectedVersion, audit)
	})
}

// UpdatePointTx 在调用方事务内条件更新监测点（仅 active），并写入审计。
func UpdatePointTx(tx *gorm.DB, point *model.MonitoringPoint, expectedVersion uint, audit *model.AuditLog) error {
	if err := UpdatePointOnlyTx(tx, point, expectedVersion); err != nil {
		return err
	}
	if err := tx.Create(audit).Error; err != nil {
		return fmt.Errorf("audit monitoring point update: %w", err)
	}
	return nil
}

// UpdatePointOnlyTx 仅做监测点条件更新，审计由调用方在同事务内补充写入，
// 以便审计元数据携带同事务级联失效的归因运行 ID。
func UpdatePointOnlyTx(tx *gorm.DB, point *model.MonitoringPoint, expectedVersion uint) error {
	result := tx.Model(&model.MonitoringPoint{}).
		Where("id = ? AND version = ? AND point_state = ?", point.ID, expectedVersion, "active").
		Updates(map[string]any{
			"name": point.Name, "xm": point.XM, "ym": point.YM, "height_m": point.HeightM,
			"area_type": point.AreaType, "background_profile_json": point.BackgroundProfileJSON,
			"owner_team": point.OwnerTeam, "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("update monitoring point: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrInvalidData
	}
	return nil
}

func (r *MonitoringPointRepository) Deactivate(ctx context.Context, id, expectedVersion uint, audit *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return DeactivatePointTx(tx, id, expectedVersion, audit)
	})
}

// DeactivatePointTx 在调用方事务内条件停用监测点并写入审计。
func DeactivatePointTx(tx *gorm.DB, id, expectedVersion uint, audit *model.AuditLog) error {
	if err := DeactivatePointOnlyTx(tx, id, expectedVersion); err != nil {
		return err
	}
	if err := tx.Create(audit).Error; err != nil {
		return fmt.Errorf("audit monitoring point deactivation: %w", err)
	}
	return nil
}

// DeactivatePointOnlyTx 仅做监测点条件停用，审计由调用方在同事务内补充写入。
func DeactivatePointOnlyTx(tx *gorm.DB, id, expectedVersion uint) error {
	result := tx.Model(&model.MonitoringPoint{}).
		Where("id = ? AND version = ? AND point_state = ?", id, expectedVersion, "active").
		Updates(map[string]any{"point_state": "inactive", "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("deactivate monitoring point: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrInvalidData
	}
	return nil
}
