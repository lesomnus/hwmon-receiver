// Ported from collector/hwmon_linux.go of node_exporter v1.12.0,
// Copyright 2016 The Prometheus Authors, licensed under the Apache License, Version 2.0.
// https://github.com/prometheus/node_exporter/blob/v1.12.0/collector/hwmon_linux.go
//
// Changes: samples are returned instead of being sent as Prometheus metrics,
// the sysfs root is a parameter instead of a flag, and filters are passed in.
// Names, labels, values, and help texts are kept as they are
// so the output continues the series node_exporter produced.

package hwmonreceiver

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	hwmonInvalidMetricChars = regexp.MustCompile("[^a-z0-9:_]")
	hwmonFilenameFormat     = regexp.MustCompile(`^(?P<type>[^0-9]+)(?P<id>[0-9]*)?(_(?P<property>.+))?$`)
	hwmonSensorTypes        = []string{
		"vrm", "beep_enable", "update_interval", "in", "cpu", "fan",
		"pwm", "temp", "curr", "power", "energy", "humidity",
		"intrusion", "freq",
	}
)

// errNoData means the system has no hwmon class at all.
var errNoData = errors.New("no hwmon data")

type label struct {
	key   string
	value string
}

type sample struct {
	name    string
	help    string
	counter bool
	labels  []label
	value   float64
}

type filter struct {
	exclude *regexp.Regexp
	include *regexp.Regexp
}

func (f filter) ignored(name string) bool {
	return (f.exclude != nil && f.exclude.MatchString(name)) ||
		(f.include != nil && !f.include.MatchString(name))
}

type collector struct {
	// Path of sysfs, usually `/sys`.
	sys_path string

	chip_filter   filter
	sensor_filter filter

	// Called for things node_exporter logs at debug level.
	debug func(msg string, kv ...any)
}

func cleanMetricName(name string) string {
	lower := strings.ToLower(name)
	replaced := hwmonInvalidMetricChars.ReplaceAllLiteralString(lower, "_")
	cleaned := strings.Trim(replaced, "_")
	return cleaned
}

func addValueFile(data map[string]map[string]string, sensor string, prop string, file string) {
	raw, err := sysReadFile(file)
	if err != nil {
		return
	}
	value := strings.Trim(string(raw), "\n")

	if _, ok := data[sensor]; !ok {
		data[sensor] = make(map[string]string)
	}

	data[sensor][prop] = value
}

// explodeSensorFilename splits a sensor name into <type><num>_<property>.
func explodeSensorFilename(filename string) (ok bool, sensorType string, sensorNum int, sensorProperty string) {
	matches := hwmonFilenameFormat.FindStringSubmatch(filename)
	if len(matches) == 0 {
		return false, sensorType, sensorNum, sensorProperty
	}
	for i, match := range hwmonFilenameFormat.SubexpNames() {
		if i >= len(matches) {
			return true, sensorType, sensorNum, sensorProperty
		}
		if match == "type" {
			sensorType = matches[i]
		}
		if match == "property" {
			sensorProperty = matches[i]
		}
		if match == "id" && len(matches[i]) > 0 {
			if num, err := strconv.Atoi(matches[i]); err == nil {
				sensorNum = num
			} else {
				return false, sensorType, sensorNum, sensorProperty
			}
		}
	}
	return true, sensorType, sensorNum, sensorProperty
}

func collectSensorData(dir string, data map[string]map[string]string) error {
	sensorFiles, dirError := os.ReadDir(dir)
	if dirError != nil {
		return dirError
	}
	for _, file := range sensorFiles {
		filename := file.Name()
		ok, sensorType, sensorNum, sensorProperty := explodeSensorFilename(filename)
		if !ok {
			continue
		}

		if slices.Contains(hwmonSensorTypes, sensorType) {
			addValueFile(data, sensorType+strconv.Itoa(sensorNum), sensorProperty, filepath.Join(dir, file.Name()))
		}
	}
	return nil
}

func chipLabels(chip, sensor string) []label {
	return []label{{"chip", chip}, {"sensor", sensor}}
}

func (c *collector) updateHwmon(emit func(sample), dir, hwmonName string) error {
	data := make(map[string]map[string]string)
	err := collectSensorData(dir, data)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "device")); err == nil {
		err := collectSensorData(filepath.Join(dir, "device"), data)
		if err != nil {
			return err
		}
	}

	hwmonChipName, err := c.hwmonHumanReadableChipName(dir)
	if err == nil {
		// sensor chip metadata
		emit(sample{
			name:   "node_hwmon_chip_names",
			help:   "Annotation metric for human-readable chip names",
			labels: []label{{"chip", hwmonName}, {"chip_name", hwmonChipName}},
			value:  1.0,
		})
	}

	// Format all sensors.
	for sensor, sensorData := range data {

		// Filtering for sensors is done on concatenated device name and sensor name
		// separated by a semicolon. This allows for excluding or including of specific
		// sensors on specific devices. For example, to exclude the sensor "temp3" on
		// the device "platform_coretemp_0", use "platform_coretemp_0;temp3"
		if c.sensor_filter.ignored(hwmonName + ";" + sensor) {
			c.debug("ignoring sensor", "sensor", sensor)
			continue
		}

		_, sensorType, _, _ := explodeSensorFilename(sensor)

		labels := chipLabels(hwmonName, sensor)
		if labelText, ok := sensorData["label"]; ok {
			label_ := strings.ToValidUTF8(labelText, "�")
			emit(sample{
				name:   "node_hwmon_sensor_label",
				help:   "Label for given chip and sensor",
				labels: []label{{"chip", hwmonName}, {"sensor", sensor}, {"label", label_}},
				value:  1.0,
			})
		}

		if sensorType == "beep_enable" {
			value := 0.0
			if sensorData[""] == "1" {
				value = 1.0
			}
			emit(sample{name: "node_hwmon_beep_enabled", help: "Hardware beep enabled", labels: labels, value: value})
			continue
		}
		if sensorType == "vrm" {
			parsedValue, err := strconv.ParseFloat(sensorData[""], 64)
			if err != nil {
				continue
			}
			emit(sample{name: "node_hwmon_voltage_regulator_version", help: "Hardware voltage regulator", labels: labels, value: parsedValue})
			continue
		}
		if sensorType == "update_interval" {
			parsedValue, err := strconv.ParseFloat(sensorData[""], 64)
			if err != nil {
				continue
			}
			emit(sample{name: "node_hwmon_update_interval_seconds", help: "Hardware monitor update interval", labels: labels, value: parsedValue * 0.001})
			continue
		}

		prefix := "node_hwmon_" + sensorType

		for element, value := range sensorData {

			if element == "label" {
				continue
			}

			name := prefix
			if element == "input" {
				// input is actually the value
				if _, ok := sensorData[""]; ok {
					name = name + "_input"
				}
			} else if element != "" {
				name = name + "_" + cleanMetricName(element)
			}
			parsedValue, err := strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}

			// special elements, fault, alarm & beep should be handed out without units
			if element == "fault" || element == "alarm" {
				emit(sample{name: name, help: "Hardware sensor " + element + " status (" + sensorType + ")", labels: labels, value: parsedValue})
				continue
			}
			if element == "beep" {
				emit(sample{name: name + "_enabled", help: "Hardware monitor sensor has beeping enabled", labels: labels, value: parsedValue})
				continue
			}

			// everything else should get a unit
			if sensorType == "in" || sensorType == "cpu" {
				emit(sample{name: name + "_volts", help: "Hardware monitor for voltage (" + element + ")", labels: labels, value: parsedValue * 0.001})
				continue
			}
			if sensorType == "temp" && element != "type" {
				if element == "" {
					element = "input"
				}
				emit(sample{name: name + "_celsius", help: "Hardware monitor for temperature (" + element + ")", labels: labels, value: parsedValue * 0.001})
				continue
			}
			if sensorType == "curr" {
				emit(sample{name: name + "_amps", help: "Hardware monitor for current (" + element + ")", labels: labels, value: parsedValue * 0.001})
				continue
			}
			if sensorType == "energy" {
				emit(sample{name: name + "_joule_total", help: "Hardware monitor for joules used so far (" + element + ")", counter: true, labels: labels, value: parsedValue / 1000000.0})
				continue
			}
			if sensorType == "power" && element == "accuracy" {
				emit(sample{name: name, help: "Hardware monitor power meter accuracy, as a ratio", labels: labels, value: parsedValue / 1000000.0})
				continue
			}
			if sensorType == "power" && (element == "average_interval" || element == "average_interval_min" || element == "average_interval_max") {
				emit(sample{name: name + "_seconds", help: "Hardware monitor power usage update interval (" + element + ")", labels: labels, value: parsedValue * 0.001})
				continue
			}
			if sensorType == "power" {
				emit(sample{name: name + "_watt", help: "Hardware monitor for power usage in watts (" + element + ")", labels: labels, value: parsedValue / 1000000.0})
				continue
			}

			if sensorType == "humidity" {
				emit(sample{name: name, help: "Hardware monitor for humidity, as a ratio (multiply with 100.0 to get the humidity as a percentage) (" + element + ")", labels: labels, value: parsedValue / 1000000.0})
				continue
			}

			if sensorType == "fan" && (element == "input" || element == "min" || element == "max" || element == "target") {
				emit(sample{name: name + "_rpm", help: "Hardware monitor for fan revolutions per minute (" + element + ")", labels: labels, value: parsedValue})
				continue
			}

			if sensorType == "freq" && element == "input" {
				if label_, ok := sensorData["label"]; ok {
					sensorLabel := cleanMetricName(label_)
					emit(sample{name: name + "_freq_mhz", help: "Hardware monitor for GPU frequency in MHz", labels: chipLabels(hwmonName, sensorLabel), value: parsedValue / 1000000.0})
				}
				continue
			}
			// fallback, just dump the metric as is

			emit(sample{name: name, help: "Hardware monitor " + sensorType + " element " + element, labels: labels, value: parsedValue})
		}
	}

	return nil
}

func (c *collector) hwmonName(dir string) (string, error) {
	// generate a name for a sensor path

	// sensor numbering depends on the order of linux module loading and
	// is thus unstable.
	// However the path of the device has to be stable:
	// - /sys/devices/<bus>/<device>
	// Some hardware monitors have a "name" file that exports a human
	// readable name that can be used.

	// human readable names would be bat0 or coretemp, while a path string
	// could be platform_applesmc.768

	// preference 1: construct a name based on device name, always unique

	devicePath, devErr := filepath.EvalSymlinks(filepath.Join(dir, "device"))
	if devErr == nil {
		devPathPrefix, devName := filepath.Split(devicePath)
		_, devType := filepath.Split(strings.TrimRight(devPathPrefix, "/"))

		cleanDevName := cleanMetricName(devName)
		cleanDevType := cleanMetricName(devType)

		if cleanDevType != "" && cleanDevName != "" {
			return cleanDevType + "_" + cleanDevName, nil
		}

		if cleanDevName != "" {
			return cleanDevName, nil
		}
	}

	// preference 2: is there a name file
	sysnameRaw, nameErr := os.ReadFile(filepath.Join(dir, "name"))
	if nameErr == nil && string(sysnameRaw) != "" {
		cleanName := cleanMetricName(string(sysnameRaw))
		if cleanName != "" {
			return cleanName, nil
		}
	}

	// it looks bad, name and device don't provide enough information
	// return a hwmon[0-9]* name

	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}

	// take the last path element, this will be hwmonX
	_, name := filepath.Split(realDir)
	cleanName := cleanMetricName(name)
	if cleanName != "" {
		return cleanName, nil
	}
	return "", errors.New("Could not derive a monitoring name for " + dir)
}

// hwmonHumanReadableChipName is similar to the methods in hwmonName, but with
// different precedences -- we can allow duplicates here.
func (c *collector) hwmonHumanReadableChipName(dir string) (string, error) {
	sysnameRaw, nameErr := os.ReadFile(filepath.Join(dir, "name"))
	if nameErr != nil {
		return "", nameErr
	}

	if string(sysnameRaw) != "" {
		cleanName := cleanMetricName(string(sysnameRaw))
		if cleanName != "" {
			return cleanName, nil
		}
	}

	return "", errors.New("Could not derive a human-readable chip type for " + dir)
}

func (c *collector) update(emit func(sample)) error {
	// Step 1: scan /sys/class/hwmon, resolve all symlinks and call
	//         updateHwmon for each folder.

	hwmonPathName := filepath.Join(c.sys_path, "class", "hwmon")

	hwmonFiles, err := os.ReadDir(hwmonPathName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.debug("hwmon collector metrics are not available for this system")
			return errNoData
		}

		return err
	}

	// Pass 1: enumerate hwmon directories and pre-compute the device-derived
	// chip name. Multiple hwmon nodes can share a single parent device (for
	// example, asus-nb-wmi exposes one hwmon for fan control and another for
	// WMI sensors), which makes hwmonName collide and triggers the "metric
	// collected before with the same name and label values" registry error.
	type hwmonEntry struct {
		dir      string
		baseName string
		nameFile string
	}

	entries := make([]hwmonEntry, 0, len(hwmonFiles))
	chipCounts := make(map[string]int, len(hwmonFiles))
	nameCounts := make(map[string]int, len(hwmonFiles))
	for _, hwDir := range hwmonFiles {
		hwmonXPathName := filepath.Join(hwmonPathName, hwDir.Name())
		fileInfo, err := os.Lstat(hwmonXPathName)
		if err != nil {
			continue
		}

		if fileInfo.Mode()&os.ModeSymlink > 0 {
			fileInfo, err = os.Stat(hwmonXPathName)
			if err != nil {
				continue
			}
		}

		if !fileInfo.IsDir() {
			continue
		}

		baseName, err := c.hwmonName(hwmonXPathName)
		if err != nil {
			c.debug("failed to derive hwmon chip name", "dir", hwmonXPathName, "err", err)
			continue
		}

		nameFile := ""
		if raw, err := os.ReadFile(filepath.Join(hwmonXPathName, "name")); err == nil {
			nameFile = strings.TrimSpace(string(raw))
		}

		entries = append(entries, hwmonEntry{
			dir:      hwmonXPathName,
			baseName: baseName,
			nameFile: nameFile,
		})
		chipCounts[baseName]++
		nameCounts[baseName+"\x00"+nameFile]++
	}

	// Pass 2: emit metrics. For each entry, resolve a unique chip label
	// (disambiguating colliding base names with the chip's `name` file
	// content when distinct, else with the hwmonX basename) and only then
	// apply the include/exclude filter so user regexes match the label
	// that ends up in the metrics.
	var lastErr error
	for _, e := range entries {
		chipName := e.baseName
		if chipCounts[e.baseName] > 1 {
			suffix := cleanMetricName(e.nameFile)
			if suffix == "" || nameCounts[e.baseName+"\x00"+e.nameFile] > 1 {
				suffix = cleanMetricName(filepath.Base(e.dir))
			}
			chipName = e.baseName + "_" + suffix
		}

		if c.chip_filter.ignored(chipName) {
			c.debug("ignoring hwmon chip", "chip", chipName)
			continue
		}

		if err := c.updateHwmon(emit, e.dir, chipName); err != nil {
			lastErr = err
		}
	}

	return lastErr
}
