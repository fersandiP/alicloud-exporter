package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	RegionID     string        `yaml:"region_id"`
	PollInterval time.Duration `yaml:"-"`
	ListenAddr   string        `yaml:"listen_addr"`
	MetricsPath  string        `yaml:"metrics_path"`
	Metrics      []MetricSpec  `yaml:"metrics"`

	RawPollInterval string `yaml:"poll_interval"`
}

type MetricSpec struct {
	Namespace       string              `yaml:"namespace"`
	MetricName      string              `yaml:"metric_name"`
	Period          int                 `yaml:"period"`
	Statistic       string              `yaml:"statistic"`
	Rename          string              `yaml:"rename"`
	Dimensions      []string            `yaml:"dimensions"`
	DimensionSelect map[string][]string `yaml:"dimension_select"`
}

var (
	nameSanitizer  = strings.NewReplacer(".", "_", "-", "_")
	validName      = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	allowedStats   = map[string]bool{"Average": true, "Maximum": true, "Minimum": true, "Sum": true, "Value": true}
)

func (s MetricSpec) FinalName() string {
	if s.Rename != "" {
		return s.Rename
	}
	return "aliyun_" + nameSanitizer.Replace(s.Namespace) + "_" + nameSanitizer.Replace(s.MetricName)
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() error {
	if c.RawPollInterval == "" {
		c.PollInterval = 60 * time.Second
	} else {
		d, err := time.ParseDuration(c.RawPollInterval)
		if err != nil {
			return fmt.Errorf("poll_interval %q: %w", c.RawPollInterval, err)
		}
		c.PollInterval = d
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":9525"
	}
	if c.MetricsPath == "" {
		c.MetricsPath = "/metrics"
	}
	for i := range c.Metrics {
		if c.Metrics[i].Period == 0 {
			c.Metrics[i].Period = 60
		}
		if c.Metrics[i].Statistic == "" {
			c.Metrics[i].Statistic = "Average"
		}
	}
	return nil
}

func (c *Config) validate() error {
	if c.RegionID == "" {
		return fmt.Errorf("region_id is required")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll_interval must be positive")
	}
	if len(c.Metrics) == 0 {
		return fmt.Errorf("at least one metric spec is required")
	}
	seen := map[string]bool{}
	for i, s := range c.Metrics {
		where := fmt.Sprintf("metrics[%d]", i)
		if s.Namespace == "" || s.MetricName == "" {
			return fmt.Errorf("%s: namespace and metric_name are required", where)
		}
		if !allowedStats[s.Statistic] {
			return fmt.Errorf("%s: statistic %q not in Average|Maximum|Minimum|Sum|Value", where, s.Statistic)
		}
		if s.Period <= 0 {
			return fmt.Errorf("%s: period must be positive", where)
		}
		dimSet := map[string]bool{}
		for _, d := range s.Dimensions {
			dimSet[d] = true
		}
		for k := range s.DimensionSelect {
			if !dimSet[k] {
				return fmt.Errorf("%s: dimension_select key %q is not listed in dimensions", where, k)
			}
		}
		name := s.FinalName()
		if !validName.MatchString(name) {
			return fmt.Errorf("%s: derived metric name %q is not a valid Prometheus name", where, name)
		}
		if seen[name] {
			return fmt.Errorf("%s: duplicate metric name %q", where, name)
		}
		seen[name] = true
	}
	return nil
}
