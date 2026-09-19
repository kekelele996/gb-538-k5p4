package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"industrial-noise-source-attribution/backend/internal/constants"
	"industrial-noise-source-attribution/backend/internal/dto"
	"industrial-noise-source-attribution/backend/internal/model"
	"industrial-noise-source-attribution/backend/internal/repository"
	"industrial-noise-source-attribution/backend/internal/util"
)

type invalidationFixture struct {
	db            *gorm.DB
	points        *MonitoringPointService
	measurements  *NoiseMeasurementService
	sources       *SourceProfileService
	runs          *AttributionRunService
	pointID       uint
	otherPointID  uint
	sourceID      uint
	otherSourceID uint
	measurementID uint
	actor         model.Actor
	otherActor    model.Actor
}

func newInvalidationFixture(t *testing.T) invalidationFixture {
	t.Helper()
	dsn := "file:invalidation-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.MonitoringPoint{}, &model.NoiseMeasurement{},
		&model.SourceProfile{}, &model.AttributionRun{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_run_input_active " +
			"ON attribution_runs (input_hash, algorithm_version) " +
			"WHERE attribution_state <> 'invalidated'",
	).Error; err != nil {
		t.Fatalf("create partial index: %v", err)
	}
	pointRepo := repository.NewMonitoringPointRepository(db)
	measurementRepo := repository.NewNoiseMeasurementRepository(db)
	sourceRepo := repository.NewSourceProfileRepository(db)
	runRepo := repository.NewAttributionRunRepository(db)
	transactor := repository.NewTransactor(db)
	invalidation := NewFrozenInputInvalidationService(transactor)

	fixture := invalidationFixture{
		db:           db,
		points:       NewMonitoringPointService(pointRepo, transactor, invalidation),
		measurements: NewNoiseMeasurementService(measurementRepo, pointRepo, transactor, invalidation),
		sources:      NewSourceProfileService(sourceRepo, transactor, invalidation),
		runs:         NewAttributionRunService(runRepo, measurementRepo, sourceRepo, transactor),
		actor:        model.Actor{ID: 1, Username: "engineer", DisplayName: "Acoustic Engineer", Role: constants.RoleAcousticEngineer, RequestID: "req-actor-1"},
		otherActor:   model.Actor{ID: 2, Username: "reviewer", DisplayName: "Independent Reviewer", Role: constants.RoleReviewer, RequestID: "req-actor-2"},
	}
	ctx := context.Background()
	point, err := fixture.points.Create(ctx, dto.CreateMonitoringPointRequest{
		PointCode: "MP-INV-01", Name: "Invalidation point", XM: 10, YM: 10, HeightM: 1.5,
		AreaType: "workshop", BackgroundProfile: testSpectrum(40), OwnerTeam: "Acoustics Lab",
	}, fixture.actor)
	if err != nil {
		t.Fatalf("create point: %v", err)
	}
	fixture.pointID = point.ID
	otherPoint, err := fixture.points.Create(ctx, dto.CreateMonitoringPointRequest{
		PointCode: "MP-INV-02", Name: "Other point", XM: 50, YM: 50, HeightM: 1.5,
		AreaType: "workshop", BackgroundProfile: testSpectrum(38), OwnerTeam: "Acoustics Lab",
	}, fixture.actor)
	if err != nil {
		t.Fatalf("create other point: %v", err)
	}
	fixture.otherPointID = otherPoint.ID
	measurement := mustCreateReadyMeasurement(t, fixture, fixture.pointID)
	fixture.measurementID = measurement.ID
	mustCreateReadyMeasurement(t, fixture, fixture.otherPointID)
	source := mustCreateActiveSource(t, fixture, "SRC-INV-01")
	fixture.sourceID = source.ID
	otherSource := mustCreateActiveSource(t, fixture, "SRC-INV-02")
	fixture.otherSourceID = otherSource.ID
	return fixture
}

func testSpectrum(base float64) map[string]float64 {
	return map[string]float64{"63": base + 8, "125": base + 10, "250": base + 12, "500": base + 11,
		"1000": base + 9, "2000": base + 7, "4000": base + 5, "8000": base + 3}
}

func mustCreateReadyMeasurement(t *testing.T, fixture invalidationFixture, pointID uint) dto.NoiseMeasurementResponse {
	t.Helper()
	ctx := context.Background()
	measuredAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC).Add(time.Duration(pointID) * time.Hour)
	created, _, err := fixture.measurements.Create(ctx, dto.CreateNoiseMeasurementRequest{
		MonitoringPointID: pointID, MeasuredAt: measuredAt, DurationS: 900,
		OctaveBands: testSpectrum(62), OverallDBA: 72, BackgroundDBA: 42,
	}, fixture.actor)
	if err != nil {
		t.Fatalf("create measurement: %v", err)
	}
	for _, to := range []constants.MeasurementState{constants.MeasurementValidated, constants.MeasurementNormalized, constants.MeasurementReady} {
		updated, transitionErr := fixture.measurements.Transition(ctx, created.ID, dto.MeasurementTransitionRequest{
			ToState: string(to), Version: created.Version,
		}, fixture.actor)
		if transitionErr != nil {
			t.Fatalf("transition measurement to %s: %v", to, transitionErr)
		}
		created = updated
	}
	return created
}

func mustCreateActiveSource(t *testing.T, fixture invalidationFixture, code string) dto.SourceProfileResponse {
	t.Helper()
	ctx := context.Background()
	created, err := fixture.sources.Create(ctx, dto.CreateSourceProfileRequest{
		SourceCode: code, Name: code + " unit", XM: 0, YM: 0, HeightM: 1.8,
		ReferenceDistanceM: 1, OctavePower: testSpectrum(90),
		Directivity:     map[string]float64{"63": 0, "125": 0, "250": 0, "500": 0, "1000": 0, "2000": 0, "4000": 0, "8000": 0},
		OperatingFactor: 0.9,
	}, fixture.actor)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	activated, err := fixture.sources.Transition(ctx, created.ID, dto.ProfileTransitionRequest{
		ToState: string(constants.ProfileActive), LockVersion: created.LockVersion,
	}, fixture.actor)
	if err != nil {
		t.Fatalf("activate source: %v", err)
	}
	return activated
}

func mustCreateRun(t *testing.T, fixture invalidationFixture, measurementIDs, sourceIDs []uint) dto.AttributionRunResponse {
	t.Helper()
	run, reused, err := fixture.runs.Create(context.Background(), dto.CreateAttributionRunRequest{
		MeasurementIDs: measurementIDs, SourceProfileIDs: sourceIDs,
	}, fixture.actor, "key-"+t.Name()+"-"+time.Now().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("create attribution run: %v", err)
	}
	if reused {
		t.Fatalf("expected a new run, got an idempotent replay")
	}
	return run
}

func assertRunState(t *testing.T, fixture invalidationFixture, runID uint, want constants.AttributionState) dto.AttributionRunResponse {
	t.Helper()
	got, err := fixture.runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run %d: %v", runID, err)
	}
	if got.AttributionState != string(want) {
		t.Fatalf("run %d state = %s, want %s", runID, got.AttributionState, want)
	}
	return got
}

func TestPointCoordinatesChangeInvalidatesUnconfirmedRun(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})

	updated, err := fixture.points.Update(context.Background(), fixture.pointID, dto.UpdateMonitoringPointRequest{
		Name: "Invalidation point", XM: 22, YM: 10, HeightM: 1.5, AreaType: "workshop",
		BackgroundProfile: testSpectrum(40), OwnerTeam: "Acoustics Lab", Version: 1,
	}, fixture.actor)
	if err != nil {
		t.Fatalf("update point coordinates: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("point version = %d, want 2", updated.Version)
	}
	if updated.XM != 22 {
		t.Fatalf("point coordinates must persist, x_m = %v, want 22", updated.XM)
	}
	detail := assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	if detail.Invalidation == nil {
		t.Fatal("invalidated run must expose invalidation detail")
	}
	if detail.Invalidation.ReasonCode != string(constants.InvalidationReasonPointCoordinates) {
		t.Fatalf("reason code = %s", detail.Invalidation.ReasonCode)
	}
	if detail.Invalidation.TriggerType != "MonitoringPoint" || detail.Invalidation.TriggerID != fixture.pointID ||
		detail.Invalidation.TriggerCode != "MP-INV-01" || detail.Invalidation.InvalidatedAt == nil {
		t.Fatalf("invalidation detail missing trigger source/time: %+v", detail.Invalidation)
	}
	if _, err := fixture.runs.Review(context.Background(), run.ID, dto.ReviewAttributionRequest{Version: detail.Version, Note: "late review"}, fixture.otherActor); !isConflict(err) {
		t.Fatalf("review on invalidated run must return conflict, got %v", err)
	}
	if _, err := fixture.runs.Confirm(context.Background(), run.ID, dto.AttributionActionRequest{Version: detail.Version}, fixture.otherActor); !isConflict(err) {
		t.Fatalf("confirm on invalidated run must return conflict, got %v", err)
	}
	assertInvalidationAudit(t, fixture.db, run.ID, constants.InvalidationReasonPointCoordinates, fixture.pointID)
	// 触发实体（监测点）审计必须在同事务内记录被级联失效的运行 ID。
	assertTriggerAuditLinks(t, fixture.db, "MonitoringPoint", fixture.pointID, "monitoring_point.updated", []uint{run.ID})
}

func TestPointBackgroundChangeInvalidatesUnconfirmedRun(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})

	if _, err := fixture.points.Update(context.Background(), fixture.pointID, dto.UpdateMonitoringPointRequest{
		Name: "Invalidation point", XM: 10, YM: 10, HeightM: 1.5, AreaType: "workshop",
		BackgroundProfile: testSpectrum(44), OwnerTeam: "Acoustics Lab", Version: 1,
	}, fixture.actor); err != nil {
		t.Fatalf("update background: %v", err)
	}
	detail := assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	if detail.Invalidation.ReasonCode != string(constants.InvalidationReasonPointBackground) {
		t.Fatalf("reason code = %s", detail.Invalidation.ReasonCode)
	}
	stored, err := fixture.points.Get(context.Background(), fixture.pointID)
	if err != nil {
		t.Fatalf("get point: %v", err)
	}
	if stored.BackgroundProfile["500"] != testSpectrum(44)["500"] {
		t.Fatalf("background profile must persist after update, got %v want %v", stored.BackgroundProfile["500"], testSpectrum(44)["500"])
	}
}

func TestPointDeactivationInvalidatesUnconfirmedRun(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})

	if _, err := fixture.points.Deactivate(context.Background(), fixture.pointID, 1, fixture.actor); err != nil {
		t.Fatalf("deactivate point: %v", err)
	}
	detail := assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	if detail.Invalidation.ReasonCode != string(constants.InvalidationReasonPointDeactivated) {
		t.Fatalf("reason code = %s", detail.Invalidation.ReasonCode)
	}
	assertTriggerAuditLinks(t, fixture.db, "MonitoringPoint", fixture.pointID, "monitoring_point.deactivated", []uint{run.ID})
}

func TestMeasurementSupersededInvalidatesFrozenRun(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})

	if _, err := fixture.measurements.Transition(context.Background(), fixture.measurementID,
		dto.MeasurementTransitionRequest{ToState: string(constants.MeasurementSuperseded), Version: 4},
		fixture.actor); err != nil {
		t.Fatalf("supersede measurement: %v", err)
	}
	detail := assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	if detail.Invalidation.ReasonCode != string(constants.InvalidationReasonMeasurementReplaced) ||
		detail.Invalidation.TriggerType != "NoiseMeasurement" || detail.Invalidation.TriggerID != fixture.measurementID {
		t.Fatalf("invalidation detail wrong: %+v", detail.Invalidation)
	}
	assertTriggerAuditLinks(t, fixture.db, "NoiseMeasurement", fixture.measurementID, "noise_measurement.state_changed", []uint{run.ID})
}

func TestSourceProfileRetiredInvalidatesFrozenRun(t *testing.T) {
	fixture := newInvalidationFixture(t)
	runAffected := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})
	runUnrelated := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.otherSourceID})

	if _, err := fixture.sources.Transition(context.Background(), fixture.sourceID,
		dto.ProfileTransitionRequest{ToState: string(constants.ProfileRetired), LockVersion: 2}, fixture.actor); err != nil {
		t.Fatalf("retire source: %v", err)
	}
	detail := assertRunState(t, fixture, runAffected.ID, constants.AttributionInvalidated)
	if detail.Invalidation.ReasonCode != string(constants.InvalidationReasonSourceRetired) ||
		detail.Invalidation.TriggerType != "SourceProfile" || detail.Invalidation.TriggerID != fixture.sourceID {
		t.Fatalf("invalidation detail wrong: %+v", detail.Invalidation)
	}
	assertRunState(t, fixture, runUnrelated.ID, constants.AttributionCompleted)
	assertTriggerAuditLinks(t, fixture.db, "SourceProfile", fixture.sourceID, "source_profile.state_changed", []uint{runAffected.ID})
}

func TestConfirmedRunSurvivesInputChanges(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})
	reviewed, err := fixture.runs.Review(context.Background(), run.ID,
		dto.ReviewAttributionRequest{Version: run.Version, Note: "Independent review passed."}, fixture.otherActor)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	confirmed, err := fixture.runs.Confirm(context.Background(), run.ID,
		dto.AttributionActionRequest{Version: reviewed.Version}, fixture.otherActor)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := fixture.points.Deactivate(context.Background(), fixture.pointID, 1, fixture.actor); err != nil {
		t.Fatalf("deactivate point: %v", err)
	}
	stored := assertRunState(t, fixture, run.ID, constants.AttributionConfirmed)
	if stored.Invalidation != nil {
		t.Fatalf("confirmed run must not carry invalidation detail: %+v", stored.Invalidation)
	}
	if stored.Version != confirmed.Version {
		t.Fatalf("confirmed run version changed from %d to %d", confirmed.Version, stored.Version)
	}
}

func TestReviewOnInvalidatedRunConflictsAndRecomputeDoesNotReuse(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})
	if _, err := fixture.points.Deactivate(context.Background(), fixture.pointID, 1, fixture.actor); err != nil {
		t.Fatalf("deactivate point: %v", err)
	}
	invalidated := assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	// 复核与确认均被拒绝并返回 409。
	if _, err := fixture.runs.Review(context.Background(), run.ID,
		dto.ReviewAttributionRequest{Version: invalidated.Version, Note: "too late"}, fixture.otherActor); !isConflict(err) {
		t.Fatalf("review conflict expected, got %v", err)
	}
	// 旧失效结果不会被新运行复用：即使冻结输入内容（因而 input_hash）相同，
	// 失效行也排除在幂等查找之外，重新计算必须生成新运行。
	recomputed, reused, err := fixture.runs.Create(context.Background(), dto.CreateAttributionRunRequest{
		MeasurementIDs: []uint{fixture.measurementID}, SourceProfileIDs: []uint{fixture.sourceID},
	}, fixture.actor, "key-recompute")
	if err != nil {
		t.Fatalf("recompute with unchanged frozen content must create a new run: %v", err)
	}
	if reused || recomputed.ID == run.ID {
		t.Fatalf("recompute must not reuse invalidated run %d: reused=%v newID=%d", run.ID, reused, recomputed.ID)
	}
	if recomputed.InputHash != run.InputHash {
		t.Fatalf("unchanged frozen content must keep the same input hash")
	}
	assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	assertRunState(t, fixture, recomputed.ID, constants.AttributionCompleted)
	assertRecomputeAuditLinks(t, fixture.db, recomputed.ID, []uint{run.ID})
}

func TestRecomputeAfterPointReactivationCreatesNewRun(t *testing.T) {
	fixture := newInvalidationFixture(t)
	run := mustCreateRun(t, fixture, []uint{fixture.measurementID}, []uint{fixture.sourceID})
	if _, err := fixture.points.Deactivate(context.Background(), fixture.pointID, 1, fixture.actor); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)

	// 监测点纠正后重新启用（业务无启用动作，测试中直接条件更新），冻结内容不变 -> input_hash 不变。
	if err := fixture.dbReactivatePoint(fixture.pointID); err != nil {
		t.Fatalf("reactivate point: %v", err)
	}
	recomputed, reused, err := fixture.runs.Create(context.Background(), dto.CreateAttributionRunRequest{
		MeasurementIDs: []uint{fixture.measurementID}, SourceProfileIDs: []uint{fixture.sourceID},
	}, fixture.actor, "key-recompute-after-fix")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	// 同一 input_hash 的旧结果已失效，不能复用，必须生成新运行并在审计中串联旧失效运行。
	if reused || recomputed.ID == run.ID {
		t.Fatalf("recompute must create a new run: reused=%v old=%d new=%d", reused, run.ID, recomputed.ID)
	}
	if recomputed.InputHash != run.InputHash {
		t.Fatalf("unchanged frozen content must keep the same input hash")
	}
	assertRunState(t, fixture, run.ID, constants.AttributionInvalidated)
	assertRunState(t, fixture, recomputed.ID, constants.AttributionCompleted)
	assertRecomputeAuditLinks(t, fixture.db, recomputed.ID, []uint{run.ID})
}

// dbReactivatePoint 在测试中模拟管理员纠正后重新启用监测点（业务无该动作，直接条件更新）。
func (f invalidationFixture) dbReactivatePoint(pointID uint) error {
	return f.db.Model(&model.MonitoringPoint{}).Where("id = ?", pointID).
		Updates(map[string]any{"point_state": "active", "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()}).Error
}

func assertInvalidationAudit(t *testing.T, db *gorm.DB, runID uint, reason constants.InvalidationReasonCode, triggerID uint) {
	t.Helper()
	var logs []model.AuditLog
	if err := db.Where("entity_type = ? AND entity_id = ? AND action = ?", "AttributionRun", runID, "attribution_run.invalidated").
		Find(&logs).Error; err != nil {
		t.Fatalf("query invalidation audits: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly 1 invalidation audit, got %d", len(logs))
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(logs[0].MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if metadata["reason_code"] != string(reason) {
		t.Fatalf("audit reason_code = %v, want %s", metadata["reason_code"], reason)
	}
	if triggerIDValue, _ := metadata["trigger_entity_id"].(float64); uint(triggerIDValue) != triggerID {
		t.Fatalf("audit trigger_entity_id = %v, want %d", metadata["trigger_entity_id"], triggerID)
	}
	var before map[string]any
	if err := json.Unmarshal([]byte(logs[0].BeforeJSON), &before); err != nil {
		t.Fatalf("decode before snapshot: %v", err)
	}
	// model 无 json tag，审计快照保留 Go 字段名（与全部既有审计一致）。
	priorState, _ := before["AttributionState"].(string)
	if priorState != "completed" && priorState != "reviewed" {
		t.Fatalf("before snapshot must preserve prior state, got %v", before["AttributionState"])
	}
	var afterSnapshot map[string]any
	if err := json.Unmarshal([]byte(logs[0].AfterJSON), &afterSnapshot); err != nil {
		t.Fatalf("decode after snapshot: %v", err)
	}
	if afterSnapshot["AttributionState"] != "invalidated" || afterSnapshot["InvalidationReasonCode"] != string(reason) {
		t.Fatalf("after snapshot must record invalidated state and reason: %+v", afterSnapshot)
	}
}

func assertTriggerAuditLinks(t *testing.T, db *gorm.DB, entityType string, entityID uint, action string, runIDs []uint) {
	t.Helper()
	var log model.AuditLog
	if err := db.Where("entity_type = ? AND entity_id = ? AND action = ?", entityType, entityID, action).
		Order("id DESC").First(&log).Error; err != nil {
		t.Fatalf("query trigger audit: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(log.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode trigger metadata: %v", err)
	}
	rawIDs, ok := metadata["invalidated_run_ids"].([]any)
	if !ok || len(rawIDs) != len(runIDs) {
		t.Fatalf("trigger audit must list invalidated run ids %v, got %v", runIDs, metadata["invalidated_run_ids"])
	}
}

func assertRecomputeAuditLinks(t *testing.T, db *gorm.DB, newRunID uint, invalidatedIDs []uint) {
	t.Helper()
	var log model.AuditLog
	if err := db.Where("entity_type = ? AND entity_id = ? AND action = ?", "AttributionRun", newRunID, "attribution_run.completed").
		Order("id DESC").First(&log).Error; err != nil {
		t.Fatalf("query recompute audit: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(log.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	rawIDs, ok := metadata["recomputed_from_invalidated_run_ids"].([]any)
	if !ok || len(rawIDs) != len(invalidatedIDs) {
		t.Fatalf("recompute audit must link prior invalidated runs, got %v", metadata["recomputed_from_invalidated_run_ids"])
	}
}

func isConflict(err error) bool {
	var appErr *util.AppError
	return errors.As(err, &appErr) && appErr.Status == 409
}
