package compose

import (
	"sort"
	"time"
)

// Project is a loaded, merged and validated Compose application model.
type Project struct {
	// Name is the project name: lowercase letters, digits, '-' and '_'.
	Name string `json:"name"`
	// WorkingDir is the project directory (in Options.FS).
	WorkingDir string `json:"-"`
	// Files are the Compose files read, in order.
	Files []string `json:"-"`
	// Services are the enabled services; DisabledServices those whose
	// profiles are not active.
	Services         map[string]*Service `json:"services"`
	DisabledServices map[string]*Service `json:"-"`
	Networks         map[string]*Network `json:"networks,omitempty"`
	Volumes          map[string]*Volume  `json:"volumes,omitempty"`
	// Extensions are the top-level `x-` keys.
	Extensions map[string]any `json:"-"`
	// Environment is what interpolation read (explicit environment over
	// .env), with COMPOSE_PROJECT_NAME.
	Environment map[string]string `json:"-"`
	// Warnings are what was accepted but is not applied (unsupported
	// attributes, unset variables).
	Warnings []string `json:"-"`
}

// ServiceNames returns the enabled services' names, sorted.
func (p *Project) ServiceNames() []string {
	names := make([]string, 0, len(p.Services))
	for n := range p.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Command is a command line: nil is "the image's", an empty non-nil one
// clears the image's.
type Command []string

// Service is one service.
type Service struct {
	Name          string                     `json:"-"`
	Image         string                     `json:"image,omitempty"`
	Build         *Build                     `json:"build,omitempty"`
	Command       Command                    `json:"command,omitempty"`
	Entrypoint    Command                    `json:"entrypoint,omitempty"`
	Environment   map[string]string          `json:"environment,omitempty"`
	WorkingDir    string                     `json:"working_dir,omitempty"`
	Volumes       []Mount                    `json:"volumes,omitempty"`
	Networks      map[string]*ServiceNetwork `json:"networks,omitempty"`
	NetworkMode   string                     `json:"network_mode,omitempty"`
	DependsOn     map[string]Dependency      `json:"depends_on,omitempty"`
	Healthcheck   *Healthcheck               `json:"healthcheck,omitempty"`
	Restart       string                     `json:"restart,omitempty"`
	Replicas      int                        `json:"replicas"`
	Labels        map[string]string          `json:"labels,omitempty"`
	Profiles      []string                   `json:"profiles,omitempty"`
	ContainerName string                     `json:"container_name,omitempty"`
	Extensions    map[string]any             `json:"-"`
}

// Build is a service's build section (build.md), built by
// go-wasmcontainers/build through the engine.
type Build struct {
	// Context is the build context's directory in the project's file
	// system (resolved from the Compose file's directory).
	Context string `json:"context"`
	// Dockerfile is relative to Context (default Containerfile, else
	// Dockerfile); DockerfileInline is the file's content instead.
	Dockerfile       string             `json:"dockerfile,omitempty"`
	DockerfileInline string             `json:"dockerfile_inline,omitempty"`
	Args             map[string]*string `json:"args,omitempty"`
	Target           string             `json:"target,omitempty"`
	// Tags name the built image besides the service's image name.
	Tags    []string `json:"tags,omitempty"`
	NoCache bool     `json:"no_cache,omitempty"`
}

// ImageName is the image a service's containers run: its `image`, else
// "<project>-<service>" for a service that is built (as the reference
// implementation names it; build.md lets an implementation compute one).
func (p *Project) ImageName(s *Service) string {
	if s.Image != "" {
		return s.Image
	}
	return p.Name + "-" + s.Name
}

// Mount is a volume mounted into a service's containers. Only named
// volumes exist on this engine (Type "volume").
type Mount struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// ServiceNetwork is a service's place on a network.
type ServiceNetwork struct {
	Aliases []string `json:"aliases,omitempty"`
}

// Dependency conditions (workload_spec.md, depends_on long syntax).
const (
	ServiceStarted               = "service_started"
	ServiceHealthy               = "service_healthy"
	ServiceCompletedSuccessfully = "service_completed_successfully"
)

// Dependency is one depends_on entry.
type Dependency struct {
	Condition string `json:"condition"`
	Restart   bool   `json:"restart,omitempty"`
	Required  bool   `json:"required"`
}

// Healthcheck is a service's health check.
type Healthcheck struct {
	Test          []string       `json:"test,omitempty"`
	Interval      *time.Duration `json:"interval,omitempty"`
	Timeout       *time.Duration `json:"timeout,omitempty"`
	StartPeriod   *time.Duration `json:"start_period,omitempty"`
	StartInterval *time.Duration `json:"start_interval,omitempty"`
	Retries       *int64         `json:"retries,omitempty"`
	Disable       bool           `json:"disable,omitempty"`
}

// Network is a top-level network.
type Network struct {
	// Name is the network's name in the engine: "<project>_<key>" unless
	// set, the key itself when external.
	Name       string            `json:"name"`
	External   bool              `json:"external,omitempty"`
	Driver     string            `json:"driver,omitempty"`
	Internal   bool              `json:"internal,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Extensions map[string]any    `json:"-"`
}

// Volume is a top-level named volume.
type Volume struct {
	Name       string            `json:"name"`
	External   bool              `json:"external,omitempty"`
	Driver     string            `json:"driver,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Extensions map[string]any    `json:"-"`
}
