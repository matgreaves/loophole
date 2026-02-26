package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// PortBinding represents a published container port.
type PortBinding struct {
	ContainerPort uint16
	HostPort      uint16
}

// ContainerInfo holds the extracted metadata for a container.
type ContainerInfo struct {
	ID             string
	Name           string
	ComposeProject string
	ComposeService string
	Ports          []PortBinding
}

// Event represents a container lifecycle event.
type Event struct {
	Type string // "start" or "stop"
	Info ContainerInfo
}

// Watcher watches Docker for container events.
type Watcher struct {
	cli *client.Client
}

func NewWatcher() (*Watcher, error) {
	var opts []client.Opt

	// If DOCKER_HOST isn't set, probe common socket paths.
	if os.Getenv("DOCKER_HOST") == "" {
		if sock := findSocket(); sock != "" {
			opts = append(opts, client.WithHost("unix://"+sock))
		}
	}
	opts = append(opts, client.FromEnv) // let env override

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	ctx := context.Background()
	if _, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return nil, fmt.Errorf("docker not running: %w", err)
	}

	return &Watcher{cli: cli}, nil
}

// findSocket checks common Docker socket locations and returns the first one that exists.
func findSocket() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/var/run/docker.sock",
		filepath.Join(home, ".docker/run/docker.sock"),
		filepath.Join(home, ".colima/default/docker.sock"),
		filepath.Join(home, ".orbstack/run/docker.sock"),
	}
	for _, s := range candidates {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return ""
}

// Watch scans existing containers then streams events.
// It sends events on the returned channel. The channel is closed when ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) <-chan Event {
	ch := make(chan Event, 64)

	go func() {
		defer close(ch)

		// Scan existing running containers
		result, err := w.cli.ContainerList(ctx, client.ContainerListOptions{})
		if err != nil {
			slog.Error("failed to list containers", "err", err)
			return
		}

		for _, c := range result.Items {
			info, err := w.inspect(ctx, c.ID)
			if err != nil {
				slog.Error("inspect failed", "container", c.ID[:12], "err", err)
				continue
			}
			if len(info.Ports) > 0 {
				ch <- Event{Type: "start", Info: info}
			}
		}

		// Stream events
		f := make(client.Filters)
		f.Add("type", "container")
		f.Add("event", "start")
		f.Add("event", "die")

		evResult := w.cli.Events(ctx, client.EventsListOptions{Filters: f})

		for {
			select {
			case <-ctx.Done():
				return
			case err := <-evResult.Err:
				if err != nil {
					slog.Error("docker events error", "err", err)
				}
				return
			case ev := <-evResult.Messages:
				switch ev.Action {
				case events.ActionStart:
					info, err := w.inspect(ctx, ev.Actor.ID)
					if err != nil {
						slog.Error("inspect failed", "container", ev.Actor.ID[:12], "err", err)
						continue
					}
					ch <- Event{Type: "start", Info: info}
				case events.ActionDie:
					ch <- Event{
						Type: "stop",
						Info: ContainerInfo{ID: ev.Actor.ID},
					}
				}
			}
		}
	}()

	return ch
}

func (w *Watcher) inspect(ctx context.Context, containerID string) (ContainerInfo, error) {
	result, err := w.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, err
	}

	cj := result.Container
	info := ContainerInfo{
		ID:             cj.ID,
		Name:           strings.TrimPrefix(cj.Name, "/"),
		ComposeProject: cj.Config.Labels["com.docker.compose.project"],
		ComposeService: cj.Config.Labels["com.docker.compose.service"],
	}

	seen := make(map[uint16]bool)
	for port, bindings := range cj.NetworkSettings.Ports {
		containerPort := port.Num()
		if seen[containerPort] {
			continue
		}
		for _, b := range bindings {
			if b.HostPort == "" {
				continue
			}
			hp, err := strconv.ParseUint(b.HostPort, 10, 16)
			if err != nil {
				continue
			}
			seen[containerPort] = true
			info.Ports = append(info.Ports, PortBinding{
				ContainerPort: containerPort,
				HostPort:      uint16(hp),
			})
			break
		}
	}

	return info, nil
}

// Close closes the Docker client.
func (w *Watcher) Close() error {
	return w.cli.Close()
}
