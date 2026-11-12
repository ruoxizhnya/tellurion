package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"tellurion/pkg/storage"
)

// resultRetentionLoop 是进程内的回测结果保留期清理任务（CORE-RESULT-RETENTION）。
//
// 形态对齐 PeriodicAlertLoop / ReconciliationWorker：Start(ctx) 阻塞在 ticker 上，
// cancel 后最多一个在途 pass 结束即退出。每次 pass 只处理 batch_size 个 run，
// 失败停在已提交前缀，下一次续跑。
type resultRetentionLoop struct {
	store    *storage.PostgresStore
	cfg      storage.RetentionConfig
	interval time.Duration
	logger   zerolog.Logger
}

// buildResultRetention 从 result_retention.* 构建清理任务。
//
// 关闭开关不存在时默认启用（默认值就是 90/365）；store 为空或显式
// enabled=false 时返回 nil，调用方照 alert/recon 的 nil 姿态不启动。
func buildResultRetention(v *viper.Viper, store *storage.PostgresStore, logger zerolog.Logger) *resultRetentionLoop {
	if store == nil {
		return nil
	}
	enabled := true
	if v.IsSet("result_retention.enabled") {
		enabled = v.GetBool("result_retention.enabled")
	}
	if !enabled {
		return nil
	}
	interval := v.GetDuration("result_retention.interval")
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &resultRetentionLoop{
		store: store,
		cfg: storage.RetentionConfig{
			DetailDays:  v.GetInt("result_retention.detail_days"),
			SummaryDays: v.GetInt("result_retention.summary_days"),
			BatchSize:   v.GetInt("result_retention.batch_size"),
			DryRun:      v.GetBool("result_retention.dry_run"),
		},
		interval: interval,
		logger:   logger.With().Str("component", "result_retention").Logger(),
	}
}

// Start 进入周期清理，直到 ctx 取消。
func (l *resultRetentionLoop) Start(ctx context.Context) {
	l.logger.Info().
		Dur("interval", l.interval).
		Bool("dry_run", l.cfg.DryRun).
		Int("detail_days", l.cfg.DetailDays).
		Int("summary_days", l.cfg.SummaryDays).
		Int("batch_size", l.cfg.BatchSize).
		Msg("result retention loop started")

	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.logger.Info().Msg("result retention loop stopped")
			return
		case <-ticker.C:
			l.runOnce(ctx)
		}
	}
}

func (l *resultRetentionLoop) runOnce(ctx context.Context) {
	report, err := l.store.ReapBacktestResults(ctx, l.cfg)
	if err != nil {
		// 已提交前缀保留；下一次从剩余候选续跑，不会半删。
		l.logger.Error().Err(err).
			Int("detail_runs", report.DetailRuns).
			Int("summary_runs", report.SummaryRuns).
			Msg("result retention pass failed after committed prefix")
	}
	l.logger.Info().
		Bool("dry_run", report.DryRun).
		Time("now", report.Now).
		Time("detail_cutoff", report.DetailCutoff).
		Time("summary_cutoff", report.SummaryCutoff).
		Int("detail_runs", report.DetailRuns).
		Int("summary_runs", report.SummaryRuns).
		Int("detail_trades", report.DetailTrades).
		Int("detail_equity_points", report.DetailEquityPoints).
		Int("skipped", report.Skipped).
		Msg("result retention pass completed")
	for _, a := range report.Actions {
		l.logger.Info().
			Str("run_id", a.RunID).
			Str("action", a.Action).
			Int("trades", a.Trades).
			Int("equity_points", a.EquityPoints).
			Str("skip_reason", a.SkipReason).
			Msg("result retention action")
	}
}
