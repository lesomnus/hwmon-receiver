package hwmonreceiver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"go.opentelemetry.io/collector/scraper/scraperhelper"
)

type Config struct {
	scraperhelper.ControllerConfig `mapstructure:",squash"`

	// Reads `<root_path>/sys` instead of `/sys`,
	// e.g. the host's root mounted in a container, as `hostmetrics` does.
	RootPath string `mapstructure:"root_path"`

	// Regexps on the `chip` label, same as `--collector.hwmon.chip-{include,exclude}`.
	ChipInclude string `mapstructure:"chip_include"`
	ChipExclude string `mapstructure:"chip_exclude"`

	// Regexps on `<chip>;<sensor>`, same as `--collector.hwmon.sensor-{include,exclude}`.
	SensorInclude string `mapstructure:"sensor_include"`
	SensorExclude string `mapstructure:"sensor_exclude"`
}

func (c *Config) Validate() error {
	errs := []error{}
	if c.RootPath != "" {
		if err := validateRootPath(c.RootPath); err != nil {
			errs = append(errs, fmt.Errorf(`"root_path": %w`, err))
		}
	}
	if c.ChipInclude != "" && c.ChipExclude != "" {
		errs = append(errs, errors.New(`"chip_include" and "chip_exclude" are mutually exclusive`))
	}
	if c.SensorInclude != "" && c.SensorExclude != "" {
		errs = append(errs, errors.New(`"sensor_include" and "sensor_exclude" are mutually exclusive`))
	}
	for k, v := range map[string]string{
		"chip_include":   c.ChipInclude,
		"chip_exclude":   c.ChipExclude,
		"sensor_include": c.SensorInclude,
		"sensor_exclude": c.SensorExclude,
	} {
		if _, err := regexp.Compile(v); err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", k, err))
		}
	}
	return errors.Join(errs...)
}

func validateRootPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("must be absolute: %q", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	return nil
}

func (c *Config) sysPath() string {
	return filepath.Join("/", c.RootPath, "sys")
}

func newFilter(exclude, include string) filter {
	f := filter{}
	if exclude != "" {
		f.exclude = regexp.MustCompile(exclude)
	}
	if include != "" {
		f.include = regexp.MustCompile(include)
	}
	return f
}
