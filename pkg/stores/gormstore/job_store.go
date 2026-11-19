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

func (s *JobStore) GetJob(ctx context.Context, id string) (job.Job, error) {
	var j job.Job
	err := s.db.WithContext(ctx).First(&j, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return job.Job{}, job.ErrNotFound
	}
	return j, err
}

// SaveJob UPSERT 任务行（含定义字段，代码升级改调度时同步到库；
// HTTP 动态任务全量定义亦经此持久化）。
func (s *JobStore) SaveJob(ctx context.Context, j job.Job) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name", "description", "interval_sec", "cron_expr", "kind",
			"url", "method", "payload",
			"enabled", "last_run", "last_result", "last_err", "next_run",
		}),
	}).Create(&j).Error
}

func (s *JobStore) ListJobs(ctx context.Context) ([]job.Job, error) {
	var out []job.Job
	err := s.db.WithContext(ctx).Order("id").Find(&out).Error
	return list(out), err
}

// DeleteJob 删除任务（动态任务删除；运行记录随之外键无约束残留
// 由 ListRuns 的 job_id 过滤自然隔离，保留历史可审计）。
func (s *JobStore) DeleteJob(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&job.Job{}).Error
}

// SaveRun 追加执行记录（Seq 由自增回填）。
func (s *JobStore) SaveRun(ctx context.Context, rec job.RunRecord) (job.RunRecord, error) {
	if err := s.db.WithContext(ctx).Create(&rec).Error; err != nil {
		return job.RunRecord{}, err
	}
	return rec, nil
}

// ListRuns 分页查询执行日志（倒序最新在前 + 总数）。
func (s *JobStore) ListRuns(ctx context.Context, jobID string, offset, limit int) ([]job.RunRecord, int, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&job.RunRecord{}).
		Where("job_id = ?", jobID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []job.RunRecord
	q := s.db.WithContext(ctx).Where("job_id = ?", jobID).
		Order("seq DESC")
	if offset > 0 {
		q = q.Offset(offset)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}
