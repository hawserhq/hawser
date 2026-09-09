package doctor

import "testing"

func TestCheckGPU(t *testing.T) {
	cases := []struct {
		name string
		gpu  GPUStatus
		want Status
	}{
		{"no engine", GPUStatus{EngineInstalled: false}, Skip},
		{"off, no gpu seen", GPUStatus{EngineInstalled: true, ConfigEnabled: false}, Skip},
		{"off but gpu visible", GPUStatus{EngineInstalled: true, ConfigEnabled: false, Probed: true, Visible: true}, Skip},
		{"on, engine down", GPUStatus{EngineInstalled: true, ConfigEnabled: true, Probed: false}, OK},
		{"on, working", GPUStatus{EngineInstalled: true, ConfigEnabled: true, Probed: true, Visible: true, SpecInstalled: true}, OK},
		{"on but gpu gone", GPUStatus{EngineInstalled: true, ConfigEnabled: true, Probed: true, Visible: false, SpecInstalled: true}, Warn},
		{"on but spec missing", GPUStatus{EngineInstalled: true, ConfigEnabled: true, Probed: true, Visible: true, SpecInstalled: false}, Warn},
	}
	c := checkGPU()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(Facts{GPU: tc.gpu}).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
