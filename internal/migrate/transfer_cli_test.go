package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func destCLI(run func(argv []string) (string, error)) DockerCLI {
	return DockerCLI{run: func(_ context.Context, argv []string) (string, error) { return run(argv) }}
}

func TestTarImageDefaultAndCustom(t *testing.T) {
	if (CLITransfer{}).tarImage() != "alpine:3.24" {
		t.Error("default tar image wrong")
	}
	if (CLITransfer{TarImage: "busybox:1.36"}).tarImage() != "busybox:1.36" {
		t.Error("custom tar image not honored")
	}
}

func TestTransferHasDelegatesToDest(t *testing.T) {
	tr := CLITransfer{Dest: destCLI(func(argv []string) (string, error) {
		if strings.Contains(strings.Join(argv, " "), "inspect") {
			return "ok", nil // present
		}
		return "", nil
	})}
	if ok, err := tr.HasImage(context.Background(), "x"); !ok || err != nil {
		t.Errorf("HasImage delegate = %v, %v", ok, err)
	}
	if ok, err := tr.HasVolume(context.Background(), "v"); !ok || err != nil {
		t.Errorf("HasVolume delegate = %v, %v", ok, err)
	}
}

func TestEnsureTarImagePresentSkipsPull(t *testing.T) {
	pulled := false
	tr := CLITransfer{Dest: destCLI(func(argv []string) (string, error) {
		j := strings.Join(argv, " ")
		if strings.Contains(j, "pull") {
			pulled = true
		}
		return "ok", nil // inspect succeeds -> image present
	})}
	if err := tr.EnsureTarImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pulled {
		t.Error("should not pull when the image is already present")
	}
}

func TestEnsureTarImageAbsentPulls(t *testing.T) {
	pulled := false
	tr := CLITransfer{Dest: destCLI(func(argv []string) (string, error) {
		j := strings.Join(argv, " ")
		switch {
		case strings.Contains(j, "inspect"):
			return "", errors.New("No such image: alpine:3.24") // absent
		case strings.Contains(j, "pull"):
			pulled = true
			return "pulled", nil
		}
		return "", nil
	})}
	if err := tr.EnsureTarImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !pulled {
		t.Error("should pull when the image is absent")
	}
}

func TestDockerExe(t *testing.T) {
	if (DockerCLI{}).exe() != "docker" {
		t.Error("default exe should be docker")
	}
	if (DockerCLI{Exe: "podman"}).exe() != "podman" {
		t.Error("explicit exe not honored")
	}
}

// The Move* paths stream between two real docker processes; without docker we
// can still exercise their setup and the pipeline's start-failure branch by
// pointing the binaries at something that cannot start.
const noSuchBin = "skrog-no-such-docker-bin"

func TestMoveImagesReportsStartFailure(t *testing.T) {
	tr := CLITransfer{Src: DockerCLI{Exe: noSuchBin}, Dest: DockerCLI{Exe: noSuchBin}}
	if err := tr.MoveImages(context.Background(), []string{"nginx:latest"}); err == nil {
		t.Fatal("expected a start failure when the docker binary is missing")
	}
}

func TestMoveVolumeReportsStartFailure(t *testing.T) {
	// Dest.output (volume create, helper create, rm) is faked; only the streamed
	// cp/tar subprocesses fail to start, which is what we want to exercise.
	dest := DockerCLI{Exe: noSuchBin, run: func(_ context.Context, argv []string) (string, error) {
		if strings.Contains(strings.Join(argv, " "), "create -v") {
			return "cid123\n", nil // helper container id
		}
		return "", nil // volume create, rm -f
	}}
	tr := CLITransfer{Src: DockerCLI{Exe: noSuchBin}, Dest: dest}
	if err := tr.MoveVolume(context.Background(), "data"); err == nil {
		t.Fatal("expected a start failure streaming the volume")
	}
}

func TestMoveVolumeReportsCreateFailure(t *testing.T) {
	dest := DockerCLI{run: func(_ context.Context, argv []string) (string, error) {
		return "", errors.New("volume create denied")
	}}
	tr := CLITransfer{Dest: dest}
	if err := tr.MoveVolume(context.Background(), "data"); err == nil {
		t.Fatal("a failed destination volume-create should error")
	}
}

func TestEnsureTarImagePullError(t *testing.T) {
	tr := CLITransfer{Dest: destCLI(func(argv []string) (string, error) {
		j := strings.Join(argv, " ")
		if strings.Contains(j, "pull") {
			return "", errors.New("network unreachable")
		}
		return "", errors.New("No such image") // absent -> triggers pull
	})}
	if err := tr.EnsureTarImage(context.Background()); err == nil {
		t.Fatal("a failed pull should error")
	}
}
