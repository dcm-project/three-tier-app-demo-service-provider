package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ThreeTierAppRecord is the gorm model for persisting a ThreeTierApp.
type ThreeTierAppRecord struct {
	ID          string                 `gorm:"column:id;primaryKey"`
	Path        string                 `gorm:"column:path;not null"`
	Status      string                 `gorm:"column:status;not null"`
	Spec        v1alpha1.ThreeTierSpec `gorm:"column:spec_json;serializer:json;not null"`
	WebEndpoint string                 `gorm:"column:web_endpoint;not null;default:''"`
	CreateTime  time.Time              `gorm:"column:create_time;autoCreateTime"`
	UpdateTime  time.Time              `gorm:"column:update_time;autoUpdateTime"`
}

// TableName overrides the default gorm table name.
func (ThreeTierAppRecord) TableName() string { return "three_tier_apps" }

// New opens a database connection, runs AutoMigrate, and returns an AppStore.
// cfg.Type selects the backend: "pgsql" (default) or "sqlite".
// logLevel is the application log level string (e.g. "info", "debug").
func New(cfg config.StoreConfig, logLevel string) (AppStore, error) {
	var dialector gorm.Dialector
	if cfg.Type == "pgsql" {
		dsn := fmt.Sprintf("host=%s user=%s password=%s port=%s dbname=%s",
			cfg.Host, cfg.User, cfg.Pass, cfg.Port, cfg.Name)
		dialector = pgdriver.Open(dsn)
	} else {
		dialector = sqlite.Open(cfg.Path)
	}

	gormLevel, slogLevel := gormLogLevelFromString(logLevel)
	gormLogger := logger.New(
		slog.NewLogLogger(slog.Default().Handler(), slogLevel),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  gormLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:         gormLogger,
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying db: %w", err)
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	slog.Info("Database connection established", "type", cfg.Type)

	if err := db.AutoMigrate(&ThreeTierAppRecord{}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	slog.Info("Database schema migrated")

	return &gormStore{db: db}, nil
}

type gormStore struct {
	db *gorm.DB
}

func (s *gormStore) Create(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error) {
	rec := toRecord(app)
	if err := s.db.WithContext(ctx).Create(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return app, ErrAlreadyExists
		}
		return app, fmt.Errorf("create: %w", err)
	}
	return fromRecord(rec), nil
}

func (s *gormStore) Get(ctx context.Context, id string) (v1alpha1.ThreeTierApp, bool) {
	var rec ThreeTierAppRecord
	if err := s.db.WithContext(ctx).First(&rec, "id = ?", id).Error; err != nil {
		return v1alpha1.ThreeTierApp{}, false
	}
	return fromRecord(rec), true
}

func (s *gormStore) List(ctx context.Context, maxPageSize, offset int) ([]v1alpha1.ThreeTierApp, bool) {
	var recs []ThreeTierAppRecord
	if err := s.db.WithContext(ctx).
		Order("create_time ASC").
		Limit(maxPageSize + 1).
		Offset(offset).
		Find(&recs).Error; err != nil {
		return nil, false
	}
	hasMore := len(recs) > maxPageSize
	if hasMore {
		recs = recs[:maxPageSize]
	}
	list := make([]v1alpha1.ThreeTierApp, len(recs))
	for i, r := range recs {
		list[i] = fromRecord(r)
	}
	return list, hasMore
}

func (s *gormStore) Update(ctx context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error) {
	status := ""
	if app.Status != nil {
		status = string(*app.Status)
	}
	webEndpoint := ""
	if app.WebEndpoint != nil {
		webEndpoint = *app.WebEndpoint
	}
	result := s.db.WithContext(ctx).
		Model(&ThreeTierAppRecord{}).
		Where("id = ?", *app.Id).
		Select("status", "web_endpoint").
		Updates(&ThreeTierAppRecord{
			Status:      status,
			WebEndpoint: webEndpoint,
		})
	if result.Error != nil {
		return app, fmt.Errorf("update: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return app, ErrNotFound
	}
	var rec ThreeTierAppRecord
	if err := s.db.WithContext(ctx).First(&rec, "id = ?", *app.Id).Error; err != nil {
		return app, fmt.Errorf("reload after update: %w", err)
	}
	return fromRecord(rec), nil
}

func (s *gormStore) Delete(ctx context.Context, id string) error {
	if err := s.db.WithContext(ctx).Where("id = ?", id).Delete(&ThreeTierAppRecord{}).Error; err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

func toRecord(app v1alpha1.ThreeTierApp) ThreeTierAppRecord {
	status := ""
	if app.Status != nil {
		status = string(*app.Status)
	}
	webEndpoint := ""
	if app.WebEndpoint != nil {
		webEndpoint = *app.WebEndpoint
	}
	return ThreeTierAppRecord{
		ID:          *app.Id,
		Path:        *app.Path,
		Status:      status,
		Spec:        app.Spec,
		WebEndpoint: webEndpoint,
	}
}

func fromRecord(rec ThreeTierAppRecord) v1alpha1.ThreeTierApp {
	id := rec.ID
	path := rec.Path
	st := v1alpha1.ThreeTierAppStatus(rec.Status)
	var we *string
	if rec.WebEndpoint != "" {
		we = &rec.WebEndpoint
	}
	return v1alpha1.ThreeTierApp{
		Id:          &id,
		Path:        &path,
		Spec:        rec.Spec,
		Status:      &st,
		WebEndpoint: we,
		CreateTime:  &rec.CreateTime,
		UpdateTime:  &rec.UpdateTime,
	}
}

// gormLogLevelFromString maps the application log level to gorm and slog levels.
func gormLogLevelFromString(level string) (logger.LogLevel, slog.Level) {
	switch strings.ToLower(level) {
	case "debug":
		return logger.Info, slog.LevelDebug
	case "info":
		return logger.Warn, slog.LevelWarn
	case "warn", "warning":
		return logger.Warn, slog.LevelWarn
	case "error":
		return logger.Error, slog.LevelError
	default:
		return logger.Warn, slog.LevelWarn
	}
}
