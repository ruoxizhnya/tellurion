package source

// DBT-13 的解析层单测（改之后的新出口）。
//
// 判据的主体是「**永不返回今天**」：不可解析的输入必须换回 ok=false + 零值，
// 而不是一个看起来合理的日期 —— 零值这一条就是回落写回来的当场反应，
// 链上「折成今天」的整类后果由 dbt13_adapter_date_fidelity_test.go 在
// 适配器边界那侧钉住。

import (
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
		assert.True(t, got.IsZero(),
			"%v：读不懂时返回值必须是零值 —— 交出一个日期（尤其是一个离今天很近的日期）就是回落又回来了", in)
	}
}

func TestParseAnnouncementTime_ReadsTheShapeTheSourceSends(t *testing.T) {
	t.Parallel()

	// 主腿是 announcementTime（源实际给的形状），兜底腿是 adjunctUrl 的日期段；
	// 量级不对的那条腿要整条作废再走兜底，而不是折成 1970。
	// 「等于今天」这一类后果不在这里断言 —— 那是适配器层夹具测试的职责。
	cases := map[string]struct {
		row      map[string]interface{}
		wantDay  time.Time
		wantRead bool
	}{
		"epoch_millis_with_url": {
			row:      juchaoRow("1", "T", float64(juchaoFixtureEpochMS), "finalpage/2026-08-15/1.PDF"),
			wantDay:  dayUTC(2026, 8, 15),
			wantRead: true,
		},
		"epoch_millis_alone": {
			row:      juchaoRow("1", "T", float64(juchaoFixtureEpochMS), ""),
			wantDay:  dayUTC(2026, 8, 15),
			wantRead: true,
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
			row:      juchaoRow("1", "T", float64(juchaoFixtureEpochMS/1000), "finalpage/2026-08-15/1.PDF"),
			wantDay:  dayUTC(2026, 8, 15),
			wantRead: true,
		},
		// 量级自检的另一侧：秒测的是**下界**（1970），下面两条测的是**上界**。
		// 上界这条在第二轮审查前是没人管的 —— 把 `y > 2100` 改成 `y > 200000` 当时的全套测试全绿。
		"epoch_micros_falls_back_to_url": {
			row:      juchaoRow("1", "T", float64(juchaoFixtureEpochMS*1000), "finalpage/2026-08-15/1.PDF"),
			wantDay:  dayUTC(2026, 8, 15),
			wantRead: true,
		},
		"epoch_micros_alone_is_not_read_as_year_58000": {
			row:      juchaoRow("1", "T", float64(juchaoFixtureEpochMS*1000), ""),
			wantRead: false,
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
			require.True(t, ok, "%s：这是源给过的形状，读不懂就是解析退化", name)
			assert.Equal(t, tc.wantDay, got.UTC().Truncate(24*time.Hour))
		})
	}
}

// 交易所日历日，不是 UTC 瞬时。cninfo 的 1786723200000 是**北京时间的午夜**
// （= 2026-08-14T16:00:00Z）；按瞬时取日会退一天，而 adjunctUrl 明写 08-15。
// 落库列 `announcements.ann_time` 是 TIMESTAMPTZ，但这条链声明的语义是
// `VisibleOnAnnouncementDay` + `FreqSpecEvent`（按**公告日**可见），所以取
// 「+08 看见的那一天、落 UTC 零点」：UTC 与 +08 两侧读回来都是 08-15。
// 这条钉子防的就是「顺手把 FixedZone 改成 UTC」这种改动。
func TestParseAnnouncementTime_KeepsExchangeCalendarDayNotUTCInstant(t *testing.T) {
	t.Parallel()

	got, ok := parseAnnouncementTime(juchaoRow("1", "T", float64(juchaoFixtureEpochMS), ""))
	require.True(t, ok)
	assert.Equal(t, "2026-08-15", got.UTC().Format("2006-01-02"),
		"退到 08-14 就是按 UTC 瞬时取日 —— 与 adjunctUrl 明写的公告日差一天")
	assert.True(t, got.Location() == time.UTC, "归一在 UTC 零点，落库结果才与跑作业的机器时区无关")
}
