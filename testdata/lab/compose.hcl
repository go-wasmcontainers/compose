# The lab application of docs/HCL.md, in HCL2.
name = "lab"

x-common = {
  labels = { "org.example.lab" = "true" }
}

# var.REGISTRY comes from the environment, else this default.
variable "REGISTRY" {
  default = "localhost:5000"
}

service "web" {
  image = "${var.REGISTRY}/web:${var.TAG}"
  # Not a template: HCL only interpolates "${", so $USER stays literal.
  command = ["serve", "--port", "8080", "--greeting", "hello $USER"]
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
