package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/config"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrNotFound = errors.New("NotFound")

type Repository interface {
	UpsertCodespec(ctx context.Context, name string, codespec model.Codespec) (version int, err error)
	GetLatestCodespec(ctx context.Context, name string) (codespec *model.Codespec, version *int, err error)
	GetCodespecVersion(ctx context.Context, name string, version int) (*model.Codespec, error)
	HealthCheck(ctx context.Context) error
}

type repository struct {
	db *gorm.DB
}

func Connect(config config.ConfigSpec) (Repository, error) {
	connectionString := config.Database.ConnectionString
	if config.Database.Password != "" {
		// add on the password (which could/should be sourced from a file)
		connectionString = fmt.Sprintf("%s password=%s", connectionString, config.Database.Password)
	}

	db, err := gorm.Open(
		postgres.Open(connectionString),
		&gorm.Config{
			Logger: logger.New(
				&log.Logger,
				logger.Config{
					LogLevel: logger.Info,
					Colorful: false,
				}),
		})

	if err != nil {
		return nil, err
	}

	err = db.Migrator().AutoMigrate(&codespec{})
	return repository{db}, err
}

type codespec struct {
	Name      string `gorm:"primaryKey"`
	Version   int    `gorm:"primaryKey"`
	CreatedAt time.Time
	Spec      []byte `sql:"type:jsonb"`
}

func (r repository) UpsertCodespec(ctx context.Context, name string, codespec model.Codespec) (version int, err error) {
	specBytes, err := json.Marshal(codespec)
	if err != nil {
		return 0, err
	}

	err = r.db.Raw(`
		INSERT INTO codespecs
		SELECT
			@name,
			CASE WHEN MAX(version) IS NULL THEN 1 ELSE MAX(version) + 1 END,
			@time,
			@spec
		FROM codespecs where name = @name
		RETURNING version`,
		sql.Named("name", name),
		sql.Named("time", time.Now().UTC()),
		sql.Named("spec", specBytes)).
		Scan(&version).Error

	return version, err
}

func (r repository) GetLatestCodespec(ctx context.Context, name string) (*model.Codespec, *int, error) {
	rows, err := r.db.Raw(`
		SELECT version, spec FROM codespecs
		WHERE name = ?
		ORDER BY version DESC
		LIMIT 1`,
		name).Rows()

	if err != nil {
		return nil, nil, err
	}

	defer rows.Close()

	if !rows.Next() {
		return nil, nil, ErrNotFound
	}

	var codespecBytes []byte
	var version int

	if err := rows.Scan(&version, &codespecBytes); err != nil {
		return nil, nil, err
	}

	var codespec model.Codespec

	if err := json.Unmarshal(codespecBytes, &codespec); err != nil {
		return nil, nil, err
	}

	return &codespec, &version, nil
}

func (r repository) GetCodespecVersion(ctx context.Context, name string, version int) (*model.Codespec, error) {
	row := codespec{Name: name, Version: version}
	if err := r.db.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}

		return nil, err
	}

	codespec := model.Codespec{}
	err := json.Unmarshal(row.Spec, &codespec)
	return &codespec, err
}

func (r repository) HealthCheck(ctx context.Context) error {
	s := r.db.Exec("SELECT NULL from codespecs LIMIT 1")
	err := s.Error
	if err != nil {
		log.Ctx(ctx).Err(err).Msg("Database health check failed")
		return errors.New("failed to connect to database")
	}
	return nil
}
