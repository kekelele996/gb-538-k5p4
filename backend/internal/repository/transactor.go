package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"industrial-noise-source-attribution/backend/internal/model"
)

// Transactor 在同一数据库事务内编排跨实体写入，保证触发实体更新与
// 归因运行级联失效、审计记录原子提交。
type Transactor struct{ db *gorm.DB }

func NewTransactor(db *gorm.DB) *Transactor { return &Transactor{db: db} }

// InTx 执行 fn；返回错误或 panic 时回滚，否则提交。fn 只能使用传入的 tx。
func (t *Transactor) InTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}

// AuditLogWrite 收集事务内审计写入所需字段。
type AuditLogWrite struct {
	RequestID    string
	ActorID      uint
	ActorName    string
	Action       string
	EntityType   string
	EntityID     uint
	BeforeJSON   string
	AfterJSON    string
	MetadataJSON string
	CreatedAt    time.Time
}

// CreateAuditTx 在调用方事务内写入一条审计记录。
func CreateAuditTx(tx *gorm.DB, audit *AuditLogWrite) error {
	if audit.EntityID == 0 {
		return fmt.Errorf("audit log requires entity id")
	}
	return tx.Create(&model.AuditLog{
		RequestID: audit.RequestID, ActorID: audit.ActorID, ActorName: audit.ActorName,
		Action: audit.Action, EntityType: audit.EntityType, EntityID: audit.EntityID,
		BeforeJSON: audit.BeforeJSON, AfterJSON: audit.AfterJSON, MetadataJSON: audit.MetadataJSON,
		CreatedAt: audit.CreatedAt,
	}).Error
}
