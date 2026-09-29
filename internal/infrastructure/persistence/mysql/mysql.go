// Package mysql contains the optional, bounded MySQL example adapter.
//
// GORM stays behind this infrastructure boundary. Application state, approval,
// and idempotency ownership remain in the existing run service.
package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/plugin/opentelemetry/tracing"

	"go.opentelemetry.io/otel/trace"
	"github.com/observer-mimiron/supervisor-template/internal/application"
)

const (
	OrderQueryToolID  = "mysql_order_query"
	OrderInsertToolID = "mysql_order_insert"
	maxPayloadBytes   = 64 << 10
)

var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$`)

type User struct {
	ID        uint64    `gorm:"primaryKey"`
	Name      string    `gorm:"size:128;not null"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
	Orders    []Order   `gorm:"foreignKey:UserID"`
}

type Product struct {
	ID        uint64    `gorm:"primaryKey"`
	Name      string    `gorm:"size:128;not null"`
	Price     string    `gorm:"type:decimal(12,2);not null;check:price >= 0"`
	Stock     int64     `gorm:"not null;check:stock >= 0"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
	Orders    []Order   `gorm:"foreignKey:ProductID"`
}

type Order struct {
	ID          uint64    `gorm:"primaryKey"`
	UserID      uint64    `gorm:"not null;index"`
	ProductID   uint64    `gorm:"not null;index"`
	Quantity    int64     `gorm:"not null;check:quantity > 0"`
	TotalAmount string    `gorm:"type:decimal(12,2);not null;check:total_amount >= 0"`
	CreatedAt   time.Time `gorm:"not null"`
	UpdatedAt   time.Time `gorm:"not null"`
	User        User      `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
	Product     Product   `gorm:"foreignKey:ProductID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
}

type runLeaseRow struct {
	RunID      string    `gorm:"primaryKey;size:128"`
	OwnerToken string    `gorm:"size:64;not null"`
	ExpiresAt  time.Time `gorm:"type:datetime(3);not null;index"`
}

func (runLeaseRow) TableName() string { return "run_leases" }

type queryInput struct {
	UserID  *uint64 `json:"user_id"`
	OrderID *uint64 `json:"order_id"`
}

type insertInput struct {
	UserID      uint64 `json:"user_id"`
	ProductID   uint64 `json:"product_id"`
	Quantity    int64  `json:"quantity"`
	TotalAmount string `json:"total_amount"`
}

type Adapter struct {
	db          *gorm.DB
	mu          sync.Mutex
	idempotency map[string]string
}

func Open(ctx context.Context, dsn string, provider trace.TracerProvider) (*Adapter, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("MySQL DSN 不能为空")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 失败: %w", err)
	}
	pluginOptions := []tracing.Option{tracing.WithoutQueryVariables()}
	if provider != nil {
		pluginOptions = append(pluginOptions, tracing.WithTracerProvider(provider))
	}
	if err := db.Use(tracing.NewPlugin(pluginOptions...)); err != nil {
		return nil, fmt.Errorf("安装 GORM tracing plugin 失败: %w", err)
	}
	if sqlDB, err := db.DB(); err != nil {
		return nil, err
	} else if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}
	return &Adapter{db: db, idempotency: make(map[string]string)}, nil
}

// NewForDB creates an adapter around a test or externally managed GORM DB.
func NewForDB(db *gorm.DB) (*Adapter, error) {
	if db == nil {
		return nil, errors.New("GORM DB 不能为空")
	}
	if err := db.Use(tracing.NewPlugin(tracing.WithoutQueryVariables())); err != nil {
		return nil, err
	}
	return &Adapter{db: db, idempotency: make(map[string]string)}, nil
}

func (a *Adapter) DB() *gorm.DB { return a.db }

func (a *Adapter) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	db, err := a.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (a *Adapter) AutoMigrate(ctx context.Context) error {
	if a == nil || a.db == nil {
		return errors.New("MySQL adapter 未装配")
	}
	return a.db.WithContext(ctx).AutoMigrate(&User{}, &Product{}, &Order{}, &runLeaseRow{})
}

// Claim atomically creates a lease or replaces an expired lease for one Run.
func (a *Adapter) Claim(ctx context.Context, lease application.RunLease, now time.Time) (bool, error) {
	if err := validateLease(ctx, lease, now); err != nil {
		return false, err
	}
	result := a.db.WithContext(ctx).Exec(
		"INSERT INTO run_leases (run_id, owner_token, expires_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE owner_token = IF(expires_at <= ?, VALUES(owner_token), owner_token), expires_at = IF(expires_at <= ?, VALUES(expires_at), expires_at)",
		lease.RunID, lease.OwnerToken, lease.ExpiresAt, now, now,
	)
	if result.Error != nil {
		return false, result.Error
	}
	return a.Owns(ctx, lease, now)
}

func (a *Adapter) Owns(ctx context.Context, lease application.RunLease, now time.Time) (bool, error) {
	var count int64
	err := a.db.WithContext(ctx).Model(&runLeaseRow{}).
		Where("run_id = ? AND owner_token = ? AND expires_at > ?", lease.RunID, lease.OwnerToken, now).
		Count(&count).Error
	return count == 1, err
}

func (a *Adapter) Release(ctx context.Context, lease application.RunLease) error {
	return a.db.WithContext(ctx).Where("run_id = ? AND owner_token = ?", lease.RunID, lease.OwnerToken).Delete(&runLeaseRow{}).Error
}

func validateLease(ctx context.Context, lease application.RunLease, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease.RunID == "" || lease.OwnerToken == "" || !lease.ExpiresAt.After(now) {
		return errors.New("run lease 参数非法")
	}
	return nil
}

// Seed inserts a tiny deterministic local fixture. It is opt-in and safe to
// repeat because IDs are fixed and FirstOrCreate is used.
func (a *Adapter) Seed(ctx context.Context) error {
	if a == nil || a.db == nil {
		return errors.New("MySQL adapter 未装配")
	}
	users := []User{{ID: 1, Name: "demo-user"}, {ID: 2, Name: "demo-user-2"}}
	for _, user := range users {
		if err := a.db.WithContext(ctx).FirstOrCreate(&user, User{ID: user.ID}).Error; err != nil {
			return err
		}
	}
	products := []Product{{ID: 1, Name: "demo-product", Price: "19.90", Stock: 100}, {ID: 2, Name: "demo-product-2", Price: "9.50", Stock: 50}}
	for _, product := range products {
		if err := a.db.WithContext(ctx).FirstOrCreate(&product, Product{ID: product.ID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) Query(ctx context.Context, raw []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	input, err := decodeQuery(raw)
	if err != nil {
		return "", err
	}
	query := a.db.WithContext(ctx).Model(&Order{})
	if input.UserID != nil {
		query = query.Where("user_id = ?", *input.UserID)
	} else {
		query = query.Where("id = ?", *input.OrderID)
	}
	var orders []Order
	if err := query.Order("id asc").Limit(100).Find(&orders).Error; err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Orders []Order `json:"orders"`
	}{Orders: orders})
	return string(encoded), err
}

func (a *Adapter) Insert(ctx context.Context, raw []byte, idempotencyKey string) (string, error) {
	input, err := decodeInsert(raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return "", errors.New("MySQL insert 缺少幂等键")
	}
	// ponytail: one process-wide insert lock keeps the in-memory idempotency
	// window correct; use a durable unique key before multi-process deployment.
	a.mu.Lock()
	defer a.mu.Unlock()
	if result, ok := a.idempotency[idempotencyKey]; ok {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	order := Order{UserID: input.UserID, ProductID: input.ProductID, Quantity: input.Quantity, TotalAmount: normalizeAmount(input.TotalAmount)}
	if err := a.db.WithContext(ctx).Create(&order).Error; err != nil {
		return "", err
	}
	resultBytes, err := json.Marshal(map[string]any{"order_id": order.ID, "user_id": order.UserID, "product_id": order.ProductID, "quantity": order.Quantity, "total_amount": order.TotalAmount})
	if err != nil {
		return "", err
	}
	result := string(resultBytes)
	a.idempotency[idempotencyKey] = result
	return result, nil
}

func decodeQuery(raw []byte) (queryInput, error) {
	if len(raw) == 0 || len(raw) > maxPayloadBytes {
		return queryInput{}, errors.New("MySQL query 输入大小非法")
	}
	var input queryInput
	if err := decodeStrict(raw, &input); err != nil {
		return queryInput{}, err
	}
	if (input.UserID == nil) == (input.OrderID == nil) || (input.UserID != nil && *input.UserID == 0) || (input.OrderID != nil && *input.OrderID == 0) {
		return queryInput{}, errors.New("MySQL query 必须恰好指定一个正数 ID")
	}
	return input, nil
}

func decodeInsert(raw []byte) (insertInput, error) {
	if len(raw) == 0 || len(raw) > maxPayloadBytes {
		return insertInput{}, errors.New("MySQL insert 输入大小非法")
	}
	var input insertInput
	if err := decodeStrict(raw, &input); err != nil {
		return insertInput{}, err
	}
	if input.UserID == 0 || input.ProductID == 0 || input.Quantity <= 0 || !amountPattern.MatchString(input.TotalAmount) {
		return insertInput{}, errors.New("MySQL insert 字段非法")
	}
	return input, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("MySQL 输入 JSON 非法: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("MySQL 输入包含尾随数据")
	}
	return nil
}

func normalizeAmount(value string) string {
	if !strings.Contains(value, ".") {
		return value + ".00"
	}
	parts := strings.SplitN(value, ".", 2)
	return parts[0] + "." + parts[1] + strings.Repeat("0", 2-len(parts[1]))
}
