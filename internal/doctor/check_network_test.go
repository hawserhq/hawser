package doctor

import "testing"

func TestCheckNetwork(t *testing.T) {
	c := checkNetwork()
	cases := []struct {
		name          string
		proxy         string
		importHostCAs bool
		want          Status
	}{
		{"nothing configured", "", false, Skip},
		{"proxy without CA import", "http://px:8080", false, Warn},
		{"proxy with CA import", "http://px:8080", true, OK},
		{"CA import only", "", true, OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Run(Facts{Proxy: tc.proxy, ImportHostCAs: tc.importHostCAs}).Status
			if got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
