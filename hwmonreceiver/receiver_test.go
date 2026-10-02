package hwmonreceiver_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/hwmon-receiver/hwmonreceiver"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestConfigValidate(t *testing.T) {
	f := hwmonreceiver.NewFactory()
	if err := componenttest.CheckConfigStruct(f.CreateDefaultConfig()); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tcs := []struct {
		desc  string
		edit  func(c *hwmonreceiver.Config)
		valid bool
	}{
		{"default", func(c *hwmonreceiver.Config) {}, true},
		{"root path", func(c *hwmonreceiver.Config) { c.RootPath = t.TempDir() }, true},
		{"relative root path", func(c *hwmonreceiver.Config) { c.RootPath = "hostfs" }, false},
		{"missing root path", func(c *hwmonreceiver.Config) { c.RootPath = t.TempDir() + "/missing" }, false},
		{"root path is a file", func(c *hwmonreceiver.Config) { c.RootPath = file }, false},
		{"chip include", func(c *hwmonreceiver.Config) { c.ChipInclude = "^tmp451" }, true},
		{"chip include and exclude", func(c *hwmonreceiver.Config) {
			c.ChipInclude = "a"
			c.ChipExclude = "b"
		}, false},
		{"sensor include and exclude", func(c *hwmonreceiver.Config) {
			c.SensorInclude = "a"
			c.SensorExclude = "b"
		}, false},
		{"invalid regexp", func(c *hwmonreceiver.Config) { c.SensorExclude = "(" }, false},
	}
	for _, tc := range tcs {
		t.Run(tc.desc, func(t *testing.T) {
			c := f.CreateDefaultConfig().(*hwmonreceiver.Config)
			tc.edit(c)
			err := c.Validate()
			if tc.valid && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestReceiver(t *testing.T) {
	// A chip without a device, so it is named after its `name` file.
	root := t.TempDir()
	chip := filepath.Join(root, "sys/class/hwmon/hwmon0")
	if err := os.MkdirAll(chip, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"name":          "fake\n",
		"temp1_input":   "42500\n",
		"temp1_label":   "board\n",
		"energy1_input": "3000000\n",
	} {
		if err := os.WriteFile(filepath.Join(chip, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	f := hwmonreceiver.NewFactory()
	c := f.CreateDefaultConfig().(*hwmonreceiver.Config)
	c.RootPath = root
	c.CollectionInterval = 50 * time.Millisecond
	c.InitialDelay = 0
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	sink := new(consumertest.MetricsSink)
	r, err := f.CreateMetrics(t.Context(), receivertest.NewNopSettings(hwmonreceiver.Type), c, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(t.Context(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(t.Context())

	deadline := time.Now().Add(5 * time.Second)
	for len(sink.AllMetrics()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no metrics received")
		}
		time.Sleep(10 * time.Millisecond)
	}

	md := sink.AllMetrics()[0]
	ms := md.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	got := map[string]pmetric.Metric{}
	for i := range ms.Len() {
		got[ms.At(i).Name()] = ms.At(i)
	}

	names := []string{
		"node_hwmon_chip_names",
		"node_hwmon_energy_joule_total",
		"node_hwmon_sensor_label",
		"node_hwmon_temp_celsius",
	}
	if len(got) != len(names) {
		t.Fatalf("want %v, got %d metrics", names, len(got))
	}
	for _, name := range names {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %s", name)
		}
	}

	temp := got["node_hwmon_temp_celsius"]
	if temp.Type() != pmetric.MetricTypeGauge {
		t.Fatalf("temperature is %s, want gauge", temp.Type())
	}
	dp := temp.Gauge().DataPoints().At(0)
	if dp.DoubleValue() != 42.5 {
		t.Fatalf("temperature: got %v, want 42.5", dp.DoubleValue())
	}
	want_attrs := map[string]any{"chip": "fake", "sensor": "temp1"}
	if attrs := dp.Attributes().AsRaw(); len(attrs) != 2 || attrs["chip"] != want_attrs["chip"] || attrs["sensor"] != want_attrs["sensor"] {
		t.Fatalf("temperature attributes: got %v, want %v", attrs, want_attrs)
	}

	energy := got["node_hwmon_energy_joule_total"]
	if energy.Type() != pmetric.MetricTypeSum {
		t.Fatalf("energy is %s, want sum", energy.Type())
	}
	sum := energy.Sum()
	if !sum.IsMonotonic() || sum.AggregationTemporality() != pmetric.AggregationTemporalityCumulative {
		t.Fatal("energy must be a monotonic cumulative sum")
	}
	edp := sum.DataPoints().At(0)
	if edp.DoubleValue() != 3 {
		t.Fatalf("energy: got %v, want 3", edp.DoubleValue())
	}
	if edp.StartTimestamp() == 0 || edp.StartTimestamp() > edp.Timestamp() {
		t.Fatalf("energy start timestamp %v must be set and not after %v", edp.StartTimestamp(), edp.Timestamp())
	}
}

func TestReceiverWithoutHwmon(t *testing.T) {
	f := hwmonreceiver.NewFactory()
	c := f.CreateDefaultConfig().(*hwmonreceiver.Config)
	c.RootPath = t.TempDir()
	c.CollectionInterval = 50 * time.Millisecond
	c.InitialDelay = 0

	sink := new(consumertest.MetricsSink)
	r, err := f.CreateMetrics(t.Context(), receivertest.NewNopSettings(hwmonreceiver.Type), c, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(t.Context(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := sink.DataPointCount(); n != 0 {
		t.Fatalf("want no data points, got %d", n)
	}
}
