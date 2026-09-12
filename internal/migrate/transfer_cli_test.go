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

// A volume this call created and then failed to fill must not survive.
//
// Resume skips any volume the destination already has (Plan -> HasVolume, which
// is `docker volume inspect`: existence, not content). So a created-but-unfilled
// volume is not a retryable half-state — it is permanent. The next run reports
// "already on the Skrog engine, will skip" over an empty or truncated tar and
// tells the user the migration completed, which is silent data loss on the
// resume path the help text recommends (#236).
func TestMoveVolumeRemovesAVolumeItCreatedAndCouldNotFill(t *testing.T) {
	var argvs []string
	tr := CLITransfer{Dest: destCLI(func(argv []string) (string, error) {
		joined := strings.Join(argv, " ")
		argvs = append(argvs, joined)
		switch {
		case strings.Contains(joined, "volume inspect"):
			return "", errors.New("Error: No such volume: v") // not there yet
		case strings.Contains(joined, "volume create"):
			return "v", nil
		case strings.HasPrefix(joined, "create "):
			// The helper container is where this test forces the failure:
			// everything before it has already created the volume.
			return "", errors.New("no space left on device")
		}
		return "", nil
	})}

	err := tr.MoveVolume(context.Background(), "v")
	if err == nil {
		t.Fatal("MoveVolume succeeded despite the helper container failing")
	}
	var removed bool
	for _, a := range argvs {
		if strings.Contains(a, "volume rm") && strings.Contains(a, "v") {
			removed = true
		}
	}
	if !removed {
		t.Errorf("the volume was created and left behind; a resume will skip it as migrated.\ncalls: %v", argvs)
	}
}

// The mirror image, and the more dangerous direction: a volume that was
// already on the destination holds someone else's data. Deleting it to tidy up
// our own failure would be a far worse bug than the one above.
func TestMoveVolumeNeverRemovesAVolumeItDidNotCreate(t *testing.T) {
	var argvs []string
	tr := CLITransfer{Dest: destCLI(func(argv []string) (string, error) {
		joined := strings.Join(argv, " ")
		argvs = append(argvs, joined)
		switch {
		case strings.Contains(joined, "volume inspect"):
			return "already here", nil // pre-existing
		case strings.HasPrefix(joined, "create "):
			return "", errors.New("no space left on device")
		}
		return "", nil
	})}

	if err := tr.MoveVolume(context.Background(), "v"); err == nil {
		t.Fatal("MoveVolume succeeded despite the helper container failing")
	}
	for _, a := range argvs {
		if strings.Contains(a, "volume rm") {
			t.Errorf("a pre-existing destination volume was removed: %q\ncalls: %v", a, argvs)
		}
	}
}
