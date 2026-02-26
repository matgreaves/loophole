# CLAUDE.md

## What this is

Loophole is a macOS daemon that gives Docker containers stable loopback IPs
(127.0.0.x) and DNS names under `*.loop.test`. It proxies TCP connections from
allocated IPs to Docker's mapped host ports, with protocol-level Kafka metadata
rewriting.

## Build and test

```
go build ./...
go test ./...
```

## Running

```
sudo loophole resolve-install   # one-time DNS setup
sudo loophole serve             # start daemon (needs root for loopback aliases)
```

## Debugging connections

Check what's proxied:

```
loophole ls
```

State is persisted to `~/.loophole/state.json` — you can inspect it directly.

DNS resolution:

```
dig @127.0.0.1 -p 15353 mycontainer.loop.test
```

The daemon logs to stderr with slog. Key log lines to look for:

- `container started` — Docker event received, ports detected
- `container proxied` — IP allocated, proxies running, DNS registered
- `proxy started` — Individual port proxy listening
- `kafka protocol detected` — Kafka sniffing triggered on a connection
- `proxy dial failed` — Can't reach Docker's mapped host port
- `loopback alias failed` — IP alias failed (permissions?)
- `proxy bind failed` — Port already in use on the allocated IP

### Docker socket

If the daemon can't connect to Docker, check `DOCKER_HOST` or verify one of
these sockets exists:

1. `/var/run/docker.sock`
2. `~/.docker/run/docker.sock`
3. `~/.colima/default/docker.sock`
4. `~/.orbstack/run/docker.sock`

### Kafka

Kafka detection is automatic — no per-port configuration. The proxy sniffs the
first 8 bytes of each TCP connection. When Kafka wire protocol is detected, it
tracks request correlation IDs and rewrites Metadata (API key 3) responses so
broker advertised addresses point back through loophole's proxy IP.

Verify with: `kcat -b <name>.loop.test:<port> -L`

If broker addresses show the container's internal IP instead of loophole's
127.0.0.x, the rewrite isn't being applied — check for the `kafka protocol
detected` log line.
