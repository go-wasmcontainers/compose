package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/portmap"
)

// The labels a project's resources carry. The specification mandates the
// first two and reserves the prefix (02-model.md, container_spec.md); the
// others are the reference implementation's names (docs/REFERENCES.md).
const (
	LabelProject         = "com.docker.compose.project"
	LabelService         = "com.docker.compose.service"
	LabelContainerNumber = "com.docker.compose.container-number"
	LabelConfigHash      = "com.docker.compose.config-hash"
	LabelImage           = "com.docker.compose.image"
	LabelOneoff          = "com.docker.compose.oneoff"
	LabelNetwork         = "com.docker.compose.network"
	LabelVolume          = "com.docker.compose.volume"
	LabelDependsOn       = "com.docker.compose.depends_on"
)

// ErrHealthcheckUnsupported is what `service_healthy` meets: the engine
// cannot run a health check yet (it has no way to run a command in a
// running container). Failing beats waiting forever.
var ErrHealthcheckUnsupported = errors.New("the engine does not run health checks yet (it cannot run a command in a container), so condition service_healthy can never be met")

// Runner drives an engine through its API, as the compose CLI drives a
// daemon: it never reaches the engine any other way.
type Runner struct {
	Client *api.Client
	// Build builds a service's image (nil: services that need building
	// fail with ErrNoBuilder).
	Build BuildFunc
	// Out receives progress and logs; Err warnings.
	Out, Err io.Writer
}

func (r *Runner) printf(format string, a ...any) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, format, a...)
	}
}

func (r *Runner) warnf(format string, a ...any) {
	if r.Err != nil {
		fmt.Fprintf(r.Err, "WARN "+format+"\n", a...)
	}
}

// Container is a project's container as the engine lists it.
type Container struct {
	api.ContainerSummary
	Name    string
	Service string
	Number  int
}

// projectFilter selects a project's resources by their label; the engine
// (v0.14.0 and later) applies it; the client's *Filtered methods send it
// (v0.20.0 and later; v0.18.0's variadic form is gone).
func projectFilter(project string) api.Filters {
	return api.Filters{"label": {LabelProject + "=" + project}}
}

// Containers lists the project's containers (all states), sorted by
// service then number; the engine filters them by the project's label.
func (r *Runner) Containers(ctx context.Context, project string) ([]Container, error) {
	all, err := r.Client.ContainersFiltered(ctx, true, projectFilter(project))
	if err != nil {
		return nil, err
	}
	var out []Container
	for _, c := range all {
		if c.Labels[LabelProject] != project {
			// The engine answered for another project: say so loudly
			// rather than act on its containers.
			return nil, fmt.Errorf("the engine listed container %s of project %q for project %q: its label filter did not apply", c.ID, c.Labels[LabelProject], project)
		}
		n, _ := strconv.Atoi(c.Labels[LabelContainerNumber])
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, Container{ContainerSummary: c, Name: name, Service: c.Labels[LabelService], Number: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Number < out[j].Number
	})
	return out, nil
}

// Order returns the enabled services, dependencies first (ties by name).
func (p *Project) Order() []string {
	var out []string
	done := map[string]bool{}
	var visit func(string)
	visit = func(n string) {
		if done[n] {
			return
		}
		done[n] = true
		s := p.Services[n]
		if s == nil {
			return
		}
		for _, d := range sortedKeys(s.DependsOn) {
			visit(d)
		}
		out = append(out, n)
	}
	for _, n := range p.ServiceNames() {
		visit(n)
	}
	return out
}

// ContainerName is the name of a service's container number n.
func (p *Project) ContainerName(s *Service, n int) string {
	if s.ContainerName != "" {
		return s.ContainerName
	}
	return fmt.Sprintf("%s-%s-%d", p.Name, s.Name, n)
}

// serviceNetworks returns the network a service's containers are created
// on (the first by key) and the endpoint settings of every network they
// join: on each, the container answers to the service's name and to its
// aliases there (container_spec.md, networks, aliases), as well as to its
// own name. The engine (API 1.44+ shape) joins all of them at create.
func (r *Runner) serviceNetworks(p *Project, s *Service) (string, *api.NetworkingConfig) {
	if s.NetworkMode == "none" {
		return "none", nil
	}
	keys := sortedKeys(s.Networks)
	if len(keys) == 0 {
		return "none", nil
	}
	nc := &api.NetworkingConfig{EndpointsConfig: map[string]*api.EndpointSettings{}}
	for _, k := range keys {
		aliases := append([]string{s.Name}, s.Networks[k].Aliases...)
		nc.EndpointsConfig[p.Networks[k].Name] = &api.EndpointSettings{Aliases: aliases}
	}
	return p.Networks[keys[0]].Name, nc
}

// createRequest is the engine request for a service's container.
func (r *Runner) createRequest(p *Project, s *Service, n int, imageID string) (api.ContainerCreateRequest, error) {
	req := api.ContainerCreateRequest{}
	req.Image = p.ImageName(s)
	if s.Command != nil {
		req.Cmd = api.StrSlice(s.Command)
	}
	if s.Entrypoint != nil {
		req.Entrypoint = api.StrSlice(s.Entrypoint)
	}
	for _, k := range sortedKeys(s.Environment) {
		req.Env = append(req.Env, k+"="+s.Environment[k])
	}
	req.WorkingDir = s.WorkingDir
	req.Labels = map[string]string{}
	for k, v := range s.Labels {
		req.Labels[k] = v
	}
	hash, err := ConfigHash(p, s)
	if err != nil {
		return req, err
	}
	req.Labels[LabelProject] = p.Name
	req.Labels[LabelService] = s.Name
	req.Labels[LabelContainerNumber] = strconv.Itoa(n)
	req.Labels[LabelConfigHash] = hash
	req.Labels[LabelImage] = imageID
	req.Labels[LabelOneoff] = "False"
	if len(s.DependsOn) > 0 {
		var deps []string
		for _, d := range sortedKeys(s.DependsOn) {
			dep := s.DependsOn[d]
			deps = append(deps, fmt.Sprintf("%s:%s:%t", d, dep.Condition, dep.Restart))
		}
		req.Labels[LabelDependsOn] = strings.Join(deps, ",")
	}
	for _, m := range s.Volumes {
		b := p.Volumes[m.Source].Name + ":" + m.Target
		if m.ReadOnly {
			b += ":ro"
		}
		req.HostConfig.Binds = append(req.HostConfig.Binds, b)
	}
	req.HostConfig.NetworkMode, req.NetworkingConfig = r.serviceNetworks(p, s)
	// ports: published on the engine's host (an empty host address is
	// the engine's 127.0.0.1); expose: reachable, not published.
	if len(s.Ports) > 0 {
		var specs []string
		for _, pt := range s.Ports {
			specs = append(specs, pt.Spec())
		}
		m, err := portmap.ParseSpecs(specs)
		if err != nil {
			return req, err
		}
		req.HostConfig.PortBindings = m.API()
	}
	if len(s.Expose) > 0 {
		m, err := portmap.ParseSpecs(s.Expose)
		if err != nil {
			return req, err
		}
		req.ExposedPorts = map[string]struct{}{}
		for _, k := range m.Keys() {
			req.ExposedPorts[k.String()] = struct{}{}
		}
	}
	return req, nil
}

// ConfigHash is a digest of everything that makes a service's container:
// a container whose label differs is recreated by up.
func ConfigHash(p *Project, s *Service) (string, error) {
	doc := s.tree()
	var refs []string
	for _, k := range sortedKeys(s.Networks) {
		refs = append(refs, "network="+p.Networks[k].Name)
	}
	for _, m := range s.Volumes {
		refs = append(refs, "volume="+p.Volumes[m.Source].Name)
	}
	doc.Set("x-resolved", strSeq(refs))
	h := sha256.Sum256(marshalJSON(doc))
	return hex.EncodeToString(h[:]), nil
}

// UpOptions are `up`'s.
type UpOptions struct {
	// Services limits up to these services and their dependencies.
	Services []string
	// ForceRecreate recreates containers even when nothing changed.
	ForceRecreate bool
	// Build asks to build images first (needs the builder).
	Build bool
	// RemoveOrphans removes containers of services no longer defined.
	RemoveOrphans bool
}

// withDeps returns the named services and those they depend on.
func (p *Project) withDeps(names []string) (map[string]bool, error) {
	if len(names) == 0 {
		all := map[string]bool{}
		for n := range p.Services {
			all[n] = true
		}
		return all, nil
	}
	sel := map[string]bool{}
	var add func(string) error
	add = func(n string) error {
		if sel[n] {
			return nil
		}
		s, ok := p.Services[n]
		if !ok {
			return fmt.Errorf("no such service: %s", n)
		}
		sel[n] = true
		for d := range s.DependsOn {
			if _, ok := p.Services[d]; ok {
				if err := add(d); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, n := range names {
		if err := add(n); err != nil {
			return nil, err
		}
	}
	return sel, nil
}

// Up creates what is missing and (re)starts the project's containers,
// dependencies first; a container whose configuration did not change is
// kept (started again if it stopped).
func (r *Runner) Up(ctx context.Context, p *Project, o UpOptions) error {
	sel, err := p.withDeps(o.Services)
	if err != nil {
		return err
	}
	// Images first, as the compose CLI does: a failed build creates
	// nothing.
	images := map[string]string{}
	for _, n := range p.Order() {
		if !sel[n] {
			continue
		}
		s := p.Services[n]
		name := p.ImageName(s)
		if _, ok := images[name]; ok {
			continue
		}
		id, err := r.serviceImage(ctx, p, s, o.Build)
		if err != nil {
			return fmt.Errorf("service %q: %w", n, err)
		}
		images[name] = id
	}
	if err := r.ensureNetworks(ctx, p); err != nil {
		return err
	}
	if err := r.ensureVolumes(ctx, p); err != nil {
		return err
	}
	existing, err := r.Containers(ctx, p.Name)
	if err != nil {
		return err
	}
	byService := map[string][]Container{}
	for _, c := range existing {
		if _, ok := p.Services[c.Service]; !ok {
			if o.RemoveOrphans {
				if err := r.Client.Remove(ctx, c.ID, true); err != nil && !api.IsNotFound(err) {
					return err
				}
				r.printf(" Container %s  Removed (orphan)\n", c.Name)
			} else {
				r.warnf("container %s belongs to service %q, which the project no longer defines (--remove-orphans removes it)", c.Name, c.Service)
			}
			continue
		}
		byService[c.Service] = append(byService[c.Service], c)
	}
	for _, n := range p.Order() {
		if !sel[n] {
			continue
		}
		s := p.Services[n]
		if err := r.waitDependencies(ctx, p, s); err != nil {
			return err
		}
		id := images[p.ImageName(s)]
		if err := r.upService(ctx, p, s, byService[n], id, o.ForceRecreate); err != nil {
			return fmt.Errorf("service %q: %w", n, err)
		}
	}
	return nil
}

func (r *Runner) ensureNetworks(ctx context.Context, p *Project) error {
	have, err := r.Client.Networks(ctx)
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, n := range have {
		exists[n.Name] = true
	}
	used := map[string]bool{}
	for _, s := range p.Services {
		for k := range s.Networks {
			used[k] = true
		}
	}
	for _, k := range sortedKeys(p.Networks) {
		nw := p.Networks[k]
		if exists[nw.Name] || !used[k] {
			continue
		}
		if nw.External {
			return fmt.Errorf("external network %q not found", nw.Name)
		}
		labels := map[string]string{LabelProject: p.Name, LabelNetwork: k}
		for lk, lv := range nw.Labels {
			labels[lk] = lv
		}
		if _, err := r.Client.NetworkCreate(ctx, api.NetworkCreateRequest{Name: nw.Name, Internal: nw.Internal, Labels: labels}); err != nil {
			return fmt.Errorf("network %s: %w", nw.Name, err)
		}
		r.printf(" Network %s  Created\n", nw.Name)
	}
	return nil
}

func (r *Runner) ensureVolumes(ctx context.Context, p *Project) error {
	have, err := r.Client.Volumes(ctx)
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, v := range have {
		exists[v.Name] = true
	}
	for _, k := range sortedKeys(p.Volumes) {
		v := p.Volumes[k]
		if exists[v.Name] {
			continue
		}
		if v.External {
			return fmt.Errorf("external volume %q not found", v.Name)
		}
		labels := map[string]string{LabelProject: p.Name, LabelVolume: k}
		for lk, lv := range v.Labels {
			labels[lk] = lv
		}
		if _, err := r.Client.VolumeCreate(ctx, api.VolumeCreateOptions{Name: v.Name, Labels: labels}); err != nil {
			return fmt.Errorf("volume %s: %w", v.Name, err)
		}
		r.printf(" Volume %s  Created\n", v.Name)
	}
	return nil
}

// BuildFunc builds a service's image (tagged with p.ImageName(s) and
// its build tags) and returns its ID. Package imagebuild provides one,
// over go-wasmcontainers/build; the loader itself does not link a builder.
type BuildFunc func(ctx context.Context, p *Project, s *Service, noCache bool) (string, error)

// ErrNoBuilder is returned when a build is needed and the Runner has no
// BuildFunc.
var ErrNoBuilder = errors.New("this service must be built, and no builder is configured")

// serviceImage returns the ID of the image a service runs: built when
// asked (--build) or when it is missing and the service can be built (a
// service with an image name is pulled first, and built if the pull
// fails: build.md, "Using build and image"); pulled otherwise.
func (r *Runner) serviceImage(ctx context.Context, p *Project, s *Service, build bool) (string, error) {
	name := p.ImageName(s)
	if build && s.Build != nil {
		return r.build(ctx, p, s, false)
	}
	id, err := r.imageID(ctx, name)
	if err == nil {
		return id, nil
	}
	if !api.IsNotFound(err) {
		return "", err
	}
	if s.Build == nil {
		return r.ensureImage(ctx, name)
	}
	if s.Image != "" {
		if perr := r.pull(ctx, name); perr == nil {
			return r.imageID(ctx, name)
		} else {
			r.warnf("service %q: %v; building it", s.Name, perr)
		}
	}
	return r.build(ctx, p, s, false)
}

func (r *Runner) build(ctx context.Context, p *Project, s *Service, noCache bool) (string, error) {
	if r.Build == nil {
		return "", ErrNoBuilder
	}
	r.printf(" Image %s  Building\n", p.ImageName(s))
	id, err := r.Build(ctx, p, s, noCache || s.Build.NoCache)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	r.printf(" Image %s  Built\n", p.ImageName(s))
	return id, nil
}

// BuildServices builds the services that have a build section (all of
// them when none is named): `compose build`.
func (r *Runner) BuildServices(ctx context.Context, p *Project, services []string, noCache bool) error {
	names := services
	if len(names) == 0 {
		names = p.ServiceNames()
	}
	for _, n := range names {
		s, ok := p.Services[n]
		if !ok {
			return fmt.Errorf("no such service: %s", n)
		}
		if s.Build == nil {
			if len(services) > 0 {
				r.warnf("service %q has no build section", n)
			}
			continue
		}
		if _, err := r.build(ctx, p, s, noCache); err != nil {
			return fmt.Errorf("service %q: %w", n, err)
		}
	}
	return nil
}

// ensureImage returns the image's ID, pulling it when it is missing.
func (r *Runner) ensureImage(ctx context.Context, ref string) (string, error) {
	if id, err := r.imageID(ctx, ref); err == nil {
		return id, nil
	} else if !api.IsNotFound(err) {
		return "", err
	}
	if err := r.pull(ctx, ref); err != nil {
		return "", err
	}
	return r.imageID(ctx, ref)
}

func (r *Runner) imageID(ctx context.Context, ref string) (string, error) {
	raw, err := r.Client.ImageInspect(ctx, ref)
	if err != nil {
		return "", err
	}
	var ii api.ImageInspect
	if err := json.Unmarshal(raw, &ii); err != nil {
		return "", err
	}
	return ii.ID, nil
}

func (r *Runner) pull(ctx context.Context, ref string) error {
	r.printf(" Image %s  Pulling\n", ref)
	var failed string
	err := r.Client.Pull(ctx, ref, func(m api.JSONMessage) {
		if m.Error != "" {
			failed = m.Error
		}
	})
	if err == nil && failed != "" {
		err = errors.New(failed)
	}
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	r.printf(" Image %s  Pulled\n", ref)
	return nil
}

// waitDependencies blocks until each dependency's condition holds.
func (r *Runner) waitDependencies(ctx context.Context, p *Project, s *Service) error {
	for _, d := range sortedKeys(s.DependsOn) {
		dep := s.DependsOn[d]
		if _, ok := p.Services[d]; !ok {
			continue // a non-required dependency that is not enabled
		}
		switch dep.Condition {
		case ServiceHealthy:
			if !dep.Required {
				r.warnf("service %q: dependency %q: %v; continuing (required: false)", s.Name, d, ErrHealthcheckUnsupported)
				continue
			}
			return fmt.Errorf("service %q depends on %q being healthy: %w", s.Name, d, ErrHealthcheckUnsupported)
		case ServiceCompletedSuccessfully:
			cs, err := r.Containers(ctx, p.Name)
			if err != nil {
				return err
			}
			for _, c := range cs {
				if c.Service != d {
					continue
				}
				w, err := r.Client.Wait(ctx, c.ID, "not-running")
				if err != nil {
					return err
				}
				res, err := w.Result()
				if err != nil {
					return err
				}
				if res.StatusCode != 0 {
					err := fmt.Errorf("service %q depends on %q completing successfully, but %s exited with %d", s.Name, d, c.Name, res.StatusCode)
					if !dep.Required {
						r.warnf("%v; continuing (required: false)", err)
						continue
					}
					return err
				}
				r.printf(" Container %s  Exited (0)\n", c.Name)
			}
		}
	}
	return nil
}

func (r *Runner) upService(ctx context.Context, p *Project, s *Service, have []Container, imageID string, force bool) error {
	hash, err := ConfigHash(p, s)
	if err != nil {
		return err
	}
	byNumber := map[int]Container{}
	for _, c := range have {
		if c.Number > s.Replicas || c.Number < 1 {
			if err := r.Client.Remove(ctx, c.ID, true); err != nil && !api.IsNotFound(err) {
				return err
			}
			r.printf(" Container %s  Removed\n", c.Name)
			continue
		}
		byNumber[c.Number] = c
	}
	for n := 1; n <= s.Replicas; n++ {
		name := p.ContainerName(s, n)
		if c, ok := byNumber[n]; ok {
			if !force && c.Labels[LabelConfigHash] == hash && c.Labels[LabelImage] == imageID {
				if c.State != "running" {
					if err := r.Client.Start(ctx, c.ID); err != nil {
						return err
					}
					r.printf(" Container %s  Started\n", name)
				} else {
					r.printf(" Container %s  Running\n", name)
				}
				continue
			}
			if err := r.Client.Remove(ctx, c.ID, true); err != nil && !api.IsNotFound(err) {
				return err
			}
			r.printf(" Container %s  Recreate\n", name)
		}
		req, err := r.createRequest(p, s, n, imageID)
		if err != nil {
			return err
		}
		resp, err := r.Client.Create(ctx, name, req)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		for _, w := range resp.Warnings {
			r.warnf("%s: %s", name, w)
		}
		r.printf(" Container %s  Created\n", name)
		if err := r.Client.Start(ctx, resp.ID); err != nil {
			return fmt.Errorf("start %s: %w", name, err)
		}
		r.printf(" Container %s  Started\n", name)
	}
	return nil
}

// selected filters a project's containers by service names (none: all).
func selected(cs []Container, services []string) []Container {
	if len(services) == 0 {
		return cs
	}
	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}
	var out []Container
	for _, c := range cs {
		if want[c.Service] {
			out = append(out, c)
		}
	}
	return out
}

// inOrder sorts containers by the project's dependency order (reversed
// for stopping), then number.
func inOrder(p *Project, cs []Container, reverse bool) []Container {
	rank := map[string]int{}
	for i, n := range p.Order() {
		rank[n] = i
	}
	out := append([]Container(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank[out[i].Service], rank[out[j].Service]
		if ri != rj {
			if reverse {
				return ri > rj
			}
			return ri < rj
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// Down stops and removes the project's containers and networks, and with
// volumes its volumes (never external ones).
func (r *Runner) Down(ctx context.Context, p *Project, volumes bool) error {
	cs, err := r.Containers(ctx, p.Name)
	if err != nil {
		return err
	}
	for _, c := range inOrder(p, cs, true) {
		if err := r.Client.Remove(ctx, c.ID, true); err != nil && !api.IsNotFound(err) {
			return fmt.Errorf("remove %s: %w", c.Name, err)
		}
		r.printf(" Container %s  Removed\n", c.Name)
	}
	nets, err := r.Client.NetworksFiltered(ctx, projectFilter(p.Name))
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Labels[LabelProject] != p.Name {
			return fmt.Errorf("the engine listed network %s for project %q: its label filter did not apply", n.Name, p.Name)
		}
		if err := r.Client.NetworkRemove(ctx, n.Name); err != nil && !api.IsNotFound(err) {
			return fmt.Errorf("network %s: %w", n.Name, err)
		}
		r.printf(" Network %s  Removed\n", n.Name)
	}
	if !volumes {
		return nil
	}
	vols, err := r.Client.VolumesFiltered(ctx, projectFilter(p.Name))
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.Labels[LabelProject] != p.Name {
			return fmt.Errorf("the engine listed volume %s for project %q: its label filter did not apply", v.Name, p.Name)
		}
		if err := r.Client.VolumeRemove(ctx, v.Name, false); err != nil && !api.IsNotFound(err) {
			return fmt.Errorf("volume %s: %w", v.Name, err)
		}
		r.printf(" Volume %s  Removed\n", v.Name)
	}
	return nil
}

// Start starts the services' existing containers, dependencies first.
func (r *Runner) Start(ctx context.Context, p *Project, services []string) error {
	return r.each(ctx, p, services, false, "Started", func(c Container) error {
		if c.State == "running" {
			return nil
		}
		return r.Client.Start(ctx, c.ID)
	})
}

// Stop stops the services' running containers, dependents first.
func (r *Runner) Stop(ctx context.Context, p *Project, services []string) error {
	return r.each(ctx, p, services, true, "Stopped", func(c Container) error {
		if c.State != "running" {
			return nil
		}
		return r.Client.Stop(ctx, c.ID)
	})
}

// Restart restarts the services' containers.
func (r *Runner) Restart(ctx context.Context, p *Project, services []string) error {
	return r.each(ctx, p, services, false, "Restarted", func(c Container) error {
		return r.Client.Restart(ctx, c.ID)
	})
}

// Rm removes the services' stopped containers (running ones too with stop).
func (r *Runner) Rm(ctx context.Context, p *Project, services []string, stop bool) error {
	return r.each(ctx, p, services, true, "Removed", func(c Container) error {
		if c.State == "running" && !stop {
			return errSkip
		}
		return r.Client.Remove(ctx, c.ID, stop)
	})
}

var errSkip = errors.New("skip")

func (r *Runner) each(ctx context.Context, p *Project, services []string, reverse bool, verb string, fn func(Container) error) error {
	for _, s := range services {
		if _, ok := p.Services[s]; !ok {
			return fmt.Errorf("no such service: %s", s)
		}
	}
	cs, err := r.Containers(ctx, p.Name)
	if err != nil {
		return err
	}
	cs = selected(cs, services)
	if len(cs) == 0 {
		r.warnf("no containers to act on")
	}
	for _, c := range inOrder(p, cs, reverse) {
		if err := fn(c); err != nil {
			if errors.Is(err, errSkip) {
				continue
			}
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		r.printf(" Container %s  %s\n", c.Name, verb)
	}
	return nil
}

// Pull pulls the services' images.
func (r *Runner) Pull(ctx context.Context, p *Project, services []string) error {
	seen := map[string]bool{}
	names := services
	if len(names) == 0 {
		names = p.ServiceNames()
	}
	for _, n := range names {
		s, ok := p.Services[n]
		if !ok {
			return fmt.Errorf("no such service: %s", n)
		}
		if s.Image == "" || seen[s.Image] {
			continue
		}
		seen[s.Image] = true
		if err := r.pull(ctx, s.Image); err != nil {
			return err
		}
	}
	return nil
}

// Logs writes the containers' logs, each line prefixed by the container's
// name; with follow it returns when every stream has ended (each
// container exited) or ctx is done.
func (r *Runner) Logs(ctx context.Context, p *Project, services []string, follow bool) error {
	cs, err := r.Containers(ctx, p.Name)
	if err != nil {
		return err
	}
	return r.logs(ctx, selected(cs, services), follow)
}

func (r *Runner) logs(ctx context.Context, cs []Container, follow bool) error {
	width := 0
	for _, c := range cs {
		width = max(width, len(c.Name))
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(cs))
	for i, c := range cs {
		prefix := fmt.Sprintf("%-*s | ", width, c.Name)
		body, err := r.Client.Logs(ctx, c.ID, follow)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer body.Close()
			out := &lineWriter{mu: &mu, w: r.Out, prefix: prefix}
			errs[i] = api.Demux(out, out, body)
			out.flush()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("%s: %w", cs[i].Name, err)
		}
	}
	return nil
}

// lineWriter prefixes each line; lines of several writers do not mix.
type lineWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

// maxLine bounds a partial line kept in memory: a longer one is cut.
const maxLine = 64 << 10

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > maxLine {
				l.emit(l.buf[:maxLine])
				l.buf = append(l.buf[:0], l.buf[maxLine:]...)
				continue
			}
			return len(p), nil
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
}

func (l *lineWriter) emit(line []byte) {
	if l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s%s\n", l.prefix, line)
}

func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

// Attach follows the logs of the project's containers until they have
// all exited, or ctx is done (then they are stopped): `up` without -d.
func (r *Runner) Attach(ctx context.Context, p *Project, services []string) error {
	sel, err := p.withDeps(services)
	if err != nil {
		return err
	}
	cs, err := r.Containers(ctx, p.Name)
	if err != nil {
		return err
	}
	var mine []Container
	for _, c := range cs {
		if sel[c.Service] {
			mine = append(mine, c)
		}
	}
	err = r.logs(ctx, mine, true)
	if ctx.Err() != nil {
		r.printf("Gracefully stopping...\n")
		stopCtx := context.WithoutCancel(ctx)
		for _, c := range inOrder(p, mine, true) {
			if err := r.Client.Stop(stopCtx, c.ID); err != nil && !api.IsNotFound(err) {
				return err
			}
			r.printf(" Container %s  Stopped\n", c.Name)
		}
		return nil
	}
	return err
}
