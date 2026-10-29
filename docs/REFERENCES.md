# References

Every format and rule here is implemented from its specification. This is
the list of what was opened, and what each one decides. Revisions are the
ones read on 2026-10-27.

## The Compose Specification

<https://github.com/compose-spec/compose-spec> at `3a4a6995e4a9`
(2026-10-23), Apache-2.0. Its `spec.md` and `schema/compose-spec.json` are
copied, with their LICENSE and NOTICE, into `testdata/compose-spec/` and
`schema/`.

| Section | Decides |
|---|---|
| `02-model.md` | resources are prefixed with the project name and carry the label `com.docker.compose.project`; project names are `[a-z0-9][a-z0-9_-]*` |
| `03-compose-file.md` | default file names (`compose.yaml` preferred, `compose.yml`, legacy `docker-compose.yaml`/`.yml`); several files merge; relative paths resolve from the first file's directory |
| `04-version-and-name.md` | `version` is obsolete (warning); `name` is the default project name, exposed as `COMPOSE_PROJECT_NAME` |
| `05-services.md` | `container_name` (no scaling past one); `extends` and its merge rules (mappings, sequences with de-duplication, scalars; volumes keyed by target); `profiles` names; `restart` values; `scale` |
| `container_spec.md` | `command`/`entrypoint` (`null` = the image's, `[]` = cleared), `env_file` (required, format `raw`, the env-file syntax), `environment` (a bare name takes the environment's value or is unset), `labels` (`com.docker.compose` prefix reserved: an error), `network_mode`, `networks` (implicit `default`, `aliases`), `volumes` short and long syntax |
| `workload_spec.md` | `depends_on` short and long syntax (`service_started`, `service_healthy`, `service_completed_successfully`, `restart`, `required`), `healthcheck` (test forms `NONE`/`CMD`/`CMD-SHELL`, a string meaning `CMD-SHELL`), `ports` (short syntax `[HOST:]CONTAINER[/PROTOCOL]` with `HOST` = `[IP:](port\|range)`, bare or bracketed IPv6, ranges of equal length; long syntax `target`, `published`, `host_ip`, `protocol`, `app_protocol`, `mode`, `name`; an absent host IP is `0.0.0.0` in the specification, 127.0.0.1 here, on purpose), `expose` |
| `06-networks.md` | the implicit `default` network; `external`; `name` |
| `07-volumes.md` | named volumes; `external` (only `name` allowed besides it); `name` not scoped; driver unavailable = error; labels `com.docker.compose.project`/`.volume` |
| `10-fragments.md` | anchors and aliases resolve before interpolation; YAML merge keys |
| `11-extension.md` | `x-` keys are ignored by Compose and allowed wherever keys are not user-defined; durations and byte values |
| `12-interpolation.md` | `$VAR`, `${VAR}`, `:-`, `-`, `:?`, `?`, nesting, `$$`; values only, not keys; per file, before merging |
| `13-merge.md` | mappings merge, sequences append; `command`, `entrypoint`, `healthcheck.test` replace; `volumes`/`secrets`/`configs` unique by target, `ports` by its key; `!reset`, `!override` |
| `14-include.md` | `include` short and long syntax; each entry is its own project (own directory, own `.env`, the local environment winning); conflicts are reported |
| `15-profiles.md` | services without `profiles` are always on; a targeted service enables its profile; a dependency on a disabled service is an error |
| `schema/compose-spec.json` (draft 2020-12) | the attributes defined at each place, and which places are closed (`additionalProperties`/`unevaluatedProperties: false`): the loader's unknown-attribute errors come from it at run time; the full validation runs in the tests (`TestSpecExamples`) |

## compose-go, the reference loader

<https://github.com/compose-spec/compose-go> v2.16.2 (`6ac9323ede62`),
Apache-2.0. **Evaluated for reuse and not reused**, measured on
2026-10-27 (go1.27.1, `GOOS=js GOARCH=wasm`, `-trimpath -ldflags=-s`):

| Build | `.wasm` size |
|---|---|
| baseline (`encoding/json` + `fmt`) | 4.44 MB |
| compose-go `cli.LoadProject` (YAML and JSON only) | 9.76 MB (+5.32 MB, 44 non-standard packages: logrus, x/text/message, jsonschema, mapstructure, distribution/reference, go-connections, …) |
| compose-go + HCL2 (`hclparse`), what reuse would cost | 12.52 MB (+8.08 MB) |
| this module, YAML + JSON + HCL2 loading and writing | 9.84 MB (+5.40 MB) |

Loading a 50-service file (`BenchmarkLoad50`, one core, Apple M4 Max,
1-minute load 35–50, so indicative): compose-go 49 ms/op and 674k
allocations; this loader 2.1 ms/op (YAML), 0.93 ms (JSON), 6.4–7.3 ms
(HCL2) — compose-go also runs the full schema validation, which here runs
in the tests only. compose-go also reads the host's files and environment
directly; the browser has neither.

What was read from it and followed:

| File | Decides |
|---|---|
| `template/template.go` | interpolation details the specification leaves open: names are `[_a-zA-Z][_a-zA-Z0-9]*`; a `$` starting no name is kept; `:+`/`+`; braces in a default are counted (`${A:-{x}}`); `:?` messages are interpolated |
| `dotenv/parser.go`, `godotenv.go` | env files: `export ` prefix, `KEY: value`, quoted values over several lines, escapes in single quotes kept except `\'`, `\$`, `\0NNN`; a value is interpolated against earlier keys, the environment winning |
| `loader/loader.go` `NormalizeProjectName` | a directory name becomes a project name (lowercase, `[a-z0-9_-]`, no leading `-`/`_`) |
| `loader/normalize.go` `setNameFromKey` | a network or volume without `name` is `<project>_<key>`, an external one `<key>` |

## The reference implementation's labels

`docker/compose` `pkg/api/labels.go` (read through the GitHub API on
2026-10-27). The specification mandates `com.docker.compose.project` and
`com.docker.compose.service` and reserves the prefix; the other names
`up` will set (`config-hash`, `container-number`, `network`, `volume`,
`oneoff`, `depends_on`) are taken from that file so that tools which read
them find them. They are label names, the API's vocabulary, not
a product name.

## YAML

- **YAML 1.2.2** <https://yaml.org/spec/1.2.2/>: §3.2.1.1/§3.2.1.3 mapping
  keys are unique (duplicates are errors here); §3.2.2.2 an alias refers to
  the most recent node with its anchor; §10.3.2 the core schema's tag
  resolution (`true`/`True`/`TRUE`, `null`/`~`, integers base 10 — so
  `0800` is 800 — `0o` octal, `0x` hexadecimal, floats, `.inf`, `.nan`).
- **Merge key type** <https://yaml.org/type/merge.html> (a YAML 1.1 type
  the Compose Specification uses): `<<` takes a mapping or a sequence of
  mappings; the mapping's own keys win, then earlier sources over later
  ones.
- Parser: `go.yaml.in/yaml/v3` v3.0.5 (the YAML organisation's
  continuation of `gopkg.in/yaml.v3`), used for its node tree only:
  aliases, merge keys, duplicate keys, tags and bounds are handled here
  (`tree/yaml.go`).

## JSON

RFC 8259, through `encoding/json`'s token decoder (`tree/json.go`), which
keeps key order and offsets: duplicate keys and trailing content are
refused.

## HCL2

<https://github.com/hashicorp/hcl> v2.25.0, MPL-2.0:

- `hclsyntax/spec.md`, the native syntax specification: structural
  elements (attributes, blocks with labels, "each distinct attribute name
  may be defined no more than once within a single body"); variables come
  from the calling application's global scope; functions likewise
  (none here); templates: `${` and `%{` are escaped by doubling (`$${`,
  `%%{`); for expressions and directives.
- `hclsyntax/parser.go`: the recursive descent (unary operators,
  parentheses, conditionals and templates recurse; binary operators loop
  but build left-deep trees) — the reason for the token pre-scan bounds.
- `hclwrite/generate.go` `escapeQuotedStringLit`: how a string is written
  back (`\n`, `\r`, `\t`, `\"`, `\\`, doubling `$`/`%` before `{`,
  `\uNNNN` for non-printables); `config --format hcl` writes the same
  escapes without importing `hclwrite` (which would bring `go-cmp` into the
  browser build).

## The engine

<https://github.com/go-wasmcontainers/engine> v0.21.0 (v0.12.0 before
compose v0.3.0, v0.14.0 before v0.3.1, v0.18.0 before v0.3.2): `api`
(`Filters`; `Client.ContainersFiltered`, `NetworksFiltered`,
`VolumesFiltered` — v0.18.0's variadic filters were withdrawn because
they changed the methods' types, engine#31), `api/server/filters.go`, `server.go`, `networks.go`,
`volumes.go` (the `filters` parameter: `label` on containers, networks and
volumes, `k`, `k=v`, `k!=v`), `daemon/networks.go` and `daemon/daemon.go`
(`NetworkingConfig.EndpointsConfig` at create: every network named there is
joined, API 1.44+ shape, each with its aliases), and `README.md`,
`api/types.go`, `api/client.go` (the client's methods), `api/stdcopy.go`
(the multiplexed log stream), `api/inproc/inproc.go`, `cli/cli.go`
(`Options.Extra`, `Engine`: how `wctr compose` is wired),
`container/container.go` (wait conditions `not-running`, `next-exit`,
`removed`), `cmd/wctrd/main.go` and `cli/cli_test.go` (how an engine is
assembled in-process). Compose drives it only through its API, a subset of
the Docker Engine API v1.47: `GET /containers/json?all=1`, `POST
/containers/create`, `start`, `stop`, `restart`, `wait`, `GET .../logs`,
`DELETE /containers/{id}`, `GET`/`POST /networks`, `DELETE
/networks/{id}`, `GET`/`POST /volumes`, `DELETE /volumes/{name}`, `POST
/images/create`, `GET /images/{name}/json`, `GET /images/json`.

## The builder

The Compose Build Specification, `build.md` of compose-spec at the same
revision: `context` relative to the Compose file's directory, or a Git
URL; `dockerfile` relative to the context; `dockerfile_inline` exclusive
with it; `args` (a name alone: no value unless supplied); `target`; `tags`
in addition to `image`; `no_cache`; with both `build` and `image`, the
image is pulled first and built if it is not found.

<https://github.com/go-wasmcontainers/build> v0.2.1: `README.md`,
`builder` (`Build`, `Options`, `Result`, caches), `buildcontext`
(`PackFS`, `FromTar`, `ReadIgnore`, limits), `driver` (`NewClient`, which
from v0.2.1 accepts a base with or without the trailing API version:
`TestDriverBaseForms` checks both against the engine), and
`cli/cli.go` (how `wctr build` finds its Containerfile and cache, which
compose follows).

Engine v0.22.0 `portmap` (package documentation): `ParseSpec`/`ParseSpecs`
read `-p` values as Docker's `nat.ParsePortSpec` does and are what compose
checks each `ports` entry with and turns into `HostConfig.PortBindings`;
an empty host address listens on 127.0.0.1 (`DefaultHostIP`); bindings
are bounded (`MaxBindings`); `api.ContainerConfig.ExposedPorts` carries
`expose`.

Restart: `05-services.md` `restart` (`no`, `always`, `on-failure[:max-retries]`,
`unless-stopped`) and `deploy.md` `restart_policy` (`condition` none /
on-failure / any, the default; `delay`, `max_attempts`, `window`; "If
`restart_policy` is not set, Compose considers the `restart` field").
Engine v0.25.0 `api.HostConfig.RestartPolicy` (`Name` no / always /
unless-stopped / on-failure, `MaximumRetryCount`), its backoff (100 ms
doubling to 1 min), a user stop ending the policy until the next start, and
the `restarting` state, which compose treats as running for `stop`, `rm`,
`start`, `up` and `ps`.

cobra v1.10.2 `command.go`: `TraverseChildren` (flags of each command are
parsed before its child's, on the command `Execute` starts from) — how the
global `-f/--file` and `logs -f/--follow` coexist.
