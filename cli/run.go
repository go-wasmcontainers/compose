package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/go-wasmcontainers/build/builder"
	"github.com/go-wasmcontainers/compose"
	"github.com/go-wasmcontainers/compose/imagebuild"
	"github.com/spf13/cobra"
)

// runner loads the project and connects to the engine. The project's file
// system stays open (builds read their contexts from it) until the
// command ends (f.release, called by the wrapper Command installs).
func runner(cmd *cobra.Command, opt Options, f *flags, services []string) (*compose.Project, *compose.Runner, error) {
	p, fsys, done, err := loadFS(opt, f)
	if done != nil {
		f.release = append(f.release, done)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := p.EnableServices(services...); err != nil {
		return nil, nil, err
	}
	warn(cmd, p)
	c, err := opt.Client()
	if err != nil {
		return nil, nil, err
	}
	r := &compose.Runner{Client: c, Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
	cache := opt.BuildCache
	if cache == nil && opt.FS == nil {
		// Natively, the cache wctr build keeps; saved when the command ends.
		if path, err := builder.DefaultCachePath(); err == nil {
			fc := builder.OpenFileCache(path, 0)
			cache = fc
			f.release = append(f.release, func() {
				if err := fc.Save(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "WARN the build cache was not saved: %v\n", err)
				}
			})
		}
	}
	r.Build = imagebuild.New(imagebuild.Options{
		FS: fsys, Client: c, Cache: cache, Out: cmd.OutOrStdout(),
		Warn: func(w string) { fmt.Fprintln(cmd.ErrOrStderr(), "WARN", w) },
	})
	return p, r, nil
}

func buildCmd(opt Options, f *flags) *cobra.Command {
	var noCache bool
	cmd := &cobra.Command{
		Use:   "build [SERVICE...]",
		Short: "Build the services' images",
		Long: `Build the images of the services that have a build section, with
go-wasmcontainers/build through the engine. Each image is named by the
service's image, else <project>-<service>, and by its build tags.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := runner(cmd, opt, f, args)
			if err != nil {
				return err
			}
			return r.BuildServices(cmd.Context(), p, args, noCache)
		},
	}
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "run every build step")
	return cmd
}

// byName is the project of commands that work with a name alone (down,
// ps, logs): with -p and no Compose file, the project's resources are
// found by their labels.
func byName(cmd *cobra.Command, opt Options, f *flags) (*compose.Project, *compose.Runner, error) {
	p, r, err := runner(cmd, opt, f, nil)
	if err == nil || f.project == "" || !strings.Contains(err.Error(), "no Compose file found") {
		return p, r, err
	}
	c, cerr := opt.Client()
	if cerr != nil {
		return nil, nil, cerr
	}
	p = &compose.Project{Name: f.project, Services: map[string]*compose.Service{}}
	return p, &compose.Runner{Client: c, Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}, nil
}

func upCmd(opt Options, f *flags) *cobra.Command {
	var o compose.UpOptions
	var detach bool
	cmd := &cobra.Command{
		Use:   "up [SERVICE...]",
		Short: "Create and start the project's containers",
		Long: `Create the project's networks and volumes, pull missing images, and create
and start the services' containers, dependencies first. A container whose
configuration did not change is kept. Without -d, the containers' logs
are followed until they all exit (an interrupt stops them).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := runner(cmd, opt, f, args)
			if err != nil {
				return err
			}
			o.Services = args
			if err := r.Up(cmd.Context(), p, o); err != nil {
				return err
			}
			if detach {
				return nil
			}
			return r.Attach(cmd.Context(), p, args)
		},
	}
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "run in the background")
	cmd.Flags().BoolVar(&o.Build, "build", false, "build images before starting")
	cmd.Flags().BoolVar(&o.ForceRecreate, "force-recreate", false, "recreate containers even if nothing changed")
	cmd.Flags().BoolVar(&o.RemoveOrphans, "remove-orphans", false, "remove containers of services the project no longer defines")
	return cmd
}

func downCmd(opt Options, f *flags) *cobra.Command {
	var volumes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop and remove the project's containers and networks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := byName(cmd, opt, f)
			if err != nil {
				return err
			}
			return r.Down(cmd.Context(), p, volumes)
		},
	}
	cmd.Flags().BoolVarP(&volumes, "volumes", "v", false, "remove the project's volumes too (never external ones)")
	return cmd
}

func psCmd(opt Options, f *flags) *cobra.Command {
	var all, quiet bool
	cmd := &cobra.Command{
		Use:   "ps [SERVICE...]",
		Short: "List the project's containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := byName(cmd, opt, f)
			if err != nil {
				return err
			}
			cs, err := r.Containers(cmd.Context(), p.Name)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if quiet {
				for _, c := range filter(cs, args) {
					if all || c.Live() {
						fmt.Fprintln(out, c.ID)
					}
				}
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "NAME\tIMAGE\tCOMMAND\tSERVICE\tCREATED\tSTATUS")
			for _, c := range filter(cs, args) {
				if !all && !c.Live() {
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%q\t%s\t%s\t%s\n", c.Name, c.Image, truncate(c.Command, 20), c.Service, ago(c.Created), c.Status)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "also stopped containers")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "only IDs")
	return cmd
}

func filter(cs []compose.Container, services []string) []compose.Container {
	if len(services) == 0 {
		return cs
	}
	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}
	var out []compose.Container
	for _, c := range cs {
		if want[c.Service] {
			out = append(out, c)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func ago(unix int64) string {
	d := time.Since(time.Unix(unix, 0)).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%d hours ago", int(d.Hours()))
}

func logsCmd(opt Options, f *flags) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: "Show the services' container logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := byName(cmd, opt, f)
			if err != nil {
				return err
			}
			return r.Logs(cmd.Context(), p, args, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow until the containers exit")
	return cmd
}

func actionCmd(opt Options, f *flags, verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " [SERVICE...]",
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := runner(cmd, opt, f, args)
			if err != nil {
				return err
			}
			switch verb {
			case "start":
				return r.Start(cmd.Context(), p, args)
			case "stop":
				return r.Stop(cmd.Context(), p, args)
			}
			return r.Restart(cmd.Context(), p, args)
		},
	}
}

func rmCmd(opt Options, f *flags) *cobra.Command {
	var stop, force bool
	cmd := &cobra.Command{
		Use:   "rm [SERVICE...]",
		Short: "Remove the services' stopped containers",
		Long: `Remove the services' stopped containers (running ones too with -s).
There is no confirmation question: -f is accepted for compatibility.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := runner(cmd, opt, f, args)
			if err != nil {
				return err
			}
			return r.Rm(cmd.Context(), p, args, stop)
		},
	}
	cmd.Flags().BoolVarP(&stop, "stop", "s", false, "stop running containers first")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "do not ask (there is no question)")
	return cmd
}

func pullCmd(opt Options, f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "pull [SERVICE...]",
		Short: "Pull the services' images",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := runner(cmd, opt, f, args)
			if err != nil {
				return err
			}
			return r.Pull(cmd.Context(), p, args)
		},
	}
}

func imagesCmd(opt Options, f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "images [SERVICE...]",
		Short: "List the images the project's containers use",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, r, err := byName(cmd, opt, f)
			if err != nil {
				return err
			}
			cs, err := r.Containers(cmd.Context(), p.Name)
			if err != nil {
				return err
			}
			imgs, err := r.Client.Images(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "CONTAINER\tREPOSITORY\tTAG\tIMAGE ID\tSIZE")
			for _, c := range filter(cs, args) {
				repo, tag, id, size := c.Image, "", c.ImageID, int64(0)
				for _, im := range imgs {
					if im.ID != c.ImageID {
						continue
					}
					size = im.Size
					for _, rt := range im.RepoTags {
						if i := strings.LastIndex(rt, ":"); i > 0 {
							repo, tag = rt[:i], rt[i+1:]
							break
						}
					}
				}
				id = strings.TrimPrefix(id, "sha256:")
				if len(id) > 12 {
					id = id[:12]
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, repo, tag, id, humanSize(size))
			}
			return tw.Flush()
		},
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.3gGB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.3gMB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.3gkB", float64(n)/1e3)
	}
	return fmt.Sprintf("%dB", n)
}
