package hwmonreceiver

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/scraper/scrapererror"
	"go.uber.org/zap"
)

type hwmonScraper struct {
	collector collector
	logger    *zap.Logger

	// Start of the counters, for `node_hwmon_energy_*_joule_total`.
	start_time pcommon.Timestamp
}

func newScraper(c *Config, logger *zap.Logger) *hwmonScraper {
	return &hwmonScraper{
		collector: collector{
			sys_path:      c.sysPath(),
			chip_filter:   newFilter(c.ChipExclude, c.ChipInclude),
			sensor_filter: newFilter(c.SensorExclude, c.SensorInclude),
			debug: func(msg string, kv ...any) {
				logger.Sugar().Debugw(msg, kv...)
			},
		},
		logger: logger,
	}
}

func (s *hwmonScraper) start(_ context.Context, _ component.Host) error {
	s.start_time = pcommon.NewTimestampFromTime(time.Now())
	return nil
}

func (s *hwmonScraper) scrape(_ context.Context) (pmetric.Metrics, error) {
	samples := []sample{}
	err := s.collector.update(func(v sample) { samples = append(samples, v) })
	if errors.Is(err, errNoData) {
		return pmetric.NewMetrics(), nil
	}

	md := toMetrics(samples, s.start_time, pcommon.NewTimestampFromTime(time.Now()))
	if err != nil {
		// Chips read before the failing one are still reported.
		return md, scrapererror.NewPartialScrapeError(err, 0)
	}
	return md, nil
}

// toMetrics groups samples by name into one metric each, ordered by name and then labels.
// A repeated name and label set is dropped, as the Prometheus registry rejects it.
func toMetrics(samples []sample, start_time, ts pcommon.Timestamp) pmetric.Metrics {
	slices.SortStableFunc(samples, func(a, b sample) int {
		return cmp.Or(
			strings.Compare(a.name, b.name),
			slices.CompareFunc(a.labels, b.labels, func(x, y label) int {
				return cmp.Or(strings.Compare(x.key, y.key), strings.Compare(x.value, y.value))
			}),
		)
	})

	md := pmetric.NewMetrics()
	sm := md.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty()
	sm.Scope().SetName(ScopeName)

	var (
		m    pmetric.Metric
		dps  pmetric.NumberDataPointSlice
		prev *sample
	)
	for i := range samples {
		v := &samples[i]
		if prev != nil && prev.name == v.name && slices.Equal(prev.labels, v.labels) {
			continue
		}
		if prev == nil || prev.name != v.name {
			m = sm.Metrics().AppendEmpty()
			m.SetName(v.name)
			m.SetDescription(v.help)
			if v.counter {
				sum := m.SetEmptySum()
				sum.SetIsMonotonic(true)
				sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
				dps = sum.DataPoints()
			} else {
				dps = m.SetEmptyGauge().DataPoints()
			}
		}
		prev = v

		dp := dps.AppendEmpty()
		dp.SetTimestamp(ts)
		if v.counter {
			dp.SetStartTimestamp(start_time)
		}
		dp.SetDoubleValue(v.value)
		for _, l := range v.labels {
			dp.Attributes().PutStr(l.key, l.value)
		}
	}

	return md
}
