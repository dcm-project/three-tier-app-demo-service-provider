package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	_ "github.com/lib/pq"
)

type pgStore struct {
	db *sql.DB
}

// NewPostgresStore opens a PostgreSQL database using the given DSN and returns an AppStore.
// DSN format: "host=<h> port=<p> user=<u> password=<pw> dbname=<db> sslmode=disable"
func NewPostgresStore(dsn string) (AppStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pgMigrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	db.SetMaxIdleConns(10)
	db.SetMaxOpenConns(100)
	db.SetConnMaxLifetime(time.Hour)
	return &pgStore{db: db}, nil
}

func pgMigrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS three_tier_apps (
		id           TEXT PRIMARY KEY,
		path         TEXT NOT NULL,
		status       TEXT NOT NULL,
		spec_json    TEXT NOT NULL,
		web_endpoint TEXT NOT NULL DEFAULT '',
		create_time  TIMESTAMPTZ NOT NULL,
		update_time  TIMESTAMPTZ NOT NULL
	)`)
	if err != nil {
		return err
	}
	// For databases created before web_endpoint was introduced.
	_, _ = db.Exec(`ALTER TABLE three_tier_apps ADD COLUMN IF NOT EXISTS web_endpoint TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *pgStore) Create(_ context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error) {
	spec, err := json.Marshal(app.Spec)
	if err != nil {
		return app, fmt.Errorf("marshal spec: %w", err)
	}
	status := ""
	if app.Status != nil {
		status = string(*app.Status)
	}
	webEndpoint := ""
	if app.WebEndpoint != nil {
		webEndpoint = *app.WebEndpoint
	}
	_, err = s.db.Exec(
		`INSERT INTO three_tier_apps (id, path, status, spec_json, web_endpoint, create_time, update_time)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		*app.Id, *app.Path, status, string(spec), webEndpoint,
		app.CreateTime.UTC(), app.UpdateTime.UTC(),
	)
	if err != nil {
		if isPostgresConflict(err) {
			return app, ErrAlreadyExists
		}
		return app, fmt.Errorf("insert: %w", err)
	}
	return app, nil
}

func (s *pgStore) Get(_ context.Context, id string) (v1alpha1.ThreeTierApp, bool) {
	row := s.db.QueryRow(
		`SELECT id, path, status, spec_json, web_endpoint, create_time, update_time
		 FROM three_tier_apps WHERE id = $1`, id)
	app, err := pgScanApp(row)
	if err != nil {
		return v1alpha1.ThreeTierApp{}, false
	}
	return app, true
}

func (s *pgStore) List(_ context.Context, maxPageSize, offset int) ([]v1alpha1.ThreeTierApp, bool) {
	rows, err := s.db.Query(
		`SELECT id, path, status, spec_json, web_endpoint, create_time, update_time
		 FROM three_tier_apps ORDER BY create_time LIMIT $1 OFFSET $2`,
		maxPageSize+1, offset)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var list []v1alpha1.ThreeTierApp
	for rows.Next() {
		app, err := pgScanApp(rows)
		if err != nil {
			continue
		}
		list = append(list, app)
	}
	if len(list) > maxPageSize {
		return list[:maxPageSize], true
	}
	return list, false
}

func (s *pgStore) Update(_ context.Context, app v1alpha1.ThreeTierApp) (v1alpha1.ThreeTierApp, error) {
	status := ""
	if app.Status != nil {
		status = string(*app.Status)
	}
	webEndpoint := ""
	if app.WebEndpoint != nil {
		webEndpoint = *app.WebEndpoint
	}
	res, err := s.db.Exec(
		`UPDATE three_tier_apps SET status = $1, web_endpoint = $2, update_time = $3 WHERE id = $4`,
		status, webEndpoint, app.UpdateTime.UTC(), *app.Id,
	)
	if err != nil {
		return app, fmt.Errorf("update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return app, ErrNotFound
	}
	return app, nil
}

func (s *pgStore) Delete(_ context.Context, id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM three_tier_apps WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func pgScanApp(s scanner) (v1alpha1.ThreeTierApp, error) {
	var (
		id, path, status, specJSON, webEndpoint string
		createTime, updateTime                  time.Time
	)
	if err := s.Scan(&id, &path, &status, &specJSON, &webEndpoint, &createTime, &updateTime); err != nil {
		return v1alpha1.ThreeTierApp{}, err
	}
	var spec v1alpha1.ThreeTierSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return v1alpha1.ThreeTierApp{}, fmt.Errorf("unmarshal spec: %w", err)
	}
	st := v1alpha1.ThreeTierAppStatus(status)
	var we *string
	if webEndpoint != "" {
		we = &webEndpoint
	}
	return v1alpha1.ThreeTierApp{
		Id:          &id,
		Path:        &path,
		Spec:        spec,
		Status:      &st,
		WebEndpoint: we,
		CreateTime:  &createTime,
		UpdateTime:  &updateTime,
	}, nil
}

func isPostgresConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate key value violates unique constraint")
}
