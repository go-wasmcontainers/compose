// Package cli is the `wctr compose` command tree: an exported cobra
// command the engine's wctr wires as a subcommand, and that
// cmd/wctr-compose runs on its own.
package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-wasmcontainers/compose"
	"github.com/spf13/cobra"
)

// Options are what the host gives the command tree.
type Options struct {
	// FS is the file system Compose files are read from, its root the
	// top the project may not leave. Nil: the host's, rooted at the
	// project directory (os.OpenRoot: neither `..` nor a symbolic link
	// leaves it). A browser host passes its own.
	FS fs.FS
	// Environ is the environment interpolation reads. Nil: the process
	// environment (os.Environ).
	Environ func() []string
}

// flags are the global flags of `compose`.
type flags struct {
	files      []string
	project    string
	profiles   []string
	projectDir string
	envFiles   []string
}

// Command returns the `compose` command.
func Command(opt Options) *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Define and run multi-container applications (Compose files in YAML, JSON or HCL2)",
		Long: `Define and run multi-container applications described by the Compose
Specification (compose-spec.io), with WebAssembly (wasip1) containers.

Compose files may be written in YAML, JSON or HCL2 (compose.yaml,
compose.json, compose.hcl); see docs/HCL.md for the HCL2 mapping.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := cmd.PersistentFlags()
	pf.StringArrayVarP(&f.files, "file", "f", nil, "Compose file (repeatable; later files override earlier ones)")
	pf.StringVarP(&f.project, "project-name", "p", "", "project name")
	pf.StringArrayVar(&f.profiles, "profile", nil, "activate a profile (repeatable)")
	pf.StringVar(&f.projectDir, "project-directory", "", "project directory (default: the first file's directory)")
	pf.StringArrayVar(&f.envFiles, "env-file", nil, "environment file (repeatable; default .env in the project directory)")
	cmd.AddCommand(configCmd(opt, f))
	return cmd
}

// load reads the project the flags describe.
func load(opt Options, f *flags) (*compose.Project, error) {
	o := compose.Options{ProjectName: f.project, Profiles: f.profiles, Environment: environ(opt)}
	if opt.FS != nil {
		o.FS = opt.FS
		o.WorkingDir = f.projectDir
		o.Files = f.files
		o.EnvFiles = f.envFiles
		return compose.Load(o)
	}
	dir := f.projectDir
	if dir == "" {
		dir = "."
		if len(f.files) > 0 {
			dir = filepath.Dir(f.files[0])
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	defer root.Close() // Load reads every file before it returns
	o.FS = root.FS()
	o.WorkingDir = "."
	rel := func(p string) (string, error) {
		a, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		r, err := filepath.Rel(abs, a)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s is outside the project directory %s", p, abs)
		}
		return filepath.ToSlash(r), nil
	}
	for _, file := range f.files {
		r, err := rel(file)
		if err != nil {
			return nil, err
		}
		o.Files = append(o.Files, r)
	}
	for _, e := range f.envFiles {
		r, err := rel(e)
		if err != nil {
			return nil, err
		}
		o.EnvFiles = append(o.EnvFiles, r)
	}
	// The FS is rooted at the project directory: its name is the
	// default project name.
	o.DefaultName = compose.NormalizeProjectName(filepath.Base(abs))
	return compose.Load(o)
}

func environ(opt Options) map[string]string {
	get := os.Environ
	if opt.Environ != nil {
		get = opt.Environ
	}
	env := map[string]string{}
	for _, kv := range get() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}

func warn(cmd *cobra.Command, p *compose.Project) {
	for _, w := range p.Warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "WARN", w)
	}
}

func configCmd(opt Options, f *flags) *cobra.Command {
	var format string
	var quiet, services bool
	cmd := &cobra.Command{
		Use:   "config [SERVICE...]",
		Short: "Validate the Compose files and print the normalised model",
		Long: `Validate the Compose files and print the normalised model: merged,
interpolated, in long syntax, keys sorted. --format writes it as YAML,
JSON or HCL2; loading what it prints gives the same model back.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load(opt, f)
			if err != nil {
				return err
			}
			if err := p.EnableServices(args...); err != nil {
				return err
			}
			warn(cmd, p)
			if len(args) > 0 {
				keep := map[string]bool{}
				for _, a := range args {
					keep[a] = true
				}
				for n := range p.Services {
					if !keep[n] {
						delete(p.Services, n)
					}
				}
			}
			switch {
			case quiet:
				return nil
			case services:
				for _, n := range p.ServiceNames() {
					fmt.Fprintln(cmd.OutOrStdout(), n)
				}
				return nil
			}
			out, err := compose.Marshal(p, compose.Format(format))
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "output format: yaml, json or hcl")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "only validate")
	cmd.Flags().BoolVar(&services, "services", false, "print the service names")
	return cmd
}
