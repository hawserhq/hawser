package migrate

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// CLITransfer streams between a source and destination engine, each driven by
// its own docker CLI. The destination also answers the Has* queries, so it
// doubles as the destination Engine for skip-existing.
type CLITransfer struct {
	Src  DockerCLI
	Dest DockerCLI

	// TarImage is the image used to tar/untar volume contents. It must exist
	// on both engines; alpine is tiny and the migrate command pulls it on the
	// destination first. Default "alpine:3.24".
	TarImage string
}

func (t CLITransfer) tarImage() string {
	if t.TarImage != "" {
		return t.TarImage
	}
	return "alpine:3.24"
}

// HasImage / HasVolume delegate to the destination.
func (t CLITransfer) HasImage(ctx context.Context, ref string) (bool, error) {
	return t.Dest.HasImage(ctx, ref)
}
func (t CLITransfer) HasVolume(ctx context.Context, name string) (bool, error) {
	return t.Dest.HasVolume(ctx, name)
}

// MoveImages runs `docker save ref... | docker load`, streaming the tar
// between the two engines without staging a file on disk (a multi-GB save
// would otherwise need multi-GB of free space). Read-only against the source.
func (t CLITransfer) MoveImages(ctx context.Context, refs []string) error {
	save := exec.CommandContext(ctx, t.Src.exe(), t.Src.args(append([]string{"save"}, refs...)...)...)
	load := exec.CommandContext(ctx, t.Dest.exe(), t.Dest.args("load")...)

	pipe, err := save.StdoutPipe()
	if err != nil {
		return err
	}
	load.Stdin = pipe

	var saveErr, loadErr strings.Builder
	save.Stderr = &saveErr
	load.Stderr = &loadErr

	return pipeline(save, load, pipe, &saveErr, &loadErr, "save", "load")
}

// MoveVolume creates the volume on the destination, then streams its contents
// as a tar. The source side reads through a throwaway container (tar -c); the
// destination side receives via `docker cp -`, which is a plain HTTP PUT of a
// tar archive — deliberately NOT `docker run -i`. A hijacked interactive
// stream needs the client to half-close stdin to signal EOF, which the
// Windows named pipe the CLI speaks cannot do (byte-mode pipes have no
// half-close), so a `run -i` receiver hangs forever waiting for a tar that
// never ends. `docker cp` frames the body as ordinary chunked HTTP, which the
// bridge carries cleanly. The source volume is only ever read, never written.
func (t CLITransfer) MoveVolume(ctx context.Context, name string) error {
	if _, err := t.Dest.output(ctx, "volume", "create", name); err != nil {
		return fmt.Errorf("creating destination volume: %w", err)
	}
	// A stopped helper container gives docker cp a filesystem path (the mounted
	// volume) to extract into. It never runs; removed in all exit paths.
	cid, err := t.Dest.output(ctx, "create", "-v", name+":/to", t.tarImage(), "true")
	if err != nil {
		return fmt.Errorf("creating destination helper container: %w", err)
	}
	cid = strings.TrimSpace(cid)
	defer t.Dest.output(context.Background(), "rm", "-f", cid)

	// Source: read the volume, write a tar to stdout. Dot-relative so the
	// archive has no leading path and extracts cleanly. No -i: the container's
	// stdout is a normal attach, and EOF on it (container exit) the bridge
	// already delivers correctly.
	src := exec.CommandContext(ctx, t.Src.exe(), t.Src.args(
		"run", "--rm",
		"-v", name+":/from:ro",
		t.tarImage(), "tar", "-C", "/from", "-cf", "-", ".")...)
	// Destination: PUT the tar into the helper container's /to (the volume).
	dst := exec.CommandContext(ctx, t.Dest.exe(), t.Dest.args("cp", "-", cid+":/to")...)

	pipe, err := src.StdoutPipe()
	if err != nil {
		return err
	}
	dst.Stdin = pipe

	var srcErr, dstErr strings.Builder
	src.Stderr = &srcErr
	dst.Stderr = &dstErr

	return pipeline(src, dst, pipe, &srcErr, &dstErr, "read volume", "write volume")
}

// pipeline runs producer | consumer, waiting for both and reporting the first
// side that failed with its stderr.
//
// Both processes are waited concurrently (#86). The v0.2.0 shape — wait for
// the producer, then close the pipe — deadlocked when the CONSUMER died
// mid-stream: the parent's open read-end of the pipe meant the producer never
// saw a broken pipe, blocked writing once the buffer filled, and its Wait
// never returned. Whichever side exits first now triggers a close of the
// parent's pipe end, which unblocks the other side (EPIPE for a writing
// producer, EOF for a reading consumer). When both fail, the consumer's error
// is reported as the root cause — the producer's is usually just the
// resulting broken pipe.
func pipeline(producer, consumer *exec.Cmd, pipe io.ReadCloser, prodErr, consErr *strings.Builder, prodName, consName string) error {
	if err := consumer.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", consName, err)
	}
	if err := producer.Start(); err != nil {
		consumer.Process.Kill()
		consumer.Wait()
		return fmt.Errorf("starting %s: %w", prodName, err)
	}

	prodDone := make(chan error, 1)
	consDone := make(chan error, 1)
	go func() { prodDone <- producer.Wait() }()
	go func() { consDone <- consumer.Wait() }()

	var pErr, cErr error
	select {
	case pErr = <-prodDone:
		pipe.Close() // producer finished: give the consumer its EOF
		cErr = <-consDone
	case cErr = <-consDone:
		pipe.Close() // consumer died: unblock the producer with a broken pipe
		pErr = <-prodDone
	}

	trim := func(b *strings.Builder) string { return strings.TrimSpace(b.String()) }
	switch {
	case cErr != nil && pErr != nil:
		return fmt.Errorf("%s failed: %v: %s (the %s then failed too: %v: %s)",
			consName, cErr, trim(consErr), prodName, pErr, trim(prodErr))
	case cErr != nil:
		return fmt.Errorf("%s failed: %v: %s", consName, cErr, trim(consErr))
	case pErr != nil:
		return fmt.Errorf("%s failed: %v: %s", prodName, pErr, trim(prodErr))
	}
	return nil
}

// EnsureTarImage makes sure the tar helper image exists on the destination,
// pulling it if absent. Called once before volume moves so each volume's
// throwaway container starts instantly.
func (t CLITransfer) EnsureTarImage(ctx context.Context) error {
	if has, _ := t.Dest.HasImage(ctx, t.tarImage()); has {
		return nil
	}
	if _, err := t.Dest.output(ctx, "pull", t.tarImage()); err != nil {
		return fmt.Errorf("pulling %s on the destination: %w", t.tarImage(), err)
	}
	return nil
}
