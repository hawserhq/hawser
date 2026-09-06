package doctor

import "testing"

func TestCheckSession0(t *testing.T) {
	c := checkSession0()

	if got := c.Run(Facts{Session0: Session0Info{AutostartConfigured: true}}).Status; got != OK {
		t.Errorf("autostart on: got %v, want OK", got)
	}
	r := c.Run(Facts{Session0: Session0Info{AutostartConfigured: false}})
	if r.Status != Skip {
		t.Errorf("autostart off: got %v, want Skip", r.Status)
	}
	if r.Remedy == "" {
		t.Error("autostart off: expected guidance in the remedy")
	}
}
