package model

import "time"

type AttributionRun struct {
	ID                   uint      `gorm:"primaryKey"`
	RunCode              string    `gorm:"size:64;uniqueIndex;not null"`
	MeasurementIDsJSON   string    `gorm:"type:text;not null"`
	SourceProfileIDsJSON string    `gorm:"type:text;not null"`
	AlgorithmVersion     string    `gorm:"size:64;uniqueIndex:idx_active_input_version,where:attribution_state <> 'invalidated';not null"`
	InputHash            string    `gorm:"size:64;uniqueIndex:idx_active_input_version,where:attribution_state <> 'invalidated';not null"`
	InputSnapshotJSON    string    `gorm:"type:text;not null"`
	NormalizedBandsJSON  string    `gorm:"type:text;not null"`
	ContributionsJSON    string    `gorm:"type:text;not null"`
	EvidenceJSON         string    `gorm:"type:text;not null"`
	ResidualError        float64   `gorm:"not null"`
	AttributionState     string    `gorm:"size:24;index;not null"`
	Explanation          string    `gorm:"type:text;not null"`
	StartedAt            time.Time `gorm:"not null"`
	FinishedAt           *time.Time
	CreatedBy            uint   `gorm:"index;not null"`
	ReviewedBy           *uint  `gorm:"index"`
	ReviewNote           string `gorm:"size:1000"`
	// Frozen-input invalidation provenance. Populated only when the run reaches
	// AttributionInvalidated through the closed loop; the conclusion itself is
	// retained, only marked unusable.
	InvalidatedAt      *time.Time `gorm:"index"`
	InvalidationReason string     `gorm:"size:500"`
	TriggerEntityType  string     `gorm:"size:80"`
	TriggerEntityID    uint       `gorm:"index"`
	TriggerEntityCode  string     `gorm:"size:64"`
	InvalidatedBy      uint       `gorm:"index"`
	Version            uint       `gorm:"not null;default:1"`
	CreatedAt          time.Time  `gorm:"not null"`
	UpdatedAt          time.Time  `gorm:"not null"`
}

func (AttributionRun) TableName() string { return "attribution_runs" }
