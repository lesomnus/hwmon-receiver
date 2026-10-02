package hwmonreceiver

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Fixtures and expected output come from node_exporter v1.12.0
// (collector/fixtures/sys.ttar and collector/fixtures/e2e-output.txt),
// trimmed to hwmon. The chip filter is the one its end-to-end test uses.
func TestNodeExporterParity(t *testing.T) {
	c := collector{
		sys_path:    "testdata/node_exporter/sys",
		chip_filter: newFilter("", "(applesmc|asus_nb_wmi|coretemp|hwmon4|ieee80211|nct6779)"),
		debug:       func(string, ...any) {},
	}

	samples := []sample{}
	if err := c.update(func(v sample) { samples = append(samples, v) }); err != nil {
		t.Fatal(err)
	}

	got := render(samples)
	want := readLines(t, "testdata/node_exporter/expected.prom")
	if !slices.Equal(got, want) {
		for _, l := range want {
			if !slices.Contains(got, l) {
				t.Errorf("missing: %s", l)
			}
		}
		for _, l := range got {
			if !slices.Contains(want, l) {
				t.Errorf("unexpected: %s", l)
			}
		}
	}
}

func TestNoHwmon(t *testing.T) {
	c := collector{sys_path: t.TempDir(), debug: func(string, ...any) {}}
	if err := c.update(func(sample) {}); err != errNoData {
		t.Fatalf("want errNoData, got %v", err)
	}
}

func TestSensorFilter(t *testing.T) {
	c := collector{
		sys_path:      "testdata/node_exporter/sys",
		chip_filter:   newFilter("", "^platform_coretemp_0$"),
		sensor_filter: newFilter(`;temp[23]$`, ""),
		debug:         func(string, ...any) {},
	}

	sensors := map[string]bool{}
	if err := c.update(func(v sample) {
		for _, l := range v.labels {
			if l.key == "sensor" {
				sensors[l.value] = true
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if sensors["temp2"] || sensors["temp3"] || !sensors["temp1"] {
		t.Fatalf("unexpected sensors: %v", sensors)
	}
}

// render writes samples the way the Prometheus text format does:
// families sorted by name, labels sorted by key, and series sorted.
func render(samples []sample) []string {
	type family struct {
		help    string
		counter bool
		series  []string
	}
	families := map[string]*family{}
	for _, v := range samples {
		f, ok := families[v.name]
		if !ok {
			f = &family{help: v.help, counter: v.counter}
			families[v.name] = f
		}

		labels := slices.Clone(v.labels)
		slices.SortFunc(labels, func(a, b label) int { return strings.Compare(a.key, b.key) })
		kvs := []string{}
		for _, l := range labels {
			kvs = append(kvs, l.key+"="+strconv.Quote(l.value))
		}
		f.series = append(f.series, v.name+"{"+strings.Join(kvs, ",")+"} "+strconv.FormatFloat(v.value, 'g', -1, 64))
	}

	names := []string{}
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)

	lines := []string{}
	for _, name := range names {
		f := families[name]
		typ := "gauge"
		if f.counter {
			typ = "counter"
		}
		lines = append(lines, "# HELP "+name+" "+f.help, "# TYPE "+name+" "+typ)
		slices.Sort(f.series)
		lines = append(lines, f.series...)
	}
	return lines
}

func readLines(t *testing.T, path string) []string {
	f, err := os.Open(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lines := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}
