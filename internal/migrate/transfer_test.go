package migrate

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a real test: it is the child body for the pipeline
// tests, selected by GO_MIGRATE_HELPER (the classic exec-test pattern).
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("GO_MIGRATE_HELPER") {
	case "":
		return // running as part of the normal suite
	case "producer-bulk":
		// Writes far more than any pipe buffer, so a consumer death must
		// surface as a write error here — not block forever.
		buf := make([]byte, 64*1024)
		for i := 0; i < 256; i++ { // 16 MiB
			if _, err := os.Stdout.Write(buf); err != nil {
				os.Stderr.WriteString("producer: " + err.Error() + "\n")
				os.Exit(2)
			}
		}
		os.Exit(0)
	case "producer-small":
		os.Stdout.WriteString("payload")
		os.Exit(0)
	case "consumer-dies":
		io.ReadFull(os.Stdin, make([]byte, 10))
		os.Stderr.WriteString("consumer boom: no space left\n")
		os.Exit(3)
	case "consumer-drain":
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(0)
}

func helperCmd(role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "GO_MIGRATE_HELPER="+role)
	return cmd
}

func runPipeline(t *testing.T, prodRole, consRole string) error {
	t.Helper()
	producer := helperCmd(prodRole)
	consumer := helperCmd(consRole)
	pipe, err := producer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	consumer.Stdin = pipe
	var pe, ce strings.Builder
	producer.Stderr = &pe
	consumer.Stderr = &ce

	done := make(chan error, 1)
	go func() { done <- pipeline(producer, consumer, pipe, &pe, &ce, "save", "load") }()
	select {
	case err := <-done:
		return err
	case <-time.After(60 * time.Second):
		t.Fatal("pipeline deadlocked (#86: consumer death must unblock the producer)")
		return nil
	}
}

func TestPipelineConsumerDeathUnblocksProducer(t *testing.T) {
	// #86: the consumer dies after 10 bytes while the producer still has
	// ~16 MiB to write. v0.2.0 deadlocked here (the parent's open pipe
	// read-end starved the producer of its broken-pipe signal).
	err := runPipeline(t, "producer-bulk", "consumer-dies")
	if err == nil {
		t.Fatal("pipeline reported success despite a dead consumer")
	}
	// The consumer's stderr is the root cause and must lead the report.
	if !strings.Contains(err.Error(), "consumer boom") {
		t.Errorf("consumer's error not surfaced as root cause: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "load failed") {
		t.Errorf("report should lead with the consumer: %v", err)
	}
}

func TestPipelineHappyPath(t *testing.T) {
	if err := runPipeline(t, "producer-small", "consumer-drain"); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
}
