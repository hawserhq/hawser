//go:build e2e

// Testcontainers against the Hawser engine (#144): the library talks to
// DOCKER_HOST (the suite points it at Hawser's pipe), starts a container with a
// published port, waits for it over that port from the host, and lets the Ryuk
// reaper — a container that mounts the engine's own docker socket — clean up.
// Every one of those is a place a Windows-pipe-fronted engine could differ from
// Docker Desktop, and none of them does.
package testcontainers_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestGenericContainerPortMappingAndReaper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req := tc.ContainerRequest{
		Image:        "nginx:alpine",
		ExposedPorts: []string{"80/tcp"},
		// The wait strategy itself exercises port mapping: it polls the host-side
		// mapped port until nginx answers.
		WaitingFor: wait.ForHTTP("/").WithPort("80/tcp").WithStartupTimeout(3 * time.Minute),
	}
	c, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Fatalf("GenericContainer: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			t.Logf("terminate: %v", err)
		}
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://%s:%s/", host, port.Port())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	t.Logf("nginx answered on %s through the mapped port", url)
}
