package doctor

import "testing"

func TestCheckDisk(t *testing.T) {
	c := checkDisk()
	cases := []struct {
		name string
		disk DiskInfo
		want Status
	}{
		{"unavailable", DiskInfo{Err: "windows only"}, Skip},
		{"critically low", DiskInfo{FreeBytes: 1 << 30, TotalBytes: 100 << 30}, Fail},
		{"low", DiskInfo{FreeBytes: 4 << 30, TotalBytes: 100 << 30}, Warn},
		{"plenty", DiskInfo{FreeBytes: 50 << 30, TotalBytes: 100 << 30}, OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(Facts{Disk: tc.disk}).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:           "512 B",
		2 * 1024:      "2.0 KiB",
		5 << 30:       "5.0 GiB",
		3 * (1 << 40): "3.0 TiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
