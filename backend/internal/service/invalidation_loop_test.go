package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"industrial-noise-source-attribution/backend/internal/constants"
	"industrial-noise-source-attribution/backend/internal/dto"
	"industrial-noise-source-attribution/backend/internal/model"
	"industrial-noise-source-attribution/backend/internal/repository"
	"industrial-noise-source-attribution/backend/internal/util"
)

type invalidationFixture struct {
	db           *gorm.DB
	points       *MonitoringPointService
	measurements *NoiseMeasurementService
	sources      *SourceProfileService
	runs         *AttributionRunService
	supportRepo  *repository.SupportRepository
	runRepo      *repository.AttributionRunRepository
	actor        model.Actor
	otherActor   model.Actor
}

func setupInvalidationFixture(t *testing.T) *invalidationFixture {
	t.Helper()
	dsn := "file:invalidation-loop-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.MonitoringPoint{}, &model.NoiseMeasurement{},
		&model.SourceProfile{}, &model.AttributionRun{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC()
	users := []model.User{
		{ID: 1, Username: "engineer", PasswordHash: "x", DisplayName: "Acoustic Engineer", Role: constants.RoleAcousticEngineer, Active: true, CreatedAt: now},
		{ID: 2, Username: "reviewer", PasswordHash: "x", DisplayName: "Independent Reviewer", Role: constants.RoleReviewer, Active: true, CreatedAt: now},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	pointRepo := repository.NewMonitoringPointRepository(db)
	measurementRepo := repository.NewNoiseMeasurementRepository(db)
	sourceRepo := repository.NewSourceProfileRepository(db)
	runRepo := repository.NewAttributionRunRepository(db)
	supportRepo := repository.NewSupportRepository(db)
	return &invalidationFixture{
		db:           db,
		points:       NewMonitoringPointService(pointRepo, runRepo),
		measurements: NewNoiseMeasurementService(measurementRepo, pointRepo, runRepo),
		sources:      NewSourceProfileService(sourceRepo, runRepo),
		runs:         NewAttributionRunService(runRepo, measurementRepo, sourceRepo, supportRepo),
		supportRepo:  supportRepo, runRepo: runRepo,
		actor:      model.Actor{ID: 1, Username: "engineer", DisplayName: "Acoustic Engineer", Role: constants.RoleAcousticEngineer, RequestID: "req-engineer"},
		otherActor: model.Actor{ID: 2, Username: "reviewer", DisplayName: "Independent Reviewer", Role: constants.RoleReviewer, RequestID: "req-reviewer"},
	}
}

func fixtureSpectrum(base float64) map[string]float64 {
	result := make(map[string]float64, len(constants.OctaveBands))
	for _, band := range constants.OctaveBands {
		result[strconv.Itoa(band)] = base
	}
	return result
}

func (f *invalidationFixture) createPoint(t *testing.T, code string, x float64, background map[string]float64) dto.MonitoringPointResponse {
	t.Helper()
	point, err := f.points.Create(context.Background(), dto.CreateMonitoringPointRequest{
		PointCode: code, Name: code + " receptor", XM: x, YM: 20, HeightM: 1.5,
		AreaType: "boundary", BackgroundProfile: background, OwnerTeam: "Acoustics Lab",
	}, f.actor)
	if err != nil {
		t.Fatalf("create point %s: %v", code, err)
	}
	return point
}

// createReadyMeasurement imports a measurement and walks it to ready. The
// background must be far enough below the bands for normalization to pass.
func (f *invalidationFixture) createReadyMeasurement(t *testing.T, pointID uint, measuredAt time.Time, bands map[string]float64) dto.NoiseMeasurementResponse {
	t.Helper()
	imported, _, err := f.measurements.Create(context.Background(), dto.CreateNoiseMeasurementRequest{
		MonitoringPointID: pointID, MeasuredAt: measuredAt, DurationS: 900,
		OctaveBands: bands, OverallDBA: 80, BackgroundDBA: 40, WeatherNote: "fixture",
	}, f.actor)
	if err != nil {
		t.Fatalf("import measurement: %v", err)
	}
	for _, next := range []string{"validated", "normalized", "ready"} {
		imported, err = f.measurements.Transition(context.Background(), imported.ID, dto.MeasurementTransitionRequest{ToState: next, Version: imported.Version}, f.actor)
		if err != nil {
			t.Fatalf("transition measurement to %s: %v", next, err)
		}
	}
	return imported
}

func (f *invalidationFixture) createActiveSource(t *testing.T, code string, x float64) dto.SourceProfileResponse {
	t.Helper()
	profile, err := f.sources.Create(context.Background(), dto.CreateSourceProfileRequest{
		SourceCode: code, Name: code + " device", XM: x, YM: 0, HeightM: 1.5, ReferenceDistanceM: 1,
		OctavePower: fixtureSpectrum(95), Directivity: fixtureSpectrum(0), OperatingFactor: 0.9,
	}, f.actor)
	if err != nil {
		t.Fatalf("create source %s: %v", code, err)
	}
	profile, err = f.sources.Transition(context.Background(), profile.ID, dto.ProfileTransitionRequest{ToState: "active", LockVersion: profile.LockVersion}, f.actor)
	if err != nil {
		t.Fatalf("activate source %s: %v", code, err)
	}
	return profile
}

func (f *invalidationFixture) createRun(t *testing.T, measurementIDs, sourceIDs []uint) dto.AttributionRunResponse {
	t.Helper()
	run, reused, err := f.runs.Create(context.Background(), dto.CreateAttributionRunRequest{
		MeasurementIDs: measurementIDs, SourceProfileIDs: sourceIDs,
	}, f.actor, "idem-"+time.Now().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("create attribution run: %v", err)
	}
	if reused {
		t.Fatalf("expected a freshly calculated run")
	}
	if run.AttributionState != string(constants.AttributionCompleted) {
		t.Fatalf("new run must be completed, got %s", run.AttributionState)
	}
	return run
}

func (f *invalidationFixture) auditCount(t *testing.T, action string) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil {
		t.Fatalf("count audits %s: %v", action, err)
	}
	return count
}

func assertConflict(t *testing.T, action string, err error) {
	t.Helper()
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("%s must return 409 conflict, got %v", action, err)
	}
}

// TestSourceRetirementInvalidatesReferencingRuns covers: frozen source
// retirement invalidates referencing unconfirmed runs in the same
// transaction with trigger entity/reason/time/operator provenance; review,
// confirm and void on the invalidated run return 409; confirmed runs stay
// unchanged; FindByInput never returns the invalidated conclusion and a
// successor recomputation produces a fresh run.
func TestSourceRetirementInvalidatesReferencingRuns(t *testing.T) {
	f := setupInvalidationFixture(t)
	point := f.createPoint(t, "MP-INV-SRC", 80, fixtureSpectrum(40))
	measurement := f.createReadyMeasurement(t, point.ID, time.Now().UTC().Add(-2*time.Hour), fixtureSpectrum(75))
	sourceA := f.createActiveSource(t, "SRC-INV-A", 5)
	sourceB := f.createActiveSource(t, "SRC-INV-B", 25)

	unconfirmed := f.createRun(t, []uint{measurement.ID}, []uint{sourceA.ID})
	confirmed := f.createRun(t, []uint{measurement.ID}, []uint{sourceA.ID, sourceB.ID})
	reviewed, err := f.runs.Review(context.Background(), confirmed.ID, dto.ReviewAttributionRequest{Version: confirmed.Version, Note: "Independent review of frozen evidence."}, f.otherActor)
	if err != nil {
		t.Fatalf("review run: %v", err)
	}
	confirmed, err = f.runs.Confirm(context.Background(), confirmed.ID, dto.AttributionActionRequest{Version: reviewed.Version}, f.otherActor)
	if err != nil {
		t.Fatalf("confirm run: %v", err)
	}

	if _, err := f.sources.Transition(context.Background(), sourceA.ID, dto.ProfileTransitionRequest{
		ToState: "retired", LockVersion: sourceA.LockVersion,
	}, f.actor); err != nil {
		t.Fatalf("retire source: %v", err)
	}

	invalidatedRun, err := f.runs.Get(context.Background(), unconfirmed.ID)
	if err != nil {
		t.Fatalf("get invalidated run: %v", err)
	}
	if invalidatedRun.AttributionState != string(constants.AttributionInvalidated) {
		t.Fatalf("referencing unconfirmed run must be invalidated, got %s", invalidatedRun.AttributionState)
	}
	if invalidatedRun.Invalidation == nil {
		t.Fatal("invalidated run must expose provenance")
	}
	if invalidatedRun.Invalidation.EntityType != constants.InvalidationEntitySourceProfile ||
		invalidatedRun.Invalidation.EntityID != sourceA.ID {
		t.Fatalf("invalidation must record trigger entity, got %+v", invalidatedRun.Invalidation)
	}
	if !strings.Contains(invalidatedRun.Invalidation.EntityCode, sourceA.SourceCode) {
		t.Fatalf("invalidation must record trigger source code, got %q", invalidatedRun.Invalidation.EntityCode)
	}
	if invalidatedRun.Invalidation.Reason != constants.InvalidationReasonSourceRetired {
		t.Fatalf("unexpected invalidation reason %q", invalidatedRun.Invalidation.Reason)
	}
	if invalidatedRun.Invalidation.InvalidatedBy != "Acoustic Engineer" {
		t.Fatalf("invalidation must record triggering operator, got %q", invalidatedRun.Invalidation.InvalidatedBy)
	}
	if invalidatedRun.Invalidation.At.IsZero() {
		t.Fatal("invalidation must record trigger time")
	}
	if invalidatedRun.Version != unconfirmed.Version+1 {
		t.Fatalf("invalidation must bump optimistic version once: before=%d after=%d", unconfirmed.Version, invalidatedRun.Version)
	}

	stillConfirmed, err := f.runs.Get(context.Background(), confirmed.ID)
	if err != nil {
		t.Fatalf("get confirmed run: %v", err)
	}
	if stillConfirmed.AttributionState != string(constants.AttributionConfirmed) || stillConfirmed.Invalidation != nil {
		t.Fatal("confirmed results must remain unchanged by the invalidation loop")
	}

	_, err = f.runs.Review(context.Background(), invalidatedRun.ID, dto.ReviewAttributionRequest{Version: invalidatedRun.Version, Note: "late review must conflict"}, f.otherActor)
	assertConflict(t, "review invalidated run", err)
	_, err = f.runs.Confirm(context.Background(), invalidatedRun.ID, dto.AttributionActionRequest{Version: invalidatedRun.Version}, f.otherActor)
	assertConflict(t, "confirm invalidated run", err)
	_, err = f.runs.Void(context.Background(), invalidatedRun.ID, dto.AttributionActionRequest{Version: invalidatedRun.Version}, f.otherActor)
	assertConflict(t, "void invalidated run", err)

	stored, err := f.runRepo.Get(context.Background(), invalidatedRun.ID)
	if err != nil {
		t.Fatalf("load stored invalidated run: %v", err)
	}
	if _, findErr := f.runRepo.FindByInput(context.Background(), stored.InputHash, stored.AlgorithmVersion); findErr == nil {
		t.Fatal("FindByInput must not return an invalidated run for recomputation reuse")
	}

	sourceAv2 := f.createActiveSource(t, "SRC-INV-A", 5)
	if sourceAv2.ID == sourceA.ID {
		t.Fatal("new source version must be a new row")
	}
	recomputed, reused, err := f.runs.Create(context.Background(), dto.CreateAttributionRunRequest{
		MeasurementIDs: []uint{measurement.ID}, SourceProfileIDs: []uint{sourceAv2.ID},
	}, f.actor, "idem-recompute")
	if err != nil {
		t.Fatalf("recompute with successor source: %v", err)
	}
	if reused || recomputed.ID == invalidatedRun.ID {
		t.Fatal("recomputation must create a new run, not reuse the invalidated one")
	}
	if recomputed.AttributionState != string(constants.AttributionCompleted) {
		t.Fatalf("recomputed run must be completed, got %s", recomputed.AttributionState)
	}

	if got := f.auditCount(t, "attribution_run.invalidated"); got != 1 {
		t.Fatalf("expected exactly 1 invalidation audit, got %d", got)
	}
	var triggerAudit model.AuditLog
	if err := f.db.Where("action = ? AND entity_type = ? AND entity_id = ? AND metadata_json LIKE ?",
		"source_profile.state_changed", "SourceProfile", sourceA.ID, "%retired%").
		First(&triggerAudit).Error; err != nil {
		t.Fatalf("load trigger audit: %v", err)
	}
	if !strings.Contains(triggerAudit.MetadataJSON, "frozen_runs_invalidated") {
		t.Fatalf("trigger audit must mark the cascade, metadata=%s", triggerAudit.MetadataJSON)
	}
	var invalidationAudit model.AuditLog
	if err := f.db.Where("action = ?", "attribution_run.invalidated").First(&invalidationAudit).Error; err != nil {
		t.Fatalf("load invalidation audit: %v", err)
	}
	if invalidationAudit.RequestID != f.actor.RequestID || invalidationAudit.ActorID != f.actor.ID {
		t.Fatal("invalidation audit must share the trigger request and operator")
	}
	if !strings.Contains(invalidationAudit.BeforeJSON, string(constants.AttributionCompleted)) ||
		!strings.Contains(invalidationAudit.AfterJSON, string(constants.AttributionInvalidated)) {
		t.Fatalf("invalidation audit must retain full before/after chain, before=%s after=%s", invalidationAudit.BeforeJSON, invalidationAudit.AfterJSON)
	}
}

// TestFrozenInputTriggersEachInvalidateRuns exercises every trigger: frozen
// measurement superseded, point coordinates moved, point background updated
// and restored (identical-hash recomputation must create a new run), and
// point deactivated. Metadata-only point edits never cascade.
func TestFrozenInputTriggersEachInvalidateRuns(t *testing.T) {
	f := setupInvalidationFixture(t)
	originalBackground := fixtureSpectrum(38)
	point := f.createPoint(t, "MP-INV-CHAIN", 60, originalBackground)
	measurement := f.createReadyMeasurement(t, point.ID, time.Now().UTC().Add(-3*time.Hour), fixtureSpectrum(74))
	source := f.createActiveSource(t, "SRC-INV-CHAIN", 10)
	run := f.createRun(t, []uint{measurement.ID}, []uint{source.ID})

	// Trigger 1: frozen measurement replaced (ready -> superseded).
	if _, err := f.measurements.Transition(context.Background(), measurement.ID, dto.MeasurementTransitionRequest{
		ToState: "superseded", Version: measurement.Version,
	}, f.actor); err != nil {
		t.Fatalf("supersede measurement: %v", err)
	}
	invalidated, err := f.runs.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if invalidated.AttributionState != string(constants.AttributionInvalidated) {
		t.Fatalf("run must be invalidated when frozen measurement is superseded, got %s", invalidated.AttributionState)
	}
	if invalidated.Invalidation.EntityType != constants.InvalidationEntityNoiseMeasurement ||
		invalidated.Invalidation.EntityID != measurement.ID ||
		invalidated.Invalidation.Reason != constants.InvalidationReasonMeasurementSuperseded {
		t.Fatalf("measurement invalidation provenance mismatch: %+v", invalidated.Invalidation)
	}

	// New measurement frozen on the same point; coordinate move must
	// invalidate runs frozen on any of the point's measurements.
	newMeasurement := f.createReadyMeasurement(t, point.ID, time.Now().UTC().Add(-time.Hour), fixtureSpectrum(76))
	coordinateRun := f.createRun(t, []uint{newMeasurement.ID}, []uint{source.ID})
	point, err = f.points.Update(context.Background(), point.ID, dto.UpdateMonitoringPointRequest{
		Name: point.Name, XM: point.XM + 12, YM: point.YM, HeightM: point.HeightM,
		AreaType: point.AreaType, BackgroundProfile: point.BackgroundProfile,
		OwnerTeam: point.OwnerTeam, Version: point.Version,
	}, f.actor)
	if err != nil {
		t.Fatalf("move point coordinates: %v", err)
	}
	coordinateInvalidated, err := f.runs.Get(context.Background(), coordinateRun.ID)
	if err != nil {
		t.Fatalf("get coordinate run: %v", err)
	}
	if coordinateInvalidated.AttributionState != string(constants.AttributionInvalidated) ||
		coordinateInvalidated.Invalidation.Reason != constants.InvalidationReasonPointCoordinates ||
		coordinateInvalidated.Invalidation.EntityID != point.ID {
		t.Fatalf("coordinate change provenance mismatch: state=%s invalidation=%+v",
			coordinateInvalidated.AttributionState, coordinateInvalidated.Invalidation)
	}

	// A metadata-only edit (name/area/owner, identical coordinates and
	// background) must not cascade.
	metadataRun := f.createRun(t, []uint{newMeasurement.ID}, []uint{source.ID})
	point, err = f.points.Update(context.Background(), point.ID, dto.UpdateMonitoringPointRequest{
		Name: "Renamed receptor", XM: point.XM, YM: point.YM, HeightM: point.HeightM,
		AreaType: "workshop", BackgroundProfile: point.BackgroundProfile,
		OwnerTeam: "EHS Review", Version: point.Version,
	}, f.actor)
	if err != nil {
		t.Fatalf("metadata-only point edit: %v", err)
	}
	metadataUnchanged, err := f.runs.Get(context.Background(), metadataRun.ID)
	if err != nil {
		t.Fatalf("get metadata run: %v", err)
	}
	if metadataUnchanged.AttributionState != string(constants.AttributionCompleted) {
		t.Fatalf("non-frozen metadata edit must not invalidate runs, got %s", metadataUnchanged.AttributionState)
	}

	// Trigger 3: background spectrum update invalidates with background reason.
	changedBackground := fixtureSpectrum(38)
	changedBackground["8000"] = 39.5
	point, err = f.points.Update(context.Background(), point.ID, dto.UpdateMonitoringPointRequest{
		Name: point.Name, XM: point.XM, YM: point.YM, HeightM: point.HeightM,
		AreaType: point.AreaType, BackgroundProfile: changedBackground,
		OwnerTeam: point.OwnerTeam, Version: point.Version,
	}, f.actor)
	if err != nil {
		t.Fatalf("background point edit: %v", err)
	}
	backgroundInvalidated, err := f.runs.Get(context.Background(), metadataRun.ID)
	if err != nil {
		t.Fatalf("get background run: %v", err)
	}
	if backgroundInvalidated.AttributionState != string(constants.AttributionInvalidated) ||
		backgroundInvalidated.Invalidation.Reason != constants.InvalidationReasonPointBackground {
		t.Fatalf("background change provenance mismatch: state=%s invalidation=%+v",
			backgroundInvalidated.AttributionState, backgroundInvalidated.Invalidation)
	}

	// Restore the exact original background: the same canonical frozen input
	// now has only an invalidated prior run, so recomputation must insert a
	// brand-new run rather than reuse the invalidated conclusion.
	point, err = f.points.Update(context.Background(), point.ID, dto.UpdateMonitoringPointRequest{
		Name: point.Name, XM: point.XM, YM: point.YM, HeightM: point.HeightM,
		AreaType: point.AreaType, BackgroundProfile: originalBackground,
		OwnerTeam: point.OwnerTeam, Version: point.Version,
	}, f.actor)
	if err != nil {
		t.Fatalf("restore background: %v", err)
	}
	restored, restoredReused, err := f.runs.Create(context.Background(), dto.CreateAttributionRunRequest{
		MeasurementIDs: []uint{newMeasurement.ID}, SourceProfileIDs: []uint{source.ID},
	}, f.actor, "idem-restored")
	if err != nil {
		t.Fatalf("recompute restored identical input: %v", err)
	}
	if restoredReused || restored.ID == metadataRun.ID {
		t.Fatal("identical frozen input whose prior run was invalidated must create a new run")
	}
	if restored.InputHash != metadataRun.InputHash {
		t.Fatalf("restored frozen input must hash identically: %s vs %s", restored.InputHash, metadataRun.InputHash)
	}
	if restored.AttributionState != string(constants.AttributionCompleted) {
		t.Fatalf("restored recomputation must be completed, got %s", restored.AttributionState)
	}

	// Trigger 4: point deactivation invalidates the remaining unconfirmed run.
	if _, err := f.points.Deactivate(context.Background(), point.ID, point.Version, f.actor); err != nil {
		t.Fatalf("deactivate point: %v", err)
	}
	deactivatedRun, err := f.runs.Get(context.Background(), restored.ID)
	if err != nil {
		t.Fatalf("get deactivation run: %v", err)
	}
	if deactivatedRun.AttributionState != string(constants.AttributionInvalidated) ||
		deactivatedRun.Invalidation.Reason != constants.InvalidationReasonPointDeactivated {
		t.Fatalf("point deactivation provenance mismatch: state=%s invalidation=%+v",
			deactivatedRun.AttributionState, deactivatedRun.Invalidation)
	}

	// One invalidation audit per cascade: measurement, coordinate, background,
	// deactivation. The metadata-only edit contributes none.
	if got := f.auditCount(t, "attribution_run.invalidated"); got != 4 {
		t.Fatalf("expected exactly 4 invalidation audits across the loops, got %d", got)
	}
}

// TestInvalidationAuditSharesTriggerTransaction proves the trigger write and
// every invalidation audit share request id and operator, and the run's
// optimistic version advances exactly once in the cascade.
func TestInvalidationAuditSharesTriggerTransaction(t *testing.T) {
	f := setupInvalidationFixture(t)
	point := f.createPoint(t, "MP-INV-TX", 90, fixtureSpectrum(36))
	measurement := f.createReadyMeasurement(t, point.ID, time.Now().UTC().Add(-90*time.Minute), fixtureSpectrum(72))
	source := f.createActiveSource(t, "SRC-INV-TX", 8)
	run := f.createRun(t, []uint{measurement.ID}, []uint{source.ID})

	if _, err := f.sources.Transition(context.Background(), source.ID, dto.ProfileTransitionRequest{
		ToState: "retired", LockVersion: source.LockVersion,
	}, f.actor); err != nil {
		t.Fatalf("retire source: %v", err)
	}

	var invalidationAudits []model.AuditLog
	if err := f.db.Where("request_id = ? AND action = ?", f.actor.RequestID, "attribution_run.invalidated").Find(&invalidationAudits).Error; err != nil {
		t.Fatalf("query invalidation audits: %v", err)
	}
	if len(invalidationAudits) != 1 || invalidationAudits[0].EntityID != run.ID {
		t.Fatalf("expected exactly one invalidation audit for run %d in the trigger request, got %+v", run.ID, invalidationAudits)
	}
	var triggerAudits []model.AuditLog
	if err := f.db.Where("request_id = ? AND action = ? AND entity_id = ? AND metadata_json LIKE ?",
		f.actor.RequestID, "source_profile.state_changed", source.ID, "%retired%").
		Find(&triggerAudits).Error; err != nil {
		t.Fatalf("query trigger audits: %v", err)
	}
	if len(triggerAudits) != 1 {
		t.Fatalf("expected exactly one trigger audit, got %d", len(triggerAudits))
	}
	if triggerAudits[0].CreatedAt.Sub(invalidationAudits[0].CreatedAt).Abs() > time.Second {
		t.Fatal("trigger and invalidation must be recorded in the same transactional moment")
	}

	stored, err := f.runRepo.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Version != run.Version+1 {
		t.Fatalf("invalidation must bump optimistic version once: before=%d after=%d", run.Version, stored.Version)
	}
}
