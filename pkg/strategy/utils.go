package strategy

import (
	"time"

	"tellurion/pkg/domain"
)

func IsRebalanceDay(date time.Time, frequency string) bool {
	switch frequency {
	case "weekly":
		if date.Weekday() == time.Monday {
			return true
		}
		prevDay := date.AddDate(0, 0, -1)
		if prevDay.Weekday() == time.Sunday && date.Weekday() == time.Tuesday {
			return true
		}
		if prevDay.Weekday() == time.Saturday && date.Weekday() == time.Monday {
			return true
		}
		return false
	case "monthly":
		if date.Day() == 1 {
			return true
		}
		if date.Day() <= 3 {
			prevDay := date.AddDate(0, 0, -1)
			if prevDay.Month() != date.Month() {
				return true
			}
		}
		return false
	case "daily", "":
		return true
	default:
		return true
	}
}

// LatestBarDate 从行情里取最新的一根 bar 的日期。
//
// 它是判断「今天」最可靠的来源：日期来自被回放的数据本身，而不是墙钟。
// 回测里任何依赖墙钟的判断都会让结果随运行日期漂移 —— weekly 周一跑有
// 信号、周四跑零成交（P1-12）。取不到日期返回零值，调用方应据此拒绝
// 生成信号，而不是退回 time.Now()。
func LatestBarDate(bars map[string][]domain.OHLCV) time.Time {
	var latest time.Time
	for _, series := range bars {
		if len(series) == 0 {
			continue
		}
		if d := series[len(series)-1].Date; d.After(latest) {
			latest = d
		}
	}
	return latest
}
