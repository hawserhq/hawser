package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// cli builds a DockerCLI whose command execution is faked by run.
func cli(run func(argv []string) (string, error)) DockerCLI {
	return DockerCLI{run: func(_ context.Context, argv []string) (string, error) { return run(argv) }}
}

func TestArgsPrefix(t *testing.T) {
	if got := (DockerCLI{Host: "npipe://x"}).args("images"); strings.Join(got, " ") != "-H npipe://x images" {
		t.Errorf("Host args = %v", got)
	}
	if got := (DockerCLI{Context: "desktop-linux"}).args("ps"); strings.Join(got, " ") != "--context desktop-linux ps" {
		t.Errorf("Context args = %v", got)
	}
	if got := (DockerCLI{}).args("version"); strings.Join(got, " ") != "version" {
		t.Errorf("bare args = %v", got)
	}
	// Host wins over Context when both are set.
	if got := (DockerCLI{Host: "h", Context: "c"}).args("x"); got[0] != "-H" {
		t.Errorf("Host should take precedence: %v", got)
	}
}

func TestImagesParses(t *testing.T) {
	out := strings.Join([]string{
		`{"Repository":"nginx","Tag":"latest","ID":"sha256:aaa","Size":"142MB"}`,
		`{"Repository":"<none>","Tag":"<none>","ID":"sha256:bbb","Size":"5MB"}`,
		``, // trailing blank line must be skipped
	}, "\n")
	d := cli(func(argv []string) (string, error) {
		if !strings.Contains(strings.Join(argv, " "), "images") {
			t.Fatalf("unexpected argv %v", argv)
		}
		return out, nil
	})
	imgs, err := d.Images(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d images, want 2", len(imgs))
	}
	if imgs[0].Ref != "nginx:latest" || imgs[0].ID != "sha256:aaa" || imgs[0].Size != 142*(1<<20) {
		t.Errorf("image 0 = %+v", imgs[0])
	}
	if imgs[1].Ref != "" { // dangling
		t.Errorf("dangling image should have empty ref, got %q", imgs[1].Ref)
	}
}

func TestImagesPropagatesError(t *testing.T) {
	d := cli(func(argv []string) (string, error) { return "", errors.New("engine down") })
	if _, err := d.Images(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestImagesRejectsBadJSON(t *testing.T) {
	d := cli(func(argv []string) (string, error) { return "not json", nil })
	if _, err := d.Images(context.Background()); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestVolumesWithSizes(t *testing.T) {
	d := cli(func(argv []string) (string, error) {
		j := strings.Join(argv, " ")
		switch {
		case strings.Contains(j, "volume ls"):
			return "data\ncache\nlonely\n", nil
		case strings.Contains(j, "system df"):
			return `{"Volumes":[{"Name":"data","Size":"10MB"},{"Name":"cache","Size":"2GB"}]}`, nil
		}
		t.Fatalf("unexpected argv %v", argv)
		return "", nil
	})
	vols, err := d.Volumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int64{}
	for _, v := range vols {
		byName[v.Name] = v.Size
	}
	if byName["data"] != 10*(1<<20) || byName["cache"] != 2*(1<<30) {
		t.Errorf("sizes wrong: %+v", byName)
	}
	if byName["lonely"] != -1 { // no df entry -> unknown
		t.Errorf("missing-size volume should be -1, got %d", byName["lonely"])
	}
}

func TestVolumesWhenDfFails(t *testing.T) {
	d := cli(func(argv []string) (string, error) {
		j := strings.Join(argv, " ")
		if strings.Contains(j, "volume ls") {
			return "data\n", nil
		}
		return "", errors.New("df unsupported") // volumeSizes best-effort -> nil
	})
	vols, err := d.Volumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 || vols[0].Size != -1 {
		t.Fatalf("volumes = %+v", vols)
	}
}

func TestHasImage(t *testing.T) {
	cases := []struct {
		name    string
		ret     string
		err     error
		want    bool
		wantErr bool
	}{
		{"present", "ok", nil, true, false},
		{"absent", "", errors.New("docker image inspect x: No such image: x"), false, false},
		{"unreachable", "", errors.New("Cannot connect to the Docker daemon"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := cli(func(argv []string) (string, error) { return tc.ret, tc.err })
			got, err := d.HasImage(context.Background(), "x")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("HasImage = %v, %v; want %v, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestHasVolume(t *testing.T) {
	absent := cli(func(argv []string) (string, error) { return "", errors.New("Error: no such volume: v") })
	if ok, err := absent.HasVolume(context.Background(), "v"); ok || err != nil {
		t.Errorf("absent volume = %v, %v; want false, nil", ok, err)
	}
	present := cli(func(argv []string) (string, error) { return "[{}]", nil })
	if ok, err := present.HasVolume(context.Background(), "v"); !ok || err != nil {
		t.Errorf("present volume = %v, %v; want true, nil", ok, err)
	}
	broken := cli(func(argv []string) (string, error) { return "", errors.New("daemon unreachable") })
	if _, err := broken.HasVolume(context.Background(), "v"); err == nil {
		t.Error("unreachable should error")
	}
}

func TestParseHumanSizeExtra(t *testing.T) {
	cases := map[string]int64{
		"":        -1,
		"N/A":     -1,
		"0B":      0,
		"512B":    512,
		"1.5KB":   int64(1.5 * (1 << 10)),
		"3TB":     3 * (1 << 40),
		"garbage": -1,
	}
	for in, want := range cases {
		if got := parseHumanSize(in); got != want {
			t.Errorf("parseHumanSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPlanEmpty(t *testing.T) {
	if !(Plan{}).Empty() {
		t.Error("zero Plan should be Empty")
	}
	if (Plan{Images: []Image{{Ref: "x"}}}).Empty() {
		t.Error("Plan with an image is not Empty")
	}
	if (Plan{Volumes: []Volume{{Name: "v"}}}).Empty() {
		t.Error("Plan with a volume is not Empty")
	}
}
