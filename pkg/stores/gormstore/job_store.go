package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/basalt-go/basalt/pkg/modules/job"
)

// JobStore 实现 job.Store（GORM，任意方言）：
// 持久化任务运行态（Enabled/LastRun/LastResult/LastErr/NextRun），
// 定义基准（Name/IntervalSec）来自代码注册，SaveJob 时一并回写。
type JobStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ job.Store = (*JobStore)(nil)

func (s *JobStore) GetJob(_ context.Context, id string) (job.Job, error) {
	var j job.Job
	err := s.db.First(&j, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return job.Job{}, job.ErrNotFound
	}
	return j, err
}

// SaveJob UPSERT 任务行（含定义字段，代码升级改间隔时同步到库）。
func (s *JobStore) SaveJob(_ context.Context, j job.Job) error {
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name", "description", "interval_sec",
			"enabled", "last_run", "last_result", "last_err", "next_run",
		}),
	}).Create(&j).Error
}

func (s *JobStore) ListJobs(_ context.Context) ([]job.Job, error) {
	var out []job.Job
	err := s.db.Order("id").Find(&out).Error
	return out, err
}
