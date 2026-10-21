package source

// DBT-13 的解析层单测（改之后的新出口）。
//
// 判据的主体是「**永不返回今天**」：不可解析的输入必须换回 ok=false + 零值，
// 而不是一个看起来合理的日期。每条拒绝用例都额外比一次与墙上时钟的距离 ——
// 只要有人把回落写回来，这一条就先红。

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dayUTC(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestParseEastmoneyDateStrict_AcceptsShapesTheSourceActuallySends(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   interface{}
		want time.Time
	}{
		"iso_date":              {in: "2026-05-14", want: dayUTC(2026, 5, 14)},
		"compact_date":          {in: "20260514", want: dayUTC(2026, 5, 14)},
		"datacenter_with_clock": {in: "2026-09-24 00:00:00", want: dayUTC(2026, 9, 24)},
		"numeric_compact":       {in: float64(20260514), want: dayUTC(2026, 5, 14)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := parseEastmoneyDateStrict(tc.in)
			require.True(t, ok, "%s：这是源实际给过的形状，不许当读不懂", name)
			assert.Equal(t, tc.want, got.UTC().Truncate(24*time.Hour))
		})
	}
}

func TestParseEastmoneyDateStrict_RejectsUnknownShapesWithoutGuessingToday(t *testing.T) {
	t.Parallel()

	// 这一族都是真源里出现过的**别的东西**被误当成日期送进来的样子。
	// 特别地：13 位的 epoch 毫秒（juchao 的 announcementTime 形状）落在这里
	// 是 DBT-13 的起因 —— 旧实现把它折成 time.Now()，每条公告都成了今天。
	for _, in := range []interface{}{
		"", "   ", "14/05/2026", "2026-13-45", "not-a-date",
		"finalpage/2026-08-15/1225475868.PDF",
		float64(1786723200000), float64(2026051),
		nil, int(20260514), true,
	} {
		got, ok := parseEastmoneyDateStrict(in)
		assert.False(t, ok, "%v：读不懂就必须说读不懂", in)
		assert.True(t, got.IsZero(), "%v：读不懂时返回值必须是零值，不许是个日期", in)
		if !got.IsZero() {
			assert.Greater(t, math.Abs(got.Sub(time.Now()).Seconds()), 60.0,
				"%v：给出的日期离今天这么近，就是回落又被写回来了", in)
		}
	}
}

func TestParseAnnouncementTime_ReadsTheShapeTheSourceSends(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		row       map[string]interface{}
		wantDay   time.Time
		wantRead  bool
		wantLabel string
	}{
		"epoch_millis_with_url": {
			row:      juchaoRow("1", "T", float64(juchaoFixtureEpochMS), "finalpage/2026-08-15/1.PDF"),
			wantDay:  dayUTC(2026, 8, 15),
			wantRead: true,
		},
		"epoch_millis_alone": {
			row:       juchaoRow("1", "T", float64(juchaoFixtureEpochMS), ""),
			wantDay:   dayUTC(2026, 8, 15),
			wantRead:  true,
			wantLabel: "公告日只靠 announcementTime 也要读得出来，兜底腿不是主腿",
		},
		"adjunct_url_only": {
			row:      juchaoRow("1", "T", nil, "finalpage/2025-03-04/1.PDF"),
			wantDay:  dayUTC(2025, 3, 4),
			wantRead: true,
		},
		"calendar_day_string": {
			row:      juchaoRow("1", "T", "2024-11-02", ""),
			wantDay:  dayUTC(2024, 11, 2),
			wantRead: true,
		},
		"epoch_seconds_falls_back_to_url": {
			row:       juchaoRow("1", "T", float64(juchaoFixtureEpochMS/1000), "finalpage/2026-08-15/1.PDF"),
			wantDay:   dayUTC(2026, 8, 15),
			wantRead:  true,
			wantLabel: "量级不对的那条腿要整条作废，再走兜底 —— 而不是折成 1970",
		},
		"nothing_at_all": {
			row:      juchaoRow("1", "T", nil, ""),
			wantRead: false,
		},
		"url_without_date_segment": {
			row:      juchaoRow("1", "T", nil, "finalpage//1225475868.PDF"),
			wantRead: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := parseAnnouncementTime(tc.row)
			if !tc.wantRead {
				assert.False(t, ok, "%s：两处都读不懂 ⇒ 必须由调用方拒收该行", name)
				assert.True(t, got.IsZero(), "%s：不许交出一个猜出来的日期", name)
				return
			}
			require.True(t, ok, func() string {
				if tc.wantLabel != "" {
					return tc.wantLabel
				}
				return name + "：这是源给过的形状，读不懂就是解析退化"
			}())
			assert.Equal(t, tc.wantDay, got.UTC().Truncate(24*time.Hour), name)
			assert.NotEqual(t, time.Now().UTC().Format("2006-01-02"),
				got.UTC().Format("2006-01-02"), "%s：等于今天说明回落又回来了", name)
		})
	}
}

// 交易所日历日，不是 UTC 瞬时。cninfo 的 1786723200000 是**北京时间的午夜**
// （= 2026-08-14T16:00:00Z）；直接 .UTC() 会退一天，而 adjunctUrl 明写 08-15。
// 这条钉子防的就是「顺手把 FixedZone 改成 UTC」这种改动。
func TestParseAnnouncementTime_KeepsExchangeCalendarDayNotUTCInstant(t *testing.T) {
	t.Parallel()

	got, ok := parseAnnouncementTime(juchaoRow("1", "T", float64(juchaoFixtureEpochMS), ""))
	require.True(t, ok)
	assert.Equal(t, "2026-08-15", got.UTC().Format("2006-01-02"),
		"退到 08-14 就是按 UTC 瞬时取日 —— 落库列是 DATE，日历日必须由交易所一侧决定")
	assert.True(t, got.Location() == time.UTC, "归一在 UTC 零点，落库结果才与跑作业的机器时区无关")
}
