// Package imagebuild builds the images of a Compose project's `build:`
// sections with go-wasmcontainers/build, through the engine's API: the
// context is read from the project's file system (never outside it),
// the Containerfile (or Dockerfile, or dockerfile_inline) with it.
package imagebuild

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/go-wasmcontainers/build/buildcontext"
	"github.com/go-wasmcontainers/build/builder"
	"github.com/go-wasmcontainers/build/driver"
	"github.com/go-wasmcontainers/compose"
	"github.com/go-wasmcontainers/engine/api"
)

// MaxContainerfile bounds a Containerfile (build's own bound).
const MaxContainerfile = 1 << 20

// Options configure the builder.
type Options struct {
	// FS is the project's file system (compose.Options.FS).
	FS fs.FS
	// Client reaches the engine.
	Client *api.Client
	// Cache keeps build steps between builds (nil: one per build).
	Cache builder.Cache
	// Out receives the build's progress (nil: discarded).
	Out io.Writer
	// Limits bound the context (zero: buildcontext.DefaultLimits).
	Limits buildcontext.Limits
	// Warn receives the build's warnings (nil: dropped).
	Warn func(string)
}

// New returns a compose.BuildFunc building with go-wasmcontainers/build.
func New(o Options) compose.BuildFunc {
	if o.Limits.MaxFiles <= 0 {
		o.Limits = buildcontext.DefaultLimits
	}
	return func(ctx context.Context, p *compose.Project, s *compose.Service, noCache bool) (string, error) {
		if s.Build == nil {
			return "", fmt.Errorf("service %q has no build section", s.Name)
		}
		b := s.Build
		bc, ignored, err := loadContext(o.FS, b.Context, o.Limits)
		if err != nil {
			return "", fmt.Errorf("context %s: %w", b.Context, err)
		}
		df, name, err := containerfile(o.FS, b)
		if err != nil {
			return "", err
		}
		args := map[string]string{}
		for k, v := range b.Args {
			if v != nil { // an argument without a value is left unset (build.md, args)
				args[k] = *v
			}
		}
		tags := append([]string{p.ImageName(s)}, b.Tags...)
		hc, base := o.Client.HTTP()
		// api.Client's base already ends with the API version, which
		// driver.NewClient appends itself.
		base = strings.TrimSuffix(base, "/v"+api.APIVersion)
		res, err := builder.Build(ctx, builder.Options{
			Containerfile: df, Name: name, Context: bc, Ignored: ignored,
			BuildArgs: args, Target: b.Target, Tags: tags, NoCache: noCache,
			Engine: driver.NewClient(hc, base), Cache: o.Cache, Out: o.Out, Limits: o.Limits,
		})
		if err != nil {
			return "", err
		}
		if o.Warn != nil {
			for _, w := range res.Warnings {
				o.Warn(fmt.Sprintf("service %q: %s", s.Name, w))
			}
		}
		return res.ImageID, nil
	}
}

// loadContext packs the context directory of fsys, without what its
// .containerignore (else .dockerignore) excludes. Symbolic links are sent
// as links: the builder follows them only inside the context.
func loadContext(fsys fs.FS, dir string, lim buildcontext.Limits) (*buildcontext.Context, bool, error) {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, false, err
	}
	if st, err := fs.Stat(sub, "."); err != nil {
		return nil, false, err
	} else if !st.IsDir() {
		return nil, false, errors.New("not a directory")
	}
	var tarball bytes.Buffer
	readlink := func(name string) (string, error) { return fs.ReadLink(sub, name) }
	if _, err := buildcontext.PackFS(sub, readlink, &tarball, lim); err != nil {
		return nil, false, err
	}
	bc, err := buildcontext.FromTar(&tarball, lim, "")
	if err != nil {
		return nil, false, err
	}
	_, ignoreFile, err := buildcontext.ReadIgnore(sub)
	if err != nil {
		return nil, false, err
	}
	return bc, ignoreFile != "", nil
}

// containerfile reads the build's Containerfile: dockerfile_inline, else
// dockerfile (relative to the context, inside the project), else the
// context's Containerfile, else its Dockerfile.
func containerfile(fsys fs.FS, b *compose.Build) ([]byte, string, error) {
	if b.DockerfileInline != "" {
		if len(b.DockerfileInline) > MaxContainerfile {
			return nil, "", fmt.Errorf("dockerfile_inline is larger than %d bytes", MaxContainerfile)
		}
		return []byte(b.DockerfileInline), "dockerfile_inline", nil
	}
	names := []string{"Containerfile", "Dockerfile"}
	if b.Dockerfile != "" {
		names = []string{b.Dockerfile}
	}
	for _, n := range names {
		p := path.Clean(path.Join(b.Context, n))
		if strings.HasPrefix(n, "/") || p == ".." || strings.HasPrefix(p, "../") || !fs.ValidPath(p) {
			return nil, "", fmt.Errorf("dockerfile %q is outside the project directory", n)
		}
		f, err := fsys.Open(p)
		if err != nil {
			if b.Dockerfile == "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, "", err
		}
		data, err := io.ReadAll(io.LimitReader(f, MaxContainerfile+1))
		f.Close()
		if err != nil {
			return nil, "", err
		}
		if len(data) > MaxContainerfile {
			return nil, "", fmt.Errorf("%s is larger than %d bytes", p, MaxContainerfile)
		}
		return data, p, nil
	}
	return nil, "", fmt.Errorf("no Containerfile or Dockerfile in %s", b.Context)
}
