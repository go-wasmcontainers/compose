# compose

[![Docs](https://img.shields.io/badge/docs-hugo%20%2B%20hextra-0079A8)](https://go-wasmcontainers.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![CI](https://github.com/go-wasmcontainers/compose/actions/workflows/ci.yml/badge.svg)](https://github.com/go-wasmcontainers/compose/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-wasmcontainers/compose.svg)](https://pkg.go.dev/github.com/go-wasmcontainers/compose)
![status](https://img.shields.io/badge/status-M1-green)

**Multi-container applications for the
[go-wasmcontainers engine](https://github.com/go-wasmcontainers/engine)**,
whose containers are WASI preview 1 (`wasip1`) WebAssembly modules: `wctr
compose` reads an application described by the
[Compose Specification](https://compose-spec.io) and runs it through the
engine's API. Compose files are accepted in **three formats — YAML, JSON
and HCL2** — which load to the same model. Pure Go (`CGO_ENABLED=0`); it
builds for `GOOS=js` and `GOOS=wasip1`, because it runs in the browser
[playground](https://github.com/go-wasmcontainers/playground) too.

> **Status: M1.** Loading is complete — discovery, YAML/JSON/HCL2,
> interpolation, `.env`, merging of several files, `extends`, `include`,
> profiles, validation against the specification's schema — and the
> application runs on the engine: `up`, `down`, `ps`, `logs`, `start`,
> `stop`, `restart`, `rm`, `pull`, `images`, `build`, `config` — services
> reach each other by service name and aliases, on as many networks as
> they declare, `ports` are published on the engine's host, and `restart`
> policies are applied (engine v0.25.0 or later). What the engine cannot
> do yet (health checks) is listed in [Limitations](#limitations).

## Demo

A one-shot service fills a volume, a server serves it read-only on the
project's network, a client fetches it by the server's name — the
`TestLab` integration test, run against an in-process engine:

```yaml
name: lab
services:
  init:
    image: labapp
    command: [write, /data/msg, "hello from a volume"]
    volumes: [data:/data]
  web:
    image: labapp
    command: [serve, "8080", /data/msg]
    environment: [GREETING]
    volumes: ["data:/data:ro"]
    depends_on:
      init: {condition: service_completed_successfully}
  client:
    image: labapp
    command: [get, "http://web:8080/"]
    depends_on: [web]
volumes:
  data: {}
```

```console
$ GREETING=hi wctr compose up -d
 Network lab_default  Created
 Volume lab_data  Created
 Container lab-init-1  Created
 Container lab-init-1  Started
 Container lab-init-1  Exited (0)
 Container lab-web-1  Created
 Container lab-web-1  Started
 Container lab-client-1  Created
 Container lab-client-1  Started
$ wctr compose logs client
lab-client-1 | got: hello from a volume (greeting hi)
$ wctr compose down -v
```

`labapp` is a stock Go `wasip1` program using
[stealthrocket/net](https://github.com/stealthrocket/net)
(`testdata/wasm/labapp`).

`config` validates a file and prints the normalised model, here from HCL2
to YAML:

```console
$ go install github.com/go-wasmcontainers/compose/cmd/wctr-compose@latest
$ cat compose.hcl
variable "TAG" {
  default = "1.0"
}

service "web" {
  image = "localhost:5000/lab/web:${var.TAG}"
  network "front" {
    aliases = ["www"]
  }
  depends_on "db" {
    condition = "service_healthy"
  }
}

service "db" {
  image = "localhost:5000/lab/db:1"
  volume {
    type   = "volume"
    source = "data"
    target = "/var/lib/db"
  }
}

network "front" {}
volume "data" {}
$ TAG=1.2 wctr-compose config --format yaml
name: lab
services:
  db:
    image: localhost:5000/lab/db:1
    networks:
      default: {}
    volumes:
      - type: volume
        source: data
        target: /var/lib/db
  web:
    depends_on:
      db:
        condition: service_healthy
        required: true
    image: localhost:5000/lab/web:1.2
    networks:
      front:
        aliases:
          - www
networks:
  default:
    name: lab_default
  front:
    name: lab_front
volumes:
  data:
    name: lab_data
```

`wctr-compose` is the command on its own (`-H` names the engine's socket,
as for `wctr`); inside `wctr` it is `wctr compose`, wired through wctr's
`cli.Options.Extra` with wctr's own engine connection (natively its `-H`,
in the playground the in-process transport).

## Compose files in three formats

| File | Format |
|---|---|
| `compose.yaml`, `compose.yml` | YAML 1.2, with anchors, aliases and `<<` merge keys |
| `compose.json` | JSON |
| `compose.hcl` | HCL2 native syntax: `service "web" { ... }` blocks, `var.NAME` variables in a static context |

[docs/HCL.md](docs/HCL.md) specifies the HCL2 mapping, with one application
written side by side in the three formats; a test loads the three to the
identical model, and `config --format yaml|json|hcl` output loads back to
the same model (tested and fuzzed). Files of different formats merge:
`-f compose.yaml -f override.hcl`.

## Commands

`wctr compose [-f FILE]... [-p NAME] [--profile P]... [--project-directory DIR] [--env-file FILE]... COMMAND`

The global flags come **before** the command, as in the compose CLI: `-f`
is `--file` there, and `--follow` after `logs`.

| Command | Flags |
|---|---|
| `up [SERVICE...]` | `-d/--detach`, `--force-recreate`, `--build`, `--remove-orphans`. Builds or pulls missing images (see Builds), creates the project's networks and volumes, creates and starts containers dependencies first; a container whose configuration hash and image did not change is kept (started again if it stopped). Without `-d`, follows the logs until every container exits; an interrupt stops them |
| `down` | `-v/--volumes` (never external ones). Works with `-p NAME` alone, with no Compose file |
| `ps [SERVICE...]` | `-a/--all`, `-q/--quiet` |
| `logs [SERVICE...]` | `-f/--follow`; lines prefixed with the container's name |
| `start`, `stop`, `restart` `[SERVICE...]` | dependencies first (stop: dependents first); `stop` also stops a container waiting to restart, and ends its restart policy until the next start |
| `rm [SERVICE...]` | `-s/--stop`; stopped containers only otherwise; `-f` accepted (there is no question) |
| `pull [SERVICE...]` | |
| `images [SERVICE...]` | |
| `build [SERVICE...]` | `--no-cache` |
| `config [SERVICE...]` | `--format yaml\|json\|hcl` (default `yaml`), `-q/--quiet` (validate only), `--services` (names only) |

**Builds.** A service's `build:` section is built with
[go-wasmcontainers/build](https://github.com/go-wasmcontainers/build)
v0.2.1 through the engine (its exported `builder.Build`; package
`imagebuild` here): `context` (a directory of the project, resolved from
the Compose file's directory; its `.containerignore`, else `.dockerignore`,
applies), `dockerfile` (relative to the context, inside the project;
default `Containerfile`, else `Dockerfile`), `dockerfile_inline`, `args` (a
name alone takes the environment's value, else is left unset), `target`,
`tags`, `no_cache`. The image is named by the service's `image`, else
`<project>-<service>`. `up` builds an image that is missing (a service with
an `image` is pulled first and built if the pull fails, as build.md says),
`up --build` and `compose build` always build — through the builder's
cache, so an unchanged build gives the same image and the containers are
kept. Natively the cache is `wctr build`'s
(`$XDG_CACHE_HOME/wasmcontainers/build-cache.json`); in a browser the host
passes one (`cli.Options.BuildCache`).

Resources are named and labelled as the specification says: containers
`<project>-<service>-<n>` (or `container_name`), networks and volumes
`<project>_<key>` (or `name`; external ones by their key), every one
labelled `com.docker.compose.project`, containers also
`com.docker.compose.service`, `.container-number`, `.config-hash`,
`.image`, `.oneoff`, `.depends_on`, networks `.network`, volumes `.volume`.
The engine lists a project's resources by that label itself (Docker's
`filters` parameter).

**Networks**: a container joins every network its service declares, at
creation; on each it answers to its own name, to the service's name and
to the aliases it has there — an alias or a service name shared by
several replicas resolves to all of them. Services on no common network
cannot reach each other (tested: a front-only service cannot resolve a
back-only one).

**Dependencies**: `service_started` waits for the dependency's containers
to be started; `service_completed_successfully` waits for them to exit and
fails `up` if one exited non-zero; `service_healthy` **fails at once**
(`the engine does not run health checks yet`) instead of waiting forever —
or warns and carries on when the dependency has `required: false`.

Without `-f`, the project directory is searched for `compose.yaml`,
`compose.yml`, `compose.json`, `compose.hcl`, then the specification's
legacy names, and the first `compose.override.{yaml,yml,json,hcl}` found is
merged on top. The project name is `-p`, else `COMPOSE_PROJECT_NAME`, else
the top-level `name`, else the project directory's name.

## What is loaded

Everything the specification defines is parsed; what this engine applies:

- **Services**: `image`, `build` (`context`, `dockerfile`,
  `dockerfile_inline`, `args`, `target`, `tags`, `no_cache`), `command`,
  `entrypoint`, `environment`, `env_file` (with `required` and `format:
  raw`), `working_dir`, `volumes` (named volumes), `networks` (with
  `aliases`), `ports` (short and long syntax), `expose`, `network_mode: none`, `depends_on` (`service_started`,
  `service_healthy`, `service_completed_successfully`, `required`,
  `restart`), `healthcheck`, `deploy.replicas` / `scale` (at most 64),
  `labels`, `profiles`, `container_name`, `restart` (`no`, `always`,
  `on-failure[:N]`, `unless-stopped`) and `deploy.restart_policy`
  (`condition` none/on-failure/any → no/on-failure/always, `max_attempts`;
  it wins over `restart`, as deploy.md says; `delay` and `window` are
  warned about: the engine's backoff, 100 ms doubling to 1 min, applies),
  `x-` extensions.
- **Top level**: `name`, `services`, `networks` (`name`, `external`,
  `internal`, `labels`), `volumes` (`name`, `external`, `labels`),
  `include`, `x-` extensions; `version` is obsolete (warning).
- **Loading rules**: interpolation (`${VAR}`, `:-`, `-`, `:?`, `?`, `:+`,
  `+`, `$$`) per file from the explicit environment over `.env`; merging
  (`!reset`, `!override`, `volumes` by target); `extends` (in the file or
  another one); `include` (each its own project); profiles (`--profile`,
  `COMPOSE_PROFILES`).
- **Validation**: an attribute the specification does not define is an
  error (the specification's JSON schema, embedded, says which); one it
  defines that this engine does not apply (`cap_add`, `logging`, ...) is a
  warning, printed by every
  command. References (services, networks, volumes), dependency cycles,
  label prefixes, restart policies, durations are checked. Every error
  carries `file:line:column`.

The specification's own examples (75 Compose files in its text) go through
both the official JSON schema and this loader in the tests: whatever the
schema refuses is refused; what only this loader refuses is a deliberate
refusal listed below.

## Library

```go
p, err := compose.Load(compose.Options{
	FS:          os.DirFS("lab"),          // nothing outside it is read
	Environment: map[string]string{"TAG": "1.2"},
	Profiles:    []string{"debug"},
})
out, err := compose.Marshal(p, compose.HCL)  // or compose.YAML, compose.JSON
```

| Package | What it does |
|---|---|
| `compose` | `Load` (discovery, interpolation, merge, `extends`, `include`, profiles, validation) into `Project`; `Marshal` in the three formats; `Runner` drives the engine through its API client only (`Up`, `Down`, `Start`, `Stop`, `Restart`, `Rm`, `Pull`, `Logs`, `Attach`, `Containers`) |
| `tree` | the position-carrying document the three readers produce: `ParseYAML`, `ParseJSON`, `ParseHCL`, all bounded |
| `interp` | the specification's variable interpolation, bounded, never executing anything |
| `dotenv` | env files in the specification's syntax |
| `schema` | the embedded official JSON schema: which attributes exist where |
| `imagebuild` | `New(Options) compose.BuildFunc`: builds `build:` sections with go-wasmcontainers/build, the context read from the project's file system only |
| `cli` | the cobra command tree, `Command(Options) *cobra.Command`, for `wctr compose` |
| `cmd/wctr-compose` | the command on its own |

## Performance

`BenchmarkLoad50` loads a 50-service file (each with a command, environment,
labels, two networks, a volume, a dependency and a health check). One core,
Apple M4 Max, go1.27.1 — the machine's 1-minute load was 35–50, so the
numbers are indicative:

| Format | Size | Time | Allocations |
|---|---|---|---|
| YAML | 34 KB | 2.1 ms | 20k |
| JSON | 51 KB | 0.93 ms | 11k |
| HCL2 | 33 KB | 6.4–7.3 ms | 59k (the HCL lexer's token buffer dominates) |

The reference loader, compose-go v2.16.2, takes 49 ms and 674k
allocations on the same YAML file (it also runs the full JSON-schema
validation, which here runs in the tests). In a `GOOS=js` build, loading and
writing the three formats weighed 5.4 MB over a baseline program, of which
HCL2 (hcl + go-cty) is most; compose-go plus HCL2 would weigh 8.1 MB
([docs/REFERENCES.md](docs/REFERENCES.md)). Since the package also holds
the engine driver, a program using it links the engine's API client
(`net/http`) too: 11.5 MB in all against 4.4 MB for the baseline — inside
wctr, which links that client already, the loader is the difference.

## Security

Compose files are untrusted input: a teacher hands one to a class, the
playground loads one from a page.

- **Bounded readers**: 4 MiB per file, 64 levels of nesting, 262144 nodes
  after YAML alias expansion (the "billion laughs" document is refused, not
  expanded), 1 MiB per scalar, 64 MiB for all interpolations of a file
  together, 64 files per load (with `include` and `extends`), 8 levels of
  `include`, 16 of `extends`, 1000 services, 64 replicas per service, 256
  containers per project.
- **HCL2 static context**: no functions, no `for`; bracket nesting and the
  number of operators are checked on the tokens before the parser runs (it
  recurses on them); interpolations × the longest variable are bounded.
- **No execution**: interpolation looks values up in a map; nothing is ever
  run by a shell.
- **No file outside the project**: every path a Compose file names
  (`env_file`, `include`, `extends.file`, `--env-file`) resolves inside the
  project directory; absolute paths and `..` past the top are refused, and
  natively the directory is opened with `os.OpenRoot`, so a symbolic link
  cannot leave it either. Build contexts and `dockerfile` paths too; remote
  (Git URL) build contexts are refused. **No host bind mounts**: a WebAssembly engine has
  no host filesystem to share (in a browser there is none).
- **Fuzzed** in CI: the YAML, JSON and HCL2 readers, the interpolator, the
  env-file parser, and `Load` with its round trip through the three
  formats. `govulncheck` runs on every change.

## Limitations

- What the engine cannot do yet, and compose therefore does not:
  - **health checks**: `service_healthy` fails clearly (the engine cannot
    run a command in a container yet).
- **Ports differ from the specification on one point, on purpose**: a
  port that names no host address (`"8080:80"`, or long syntax without
  `host_ip`) is published on **127.0.0.1**, the engine's default, not on
  every interface (`0.0.0.0`) as the specification says — a lab container
  should not become a service on the machine's network by accident. Write
  `"0.0.0.0:8080:80"` (or `host_ip: 0.0.0.0`) when that is meant. `mode`,
  `name` and `app_protocol` are kept in the model and have no effect.
- Builds: remote (Git URL) contexts, `additional_contexts`, `cache_from`
  / `cache_to`, `platforms`, `secrets`, `ssh`, build `labels` and the other
  build attributes are accepted with a warning and not applied; what the
  builder itself does not do yet is in go-wasmcontainers/build's README
  (shell-form `RUN` needs process spawning).
- An engine older than v0.14.0 does not filter lists by label; compose
  refuses to act on what such an engine lists for another project (it
  checks every answer's label) rather than touch it.
- `wctr compose -f FILE ...` needs wctr's root command to set cobra's
  `TraverseChildren` (global flags before the subcommand); the standalone
  `wctr-compose` sets it.
- Refused on purpose: host bind mounts, `tmpfs`/`image`/`npipe` mounts,
  anonymous volumes, `network_mode` other than `none`, network drivers
  other than `bridge`, volume drivers other than `local`, files outside the
  project directory, `image: oci:<dir>` (an OCI layout on the host: `wctr
  pull` it first, then name the image), infinities and NaN.
- Accepted with a warning and not applied: `secrets`, `configs`, `models`, and every other attribute the engine has
  no use for.
- `include` conflicts are errors (the specification asks for a warning).
- An `x-` extension whose name is not an HCL identifier cannot be written
  in HCL2 (`config --format hcl` says so).

## References

Every format and rule is implemented from its specification; the list, with
what each decides, is in [docs/REFERENCES.md](docs/REFERENCES.md). "Compose
Specification" is the name of the open specification at compose-spec.io;
the label names it mandates (`com.docker.compose.project`, ...) are used as
the specification says. Docker is a trademark of Docker, Inc.; this project
is not affiliated with it.

## License

BSD 3-Clause, see [LICENSE](LICENSE). `schema/compose-spec.json` and
`testdata/compose-spec/` are the Compose Specification's, Apache-2.0 (their
LICENSE files are beside them).
