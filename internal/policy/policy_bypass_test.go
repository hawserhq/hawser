package policy

import "testing"

func privilegedFree(mounts []any, binds []any) map[string]any {
	hc := map[string]any{}
	if mounts != nil {
		hc["Mounts"] = mounts
	}
	if binds != nil {
		hc["Binds"] = binds
	}
	return map[string]any{"HostConfig": hc}
}

// The same mount, two spellings, must get the same verdict.
//
// bindSources skipped any Mounts entry whose Type was not "bind" — but the
// bridge rewrites Type "npipe" to "bind" immediately after this gate returns,
// and maps the source to the engine's own socket. So the legacy spelling was
// denied while `--mount type=npipe,...` was never examined, and produced a
// container holding the docker socket (#256).
func TestNpipeMountIsJudgedLikeTheBindItBecomes(t *testing.T) {
	r := Rules{AllowBindSources: []string{`C:\work`}}
	const pipe = `\.\pipe\docker_engine`

	legacy := r.EvaluateCreate(privilegedFree(nil, []any{pipe + ":/var/run/docker.sock"}))
	if !legacy.Denied {
		t.Fatal("the legacy Binds spelling was allowed; this test cannot show a divergence")
	}

	modern := r.EvaluateCreate(privilegedFree([]any{
		map[string]any{"Type": "npipe", "Source": pipe, "Target": "/var/run/docker.sock"},
	}, nil))
	if !modern.Denied {
		t.Error("--mount type=npipe bypassed allow-bind-sources while the same mount spelled as a bind was denied")
	}
}

// Volume and tmpfs mounts have a name, not a path, as their Source. Widening
// the type set must not start judging them as bind sources.
func TestNamedVolumeMountIsNotJudgedAsABindSource(t *testing.T) {
	r := Rules{AllowBindSources: []string{`C:\work`}}
	for _, typ := range []string{"volume", "tmpfs"} {
		d := r.EvaluateCreate(privilegedFree([]any{
			map[string]any{"Type": typ, "Source": "mydata", "Target": "/data"},
		}, nil))
		if d.Denied {
			t.Errorf("a %s mount was judged as a bind source: %s", typ, d.Reason)
		}
	}
}

// The prefix match must resolve traversal before comparing. winpath.ToWSL
// passes the remainder through verbatim, so a source spelled as an allowed
// root plus parent-directory segments used to satisfy the test and then mount
// somewhere else entirely.
func TestAllowBindSourcesResolvesTraversalBeforeComparing(t *testing.T) {
	r := Rules{AllowBindSources: []string{`C:\work`}}
	for _, src := range []string{
		`C:\work\..\Windows\System32`,
		`C:\work\..\..\secrets`,
		`C:/work/../Users`,
	} {
		d := r.EvaluateCreate(privilegedFree(nil, []any{src + ":/mnt"}))
		if !d.Denied {
			t.Errorf("allow-bind-sources accepted %q, which resolves outside the allowed root", src)
		}
	}
	// And the legitimate ones still pass.
	for _, src := range []string{`C:\work`, `C:\work\proj`, `c:/WORK/proj/sub`, `C:\work\a\..\b`} {
		if d := r.EvaluateCreate(privilegedFree(nil, []any{src + ":/mnt"})); d.Denied {
			t.Errorf("allow-bind-sources rejected %q, which is inside the allowed root: %s", src, d.Reason)
		}
	}
}
