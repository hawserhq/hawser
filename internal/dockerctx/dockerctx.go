// Package dockerctx manages the `hawser` docker context.
//
// Contexts are created through the docker CLI rather than by writing
// ~/.docker/contexts metadata directly. That on-disk layout is an
// implementation detail Docker has changed before, and hand-writing it would
// make Hawser the thing that breaks when it changes again. Shelling out costs a
// process and buys forward compatibility.
package dockerctx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Name is the context Hawser creates.
const Name = "hawser"

// Runner executes the docker CLI. Injectable so tests need no docker binary.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Manager creates, selects and removes the Hawser context.
type Manager struct {
	// Docker is the docker executable to drive. Empty resolves through PATH.
	Docker string
	// Runner executes commands. Empty uses the real process runner.
	Runner Runner
}

// ErrNoDockerCLI reports that no docker executable could be found or run.
//
// A distinct type because it is not really a failure: the engine is installed
// and reachable over DOCKER_HOST regardless, so install should report this and
// carry on rather than roll back a working engine over a missing convenience.
type ErrNoDockerCLI struct{ Err error }

func (e *ErrNoDockerCLI) Error() string {
	return fmt.Sprintf("no working docker CLI found (%v); "+
		"the engine is still reachable by setting DOCKER_HOST", e.Err)
}

func (e *ErrNoDockerCLI) Unwrap() error { return e.Err }

func (m *Manager) docker() string {
	if m.Docker != "" {
		return m.Docker
	}
	return "docker"
}

func (m *Manager) run(ctx context.Context, args ...string) (string, error) {
	var out []byte
	var err error
	if m.Runner != nil {
		out, err = m.Runner.Run(ctx, m.docker(), args...)
	} else {
		out, err = exec.CommandContext(ctx, m.docker(), args...).CombinedOutput()
	}
	text := strings.TrimSpace(string(out))
	if err != nil {
		// The CLI's own message is the diagnosis; the exit code is not.
		if text != "" {
			return text, fmt.Errorf("docker %s: %w: %s",
				strings.Join(args, " "), err, firstLine(text))
		}
		return text, fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Available reports whether a usable docker CLI is present.
func (m *Manager) Available(ctx context.Context) error {
	if _, err := m.run(ctx, "--version"); err != nil {
		return &ErrNoDockerCLI{Err: err}
	}
	return nil
}

// Exists reports whether the Hawser context is already defined.
func (m *Manager) Exists(ctx context.Context) (bool, error) {
	out, err := m.run(ctx, "context", "ls", "--format", "{{.Name}}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == Name {
			return true, nil
		}
	}
	return false, nil
}

// Endpoint returns the docker host the Hawser context points at.
func (m *Manager) Endpoint(ctx context.Context) (string, error) {
	out, err := m.run(ctx, "context", "inspect", Name,
		"--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Ensure creates the Hawser context, or updates it when the endpoint has moved.
//
// The endpoint does move: Hawser serves the default pipe when it is free and its
// own pipe when Docker Desktop holds it, so installing Desktop later changes
// which pipe is correct. Updating rather than recreating keeps the user's
// selection intact.
func (m *Manager) Ensure(ctx context.Context, dockerHost string) error {
	if dockerHost == "" {
		return errors.New("dockerctx: dockerHost is required")
	}
	if err := m.Available(ctx); err != nil {
		return err
	}

	exists, err := m.Exists(ctx)
	if err != nil {
		return err
	}

	if !exists {
		_, err := m.run(ctx, "context", "create", Name,
			"--description", "Hawser engine (WSL2)",
			"--docker", "host="+dockerHost)
		return err
	}

	current, err := m.Endpoint(ctx)
	if err != nil {
		return err
	}
	if current == dockerHost {
		return nil
	}
	_, err = m.run(ctx, "context", "update", Name, "--docker", "host="+dockerHost)
	return err
}

// Use selects the Hawser context as the default.
func (m *Manager) Use(ctx context.Context) error {
	_, err := m.run(ctx, "context", "use", Name)
	return err
}

// Current returns the active context name.
func (m *Manager) Current(ctx context.Context) (string, error) {
	out, err := m.run(ctx, "context", "show")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Remove deletes the Hawser context, restoring a previous selection first.
//
// Switching away is required rather than tidy: docker refuses to remove the
// context in use, and leaving the user pointed at a context that no longer
// exists would break every subsequent docker command — the opposite of
// "nothing else on the system was modified".
func (m *Manager) Remove(ctx context.Context, restoreTo string) error {
	if err := m.Available(ctx); err != nil {
		return err
	}
	exists, err := m.Exists(ctx)
	if err != nil || !exists {
		return err
	}

	if current, err := m.Current(ctx); err == nil && current == Name {
		if restoreTo == "" {
			restoreTo = "default"
		}
		if _, err := m.run(ctx, "context", "use", restoreTo); err != nil {
			return fmt.Errorf("restoring context %q: %w", restoreTo, err)
		}
	}

	_, err = m.run(ctx, "context", "rm", Name)
	return err
}

// The remote-engine contexts (#138): the same operations for an arbitrary
// context name, so `hawser remote` can back hawser-<name> with TLS material.

// ExistsNamed reports whether a context is defined.
func (m *Manager) ExistsNamed(ctx context.Context, name string) (bool, error) {
	out, err := m.run(ctx, "context", "ls", "--format", "{{.Name}}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// CreateTLS creates a context for a mutual-TLS endpoint, or updates it when it
// already exists. The --docker key=value form carries the certificate paths;
// docker copies the files into its own metadata store, so the sources may move
// afterwards without breaking the context.
func (m *Manager) CreateTLS(ctx context.Context, name, description, host, caPath, certPath, keyPath string) error {
	if err := m.Available(ctx); err != nil {
		return err
	}
	spec := fmt.Sprintf("host=%s,ca=%s,cert=%s,key=%s", host, caPath, certPath, keyPath)
	exists, err := m.ExistsNamed(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		_, err = m.run(ctx, "context", "update", name, "--docker", spec)
		return err
	}
	_, err = m.run(ctx, "context", "create", name, "--description", description, "--docker", spec)
	return err
}

// UseNamed selects a context as the default.
func (m *Manager) UseNamed(ctx context.Context, name string) error {
	_, err := m.run(ctx, "context", "use", name)
	return err
}

// RemoveNamed deletes a context, switching to restoreTo first when it is the
// current one (docker refuses to remove the context in use). A context that
// does not exist is success: the caller asked for a state.
func (m *Manager) RemoveNamed(ctx context.Context, name, restoreTo string) error {
	if err := m.Available(ctx); err != nil {
		return err
	}
	exists, err := m.ExistsNamed(ctx, name)
	if err != nil || !exists {
		return err
	}
	if current, err := m.Current(ctx); err == nil && current == name {
		if restoreTo == "" {
			restoreTo = "default"
		}
		if _, err := m.run(ctx, "context", "use", restoreTo); err != nil {
			return fmt.Errorf("restoring context %q: %w", restoreTo, err)
		}
	}
	_, err = m.run(ctx, "context", "rm", name)
	return err
}
