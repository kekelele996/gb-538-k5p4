package model

import "time"

type AttributionRun struct {
	ID                   uint      `gorm:"primaryKey"`
	RunCode              string    `gorm:"size:64;uniqueIndex;not null"`
	MeasurementIDsJSON   string    `gorm:"type:text;not null"`
	SourceProfileIDsJSON string    `gorm:"type:text;not null"`
	AlgorithmVersion     string    `gorm:"size:64;index:idx_run_input_lookup;not null"`
	InputHash            string    `gorm:"size:64;index:idx_run_input_lookup;not null"`
	InputSnapshotJSON    string    `gorm:"type:text;not null"`
	NormalizedBandsJSON  string    `gorm:"type:text;not null"`
	ContributionsJSON    string    `gorm:"type:text;not null"`
	EvidenceJSON         string    `gorm:"type:text;not null"`
	ResidualError        float64   `gorm:"not null"`
	AttributionState     string    `gorm:"size:24;index;not null"`
	Explanation          string    `gorm:"type:text;not null"`
	StartedAt            time.Time `gorm:"not null"`
	FinishedAt           *time.Time
	CreatedBy            uint `gorm:"index;not null"`
	ReviewedBy           *uint
	ReviewNote           string `gorm:"size:1000"`
	// 冻结输入失效闭环：未确认运行被级联失效时记录触发来源与原因，已确认结果永不写入这些字段。
	InvalidationReasonCode string     `gorm:"size:48;not null;default:''"`
	InvalidationReason     string     `gorm:"size:500;not null;default:''"`
	InvalidationEntityType string     `gorm:"size:48;not null;default:''"`
	InvalidationEntityID   uint       `gorm:"index;not null;default:0"`
	InvalidationEntityCode string     `gorm:"size:64;not null;default:''"`
	InvalidatedBy          uint       `gorm:"index;not null;default:0"`
	InvalidatedByName      string     `gorm:"size:120;not null;default:''"`
	InvalidatedAt          *time.Time `gorm:"index"`
	Version                uint       `gorm:"not null;default:1"`
	CreatedAt              time.Time  `gorm:"not null"`
	UpdatedAt              time.Time  `gorm:"not null"`
}

func (AttributionRun) TableName() string { return "attribution_runs" }
