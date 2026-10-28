# Compose files in HCL2

`wctr compose` reads Compose files in three formats: YAML (`compose.yaml`),
JSON (`compose.json`) and HCL2 native syntax (`compose.hcl`). All three
describe the same model, the Compose Specification's, and load to the same
internal value: this page is the specification of the HCL2 mapping, and
`TestThreeFormatsOneModel` checks it on the example below
(`testdata/lab/compose.{yaml,json,hcl}`).

The format is chosen by the file extension (`.yaml`/`.yml`, `.json`,
`.hcl`). Files of different formats can be merged: `-f compose.yaml -f
compose.override.hcl` is valid.

## The same application, three ways

<table>
<tr><th>YAML</th><th>JSON</th><th>HCL2</th></tr>
<tr><td>

```yaml
name: lab

x-common: &common
  labels:
    org.example.lab: "true"

services:
  web:
    <<: *common
    image: ${REGISTRY:-localhost:5000}/web:${TAG:?set TAG}
    command: serve --port 8080 --greeting "hello $$USER"
    environment:
      - MODE=production
      - TAG
    networks:
      front:
        aliases: [www]
      back: {}
    depends_on:
      db:
        condition: service_healthy
    deploy:
      replicas: 2

  db:
    image: localhost:5000/db:1
    volumes:
      - data:/var/lib/db
    networks: [back]
    healthcheck:
      test: ["CMD", "/bin/probe", "ready"]
      interval: 5s
      retries: 3

networks:
  front: {}
  back: {}

volumes:
  data:
```

</td><td>

```json
{
  "name": "lab",
  "x-common": {
    "labels": { "org.example.lab": "true" }
  },
  "services": {
    "web": {
      "image": "${REGISTRY:-localhost:5000}/web:${TAG:?set TAG}",
      "labels": { "org.example.lab": "true" },
      "command": ["serve", "--port", "8080",
                  "--greeting", "hello $$USER"],
      "environment": { "MODE": "production", "TAG": null },
      "networks": {
        "front": { "aliases": ["www"] },
        "back": {}
      },
      "depends_on": {
        "db": { "condition": "service_healthy" }
      },
      "deploy": { "replicas": 2 }
    },
    "db": {
      "image": "localhost:5000/db:1",
      "volumes": [
        { "type": "volume", "source": "data",
          "target": "/var/lib/db" }
      ],
      "networks": ["back"],
      "healthcheck": {
        "test": ["CMD", "/bin/probe", "ready"],
        "interval": "5s",
        "retries": 3
      }
    }
  },
  "networks": { "front": {}, "back": {} },
  "volumes": { "data": null }
}
```

</td><td>

```hcl
name = "lab"

x-common = {
  labels = { "org.example.lab" = "true" }
}

variable "REGISTRY" {
  default = "localhost:5000"
}

service "web" {
  image   = "${var.REGISTRY}/web:${var.TAG}"
  command = ["serve", "--port", "8080",
             "--greeting", "hello $USER"]
  labels  = { "org.example.lab" = "true" }

  environment = {
    MODE = "production"
    TAG  = var.TAG
  }

  network "front" {
    aliases = ["www"]
  }
  network "back" {}

  depends_on "db" {
    condition = "service_healthy"
  }

  deploy {
    replicas = 2
  }
}

service "db" {
  image = "localhost:5000/db:1"

  volume {
    type   = "volume"
    source = "data"
    target = "/var/lib/db"
  }

  networks = ["back"]

  healthcheck {
    test     = ["CMD", "/bin/probe", "ready"]
    interval = "5s"
    retries  = 3
  }
}

network "front" {}
network "back" {}

volume "data" {}
```

</td></tr>
</table>

With `TAG=1.2` in the environment, all three load to a project `lab` whose
`web` service runs `localhost:5000/web:1.2` with the command `serve --port
8080 --greeting "hello $USER"` (a literal `$USER`), two replicas, and so on.
`wctr compose config --format yaml|json|hcl` prints that model in any of the
three formats, and what it prints loads back to the same model (tested for
all three, and fuzzed).

## The mapping

An HCL2 body holds **attributes** (`name = expression`) and **blocks**
(`type "label" { body }`). The Compose model is mapped onto them by these
rules, applied in this order:

1. **Top level.** The labelled blocks `service "NAME"`, `network "NAME"`,
   `volume "NAME"`, `secret "NAME"` and `config "NAME"` are the entries of
   the top-level mappings `services`, `networks`, `volumes`, `secrets` and
   `configs`. Repeated unlabelled `include { ... }` blocks are the
   `include` sequence. `variable "NAME"` blocks declare variables (below)
   and are not part of the model. Everything else is an attribute:
   `name = "lab"`, `x-common = { ... }`.
2. **In a service.** `depends_on "SERVICE" { ... }` and `network "NAME"
   { ... }` are the entries of the mappings `depends_on` and `networks`;
   repeated unlabelled `volume { ... }`, `env_file { ... }`, `port { ...
   }`, `secret { ... }` and `config { ... }` blocks append to the sequences
   `volumes`, `env_file`, `ports`, `secrets` and `configs`.
3. **Anywhere.** Any other unlabelled block `key { ... }` is the mapping
   `key` (`deploy`, `healthcheck`, `build`, `logging`...), and may appear
   once; a labelled block `key "L" { ... }` is entry `L` of the mapping
   `key`. A block takes at most one label.
4. **Attributes are values.** An attribute's value converts as JSON
   would: strings, numbers, booleans, `null`, tuples (`[...]`) as
   sequences, objects (`{ ... }`) as mappings. So every Compose attribute
   can be written as an attribute too: `depends_on = ["db"]`, `networks =
   ["back"]`, `volumes = ["data:/var/lib/db"]`, `environment = { A = "1"
   }`, `depends_on = { db = { condition = "service_healthy" } }`. The short
   syntaxes of the specification apply exactly as in YAML.
5. **Not both.** A key set both by an attribute and by blocks
   (`networks = [...]` and `network "x" {}` in one service) is an error.
   So is a labelled block defined twice, or an attribute set twice.

Object keys that are not HCL identifiers are quoted: `labels = {
"org.example.lab" = "true" }`. Attribute names must be identifiers, so an
`x-` extension whose name is not one (`x-!`) has no HCL spelling;
`config --format hcl` says so instead of writing something else.

HCL2 has a single number type: `1.0` and `1` are the same value. The
loader therefore reads a whole number as an integer in every format
(`replicas: 2.0` in YAML is `2`). Infinities and NaN are refused in all
three formats: the Compose model is the one its JSON schema describes.

## Variables and interpolation

YAML and JSON files are interpolated as the specification says
(`12-interpolation.md`): `$VAR`, `${VAR}`, `${VAR:-default}`,
`${VAR-default}`, `${VAR:?error}`, `${VAR?error}`, plus the reference
implementation's `${VAR:+replacement}` and `${VAR+replacement}`; `$$` is a
literal `$`. Values only, never keys; nothing is executed.

**HCL2 files are not passed through that interpolation.** HCL2 has its own,
and running both would make `$` mean two things. Instead:

- Variables are `var.NAME`, in a **static evaluation context**: the only
  name in scope is `var`; there are **no functions** (so no `file()`, no
  `env()`); `for` expressions and `%{for}` directives are refused (they
  multiply sizes, see Security). Operators and conditionals work:
  `replicas = var.HA == "yes" ? 3 : 1`.
- `var.NAME` reads the **same environment** as YAML interpolation: the
  explicit environment (natively the process environment; in a browser the
  page's), over the project's `.env` (or `--env-file`), plus
  `COMPOSE_PROJECT_NAME`.
- `variable "NAME" { default = ... }` gives a default, used when the
  environment has no `NAME`; `description = "..."` is allowed. A default is
  a constant (it may not refer to other variables). Declaring a variable is
  optional: `var.TAG` works without a block when `TAG` is set.
- Using a variable that is neither set nor declared is an error, with the
  file, line and column of the reference: `compose.hcl:3:22: Unsupported
  attribute: This object does not have an attribute named "TAG".` (YAML's
  unset variables read as empty strings with a warning, as the
  specification says; `${TAG:?message}` makes one an error there.)
- Quoted strings are **templates**: `"${var.TAG}"` interpolates. A literal
  `${` is written `$${`, a literal `%{` is `%%{`; a `$` followed by
  anything else is literal (`"hello $USER"`). So the YAML `$$USER` is
  simply `$USER` in HCL2. Heredocs (`<<EOT`) are templates too.

`config --format hcl` writes `$${` and `%%{` where a value contains `${` or
`%{`, so its output reads back to the same strings.

## Diagnostics

Every error names a file, a line and a column, in all three formats:
`compose.yaml:3:12: required variable X is missing a value: X is required`,
`compose.json:4:5: duplicate key "image" (first at compose.json:3:5)`,
`compose.hcl:7:3: "networks" is set both as an attribute and with blocks`.

## Security

A Compose file is untrusted input (a lab hands it to a student; the
browser playground loads it from a page). Each reader is bounded: file size
(4 MiB), nesting depth (64), nodes after YAML alias expansion (262144, the
"billion laughs" bound: aliases are counted at every use), scalar length (1
MiB, also after interpolation), and the bytes all interpolations of one file
produce together (64 MiB). For HCL2, the token stream is checked before the
parser sees it — bracket and template nesting (64) and operators and
postfix index chains (256), since those make the parser and the evaluator
recurse — `for` is refused, and the number of template interpolations times
the longest variable value is bounded (64 MiB). The three parsers, the
interpolator, the env-file parser and the whole loader (with its round trip
through the three formats) are fuzzed in CI.
