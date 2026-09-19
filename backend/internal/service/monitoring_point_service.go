package service

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"industrial-noise-source-attribution/backend/internal/algorithm"
	"industrial-noise-source-attribution/backend/internal/constants"
	"industrial-noise-source-attribution/backend/internal/dto"
	"industrial-noise-source-attribution/backend/internal/model"
	"industrial-noise-source-attribution/backend/internal/repository"
	"industrial-noise-source-attribution/backend/internal/util"
)

type MonitoringPointService struct {
	repository   *repository.MonitoringPointRepository
	transactor   *repository.Transactor
	invalidation *FrozenInputInvalidationService
}

func NewMonitoringPointService(repo *repository.MonitoringPointRepository, transactor *repository.Transactor, invalidation *FrozenInputInvalidationService) *MonitoringPointService {
	return &MonitoringPointService{repository: repo, transactor: transactor, invalidation: invalidation}
}

func (s *MonitoringPointService) List(ctx context.Context) ([]dto.MonitoringPointResponse, error) {
	points, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]dto.MonitoringPointResponse, 0, len(points))
	for _, point := range points {
		response, err := s.toResponse(ctx, point)
		if err != nil {
			return nil, err
		}
		result = append(result, response)
	}
	return result, nil
}

func (s *MonitoringPointService) Get(ctx context.Context, id uint) (dto.MonitoringPointResponse, error) {
	point, err := s.repository.Get(ctx, id)
	if err != nil {
		return dto.MonitoringPointResponse{}, mapRepositoryError(err, "监测点不存在", "监测点读取冲突")
	}
	return s.toResponse(ctx, point)
}

func (s *MonitoringPointService) Create(ctx context.Context, request dto.CreateMonitoringPointRequest, actor model.Actor) (dto.MonitoringPointResponse, error) {
	if err := algorithm.ValidateSpectrum(request.BackgroundProfile, "background profile"); err != nil {
		return dto.MonitoringPointResponse{}, util.Validation("背景谱必须包含完整且有效的 8 个倍频程", err)
	}
	now := time.Now().UTC()
	point := model.MonitoringPoint{
		PointCode: request.PointCode, Name: request.Name, XM: request.XM, YM: request.YM,
		HeightM: request.HeightM, AreaType: request.AreaType,
		BackgroundProfileJSON: mustJSON(request.BackgroundProfile), OwnerTeam: request.OwnerTeam,
		PointState: string(constants.PointActive), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	audit := newAudit(actor, "monitoring_point.created", "MonitoringPoint", 0, map[string]any{}, point, map[string]any{"background_bands": len(request.BackgroundProfile)})
	if err := s.repository.Create(ctx, &point, audit); err != nil {
		return dto.MonitoringPointResponse{}, mapRepositoryError(err, "监测点不存在", "监测点编号已存在")
	}
	return s.toResponse(ctx, point)
}

func (s *MonitoringPointService) Update(ctx context.Context, id uint, request dto.UpdateMonitoringPointRequest, actor model.Actor) (dto.MonitoringPointResponse, error) {
	if err := algorithm.ValidateSpectrum(request.BackgroundProfile, "background profile"); err != nil {
		return dto.MonitoringPointResponse{}, util.Validation("背景谱必须包含完整且有效的 8 个倍频程", err)
	}
	before, err := s.repository.Get(ctx, id)
	if err != nil {
		return dto.MonitoringPointResponse{}, mapRepositoryError(err, "监测点不存在", "监测点读取冲突")
	}
	if before.PointState != string(constants.PointActive) {
		return dto.MonitoringPointResponse{}, util.Conflict("已停用监测点不可编辑", nil)
	}
	coordinatesChanged := before.XM != request.XM || before.YM != request.YM || before.HeightM != request.HeightM
	newBackgroundJSON := mustJSON(request.BackgroundProfile)
	backgroundChanged := before.BackgroundProfileJSON != newBackgroundJSON
	// 坐标与背景谱同时变化时合并为单一失效原因，保证同事务内只失效一次。
	invalidationCode := constants.InvalidationReasonCode("")
	switch {
	case coordinatesChanged && backgroundChanged:
		invalidationCode = constants.InvalidationReasonPointInputsChanged
	case coordinatesChanged:
		invalidationCode = constants.InvalidationReasonPointCoordinates
	case backgroundChanged:
		invalidationCode = constants.InvalidationReasonPointBackground
	}
	after := before
	after.Name, after.XM, after.YM, after.HeightM = request.Name, request.XM, request.YM, request.HeightM
	after.AreaType, after.OwnerTeam = request.AreaType, request.OwnerTeam
	after.BackgroundProfileJSON = newBackgroundJSON
	after.Version = request.Version + 1
	err = s.transactor.InTx(ctx, func(tx *gorm.DB) error {
		if txErr := repository.UpdatePointOnlyTx(tx, &after, request.Version); txErr != nil {
			return txErr
		}
		invalidatedRunIDs := []uint{}
		if invalidationCode != "" {
			scope := InvalidationScope{
				ReasonCode: invalidationCode,
				Reason:     constants.InvalidationReasonMessage(invalidationCode),
				EntityType: constants.InvalidationEntityPoint, EntityID: id, EntityCode: before.PointCode,
			}
			ids, invErr := s.invalidation.InvalidateForPointTx(tx, id, scope, actor)
			if invErr != nil {
				return invErr
			}
			invalidatedRunIDs = ids
		}
		// 触发实体审计在同事务最后写入，元数据携带级联失效运行 ID，形成双向链路。
		audit := newAudit(actor, "monitoring_point.updated", "MonitoringPoint", id, before, after, map[string]any{
			"expected_version":         request.Version,
			"coordinates_changed":      coordinatesChanged,
			"background_changed":       backgroundChanged,
			"invalidation_reason_code": stringOrEmpty(invalidationCode),
			"invalidated_run_ids":      invalidatedRunIDs,
		})
		if txErr := tx.Create(audit).Error; txErr != nil {
			return fmt.Errorf("audit monitoring point update: %w", txErr)
		}
		return nil
	})
	if err != nil {
		return dto.MonitoringPointResponse{}, mapRepositoryError(err, "监测点不存在", "监测点已被其他操作更新，请刷新后重试")
	}
	return s.Get(ctx, id)
}

func stringOrEmpty(code constants.InvalidationReasonCode) string {
	if code == "" {
		return ""
	}
	return string(code)
}

func (s *MonitoringPointService) Deactivate(ctx context.Context, id, version uint, actor model.Actor) (dto.MonitoringPointResponse, error) {
	before, err := s.repository.Get(ctx, id)
	if err != nil {
		return dto.MonitoringPointResponse{}, mapRepositoryError(err, "监测点不存在", "监测点读取冲突")
	}
	after := before
	after.PointState = string(constants.PointInactive)
	after.Version = version + 1
	err = s.transactor.InTx(ctx, func(tx *gorm.DB) error {
		if txErr := repository.DeactivatePointOnlyTx(tx, id, version); txErr != nil {
			return txErr
		}
		scope := InvalidationScope{
			ReasonCode: constants.InvalidationReasonPointDeactivated,
			Reason:     constants.InvalidationReasonMessage(constants.InvalidationReasonPointDeactivated),
			EntityType: constants.InvalidationEntityPoint, EntityID: id, EntityCode: before.PointCode,
		}
		invalidatedRunIDs, invErr := s.invalidation.InvalidateForPointTx(tx, id, scope, actor)
		if invErr != nil {
			return invErr
		}
		audit := newAudit(actor, "monitoring_point.deactivated", "MonitoringPoint", id, before, after, map[string]any{
			"expected_version":         version,
			"invalidation_reason_code": string(constants.InvalidationReasonPointDeactivated),
			"invalidated_run_ids":      invalidatedRunIDs,
		})
		if txErr := tx.Create(audit).Error; txErr != nil {
			return fmt.Errorf("audit monitoring point deactivation: %w", txErr)
		}
		return nil
	})
	if err != nil {
		return dto.MonitoringPointResponse{}, mapRepositoryError(err, "监测点不存在", "监测点状态或版本已变化")
	}
	return s.Get(ctx, id)
}

func (s *MonitoringPointService) toResponse(ctx context.Context, point model.MonitoringPoint) (dto.MonitoringPointResponse, error) {
	background, err := decodeSpectrum(point.BackgroundProfileJSON)
	if err != nil {
		return dto.MonitoringPointResponse{}, err
	}
	summary, err := s.repository.Summary(ctx, point.ID)
	if err != nil {
		return dto.MonitoringPointResponse{}, fmt.Errorf("load monitoring point summary: %w", err)
	}
	return dto.MonitoringPointResponse{
		ID: point.ID, PointCode: point.PointCode, Name: point.Name, XM: point.XM, YM: point.YM,
		HeightM: point.HeightM, AreaType: point.AreaType, BackgroundProfile: background,
		OwnerTeam: point.OwnerTeam, PointState: point.PointState, Version: point.Version,
		MeasurementCount: summary.MeasurementCount, LatestQuality: summary.LatestQuality,
		LatestMeasurementTime: summary.LatestMeasurementTime, CreatedAt: point.CreatedAt, UpdatedAt: point.UpdatedAt,
	}, nil
}
