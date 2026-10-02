# hwmon-receiver

An [OpenTelemetry Collector](https://github.com/open-telemetry/opentelemetry-collector) receiver that reads hardware sensors from `/sys/class/hwmon`: chip temperatures, fan speeds, voltages, currents, power, and energy.

It is a port of the `hwmon` collector of [node_exporter](https://github.com/prometheus/node_exporter) v1.12.0.
Metric names, labels, and descriptions are the same as node_exporter's,
so it can replace node_exporter scraped by the `prometheus` receiver without breaking existing series.

| Status    |         |
| --------- | ------- |
| Stability | alpha   |
| Signals   | metrics |

## Build

Add it to an [ocb](https://github.com/open-telemetry/opentelemetry-collector/tree/main/cmd/builder) manifest:

```yaml
receivers:
  - gomod: github.com/lesomnus/hwmon-receiver/hwmonreceiver v0.0.0-<date>-<commit>
```

Use a pseudo-version of a commit on `main`, e.g. from `go list -m github.com/lesomnus/hwmon-receiver/hwmonreceiver@main`.

## Configuration

```yaml
receivers:
  hwmon:
    collection_interval: 15s
```

| Field                 | Default | Description                                                                         |
| --------------------- | ------- | ----------------------------------------------------------------------------------- |
| `collection_interval` | `1m`    | How often to read the sensors.                                                      |
| `initial_delay`       | `1s`    | Delay before the first read.                                                        |
| `timeout`             | `0s`    | Timeout of each read; `0s` means no timeout.                                        |
| `root_path`           |         | Read `<root_path>/sys` instead of `/sys`, like `hostmetrics`. See below.            |
| `chip_include`        |         | Regexp on `chip`. Same as `--collector.hwmon.chip-include`.                          |
| `chip_exclude`        |         | Regexp on `chip`. Same as `--collector.hwmon.chip-exclude`.                          |
| `sensor_include`      |         | Regexp on `<chip>;<sensor>`, e.g. `platform_coretemp_0;temp3`. Same as `--collector.hwmon.sensor-include`. |
| `sensor_exclude`      |         | Regexp on `<chip>;<sensor>`. Same as `--collector.hwmon.sensor-exclude`.             |

Each include/exclude pair is mutually exclusive.

## Running in a container

Mount the host's root and set `root_path`, the same as for `hostmetrics`:

```yaml
receivers:
  hwmon:
    root_path: /hostfs
```

```sh
docker run -v /:/hostfs:ro ...
```

`-v` bind mounts are recursive, so the host's `/sys` comes along under `/hostfs`.
Files are only read, so neither root nor `chroot` is needed.

## Replacing node_exporter

Instead of running `node_exporter --collector.disable-defaults --collector.hwmon` and scraping it:

```yaml
receivers:
  prometheus/node_exporter:
    config:
      scrape_configs:
        - job_name: node-exporter
          static_configs:
            - targets: ["127.0.0.1:9100"]
```

use:

```yaml
receivers:
  hwmon:
    root_path: /hostfs
    collection_interval: 15s
```

Each data point has the same name, description, unit, type, and attributes (`chip`, `sensor`, `chip_name`, `label`) as before.
What differs:

- **Resource attributes.** The `prometheus` receiver adds `service.name` (the job), `service.instance.id`, `server.port`, and `url.scheme`; this receiver adds none.
  If queries or exporters rely on them, e.g. `ServiceName` of the ClickHouse exporter, set them with the `resource` processor:

  ```yaml
  processors:
    resource/hwmon:
      attributes:
        - { key: service.name, value: node-exporter, action: upsert }
        - { key: service.instance.id, value: 127.0.0.1:9100, action: upsert }
  ```

- **Counters have a start time.** `node_hwmon_energy_*_joule_total` sets `StartTimestamp` to when the receiver started, as cumulative sums should.
  The `prometheus` receiver v0.156.0 leaves it unset.
- **No exporter metrics.** node_exporter's own metrics, such as `node_scrape_collector_*`, `go_*`, and `process_*`, are gone, as are the scrape metrics such as `up`.

## Compatibility testing

- `TestNodeExporterParity` reads node_exporter's own fixtures with the chip filter of its end-to-end test,
  and expects exactly its expected output: every `# HELP`, `# TYPE`, and series.
- Against node_exporter v1.12.0 scraped by the `prometheus` receiver v0.156.0, in the same Collector:
  - on Jetson AGX Orin (L4T R36.4.4): 25 metrics and 106 series, each with the same name, description, unit, type, and attributes;
  - on node_exporter's fixtures plus an energy sensor, without a filter: 53 metrics and 141 series with identical values.
    The only difference is the start time above.
