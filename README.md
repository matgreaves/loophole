# Loophole

Stable loopback IPs and DNS names for Docker containers on macOS.

Running multiple projects locally means port collisions — two Postgres containers
can't both bind to `localhost:5432`. Docker solves this with random host ports,
but now you're juggling `localhost:49312` and checking `docker ps` every time.

Every problem has a loophole: this one watches for running containers and gives
each a dedicated loopback IP (127.0.0.x) with DNS under `*.loop.test`. Connect
to `postgres.myapp.loop.test:5432` — the original port, every time, no
collisions.

## Install

```
go install github.com/matgreaves/loophole@latest
```

## Setup

Install the macOS DNS resolver (one-time, requires sudo):

```
sudo loophole resolve-install
```

This creates `/etc/resolver/loop.test` pointing at loophole's built-in DNS
server.

## Usage

Start the daemon (requires sudo for loopback aliases):

```
sudo loophole serve
```

That's it. Start any Docker container with published ports and loophole picks it
up automatically:

```
$ docker run -d --name pg -p 5432 postgres
$ loophole ls
DNS NAME            IP         PORTS       CONTAINER
pg.loop.test        127.0.0.2  5432→49312  pg
```

Connect using the DNS name on the original port:

```
psql -h pg.loop.test -p 5432
```

### Docker Compose

Compose containers get names based on their service and project:

```
$ docker compose -p myapp up -d
$ loophole ls
DNS NAME                 IP         PORTS       CONTAINER
postgres.myapp.loop.test 127.0.0.2  5432→49312  myapp-postgres-1
redis.myapp.loop.test    127.0.0.3  6379→49313  myapp-redis-1
```

### Kafka

Loophole detects Kafka wire protocol on any port and rewrites metadata responses
so broker advertised addresses point back through the proxy. No configuration
needed — Kafka clients see loophole's IP instead of the container's internal
address:

```
$ docker run -d --name kafka -p 9092 apache/kafka
$ kcat -b kafka.loop.test:9092 -L
Metadata for all topics (from broker 1: 127.0.0.2:9092/1):
 1 brokers:
  broker 1 at 127.0.0.2:9092 (controller)
```

## Commands

```
loophole serve              Start the daemon
loophole ls                 List proxied containers
loophole resolve-install    Install macOS DNS resolver (requires sudo)
loophole resolve-uninstall  Remove macOS DNS resolver (requires sudo)
```

## How it works

1. Watches Docker for container start/stop events
2. Allocates a loopback IP (127.0.0.2, .3, .4, ...) and creates a macOS
   loopback alias (`ifconfig lo0 alias`)
3. Starts TCP proxies on the allocated IP, listening on the container's original
   ports and forwarding to Docker's mapped host ports
4. Registers the DNS name in a built-in DNS server on 127.0.0.1:15353
5. On each new TCP connection, sniffs the first 8 bytes to detect Kafka wire
   protocol — Kafka connections get metadata response rewriting, everything
   else gets plain bidirectional forwarding

## Docker socket detection

Loophole looks for the Docker socket in this order:

1. `DOCKER_HOST` environment variable
2. `/var/run/docker.sock`
3. `~/.docker/run/docker.sock`
4. `~/.colima/default/docker.sock`
5. `~/.orbstack/run/docker.sock`

## Requirements

- macOS (uses `ifconfig lo0 alias` and `/etc/resolver/`)
- Docker
- Go 1.25+
