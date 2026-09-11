package doctor

import (
	"path/filepath"
	"strings"
)

// checkCLI reports on the bundled docker CLI (#66): whether it is installed and,
// if so, whether it is the docker the shell actually resolves. The point of the
// bundle is to let a user delete Docker Desktop, so the failure that matters is
// "installed, but Desktop still wins on PATH" — which looks like success until
// you try to stop the Desktop service.
func checkCLI() Check {
	c := Check{Name: "docker-bundle", Title: "bundled docker CLI"}
	c.Run = func(f Facts) Result {
		if !f.CLI.Installed {
			r := result(c, Skip, "the bundled docker CLI is not installed")
			r.Remedy = "to run docker without Docker Desktop, `skrog cli install` " +
				"installs the docker CLI + compose + buildx, checksum-verified."
			return r
		}

		activeDir := ""
		if f.CLI.ActiveDocker != "" {
			activeDir = filepath.Dir(f.CLI.ActiveDocker)
		}
		if strings.EqualFold(filepath.Clean(activeDir), filepath.Clean(f.CLI.BinDir)) {
			return result(c, OK, "the bundled docker CLI is installed and active")
		}

		if !f.CLI.OnPath {
			r := result(c, Warn, "the bundled docker CLI is installed but its directory is not on your PATH")
			r.Detail = []string{"  " + f.CLI.BinDir}
			r.Remedy = "run `skrog cli install` (it adds the directory to your user PATH), " +
				"or add the directory above yourself."
			return r
		}

		// On PATH, but another docker resolves first — typically Docker Desktop
		// still ahead, or a shell opened before the PATH change.
		r := result(c, Warn, "another docker shadows the bundled CLI on PATH")
		detail := []string{"  bundled: " + filepath.Join(f.CLI.BinDir, "docker.exe")}
		if f.CLI.ActiveDocker != "" {
			detail = append(detail, "  active:  "+f.CLI.ActiveDocker)
		}
		r.Detail = detail
		r.Remedy = "open a new terminal; if it persists, make sure Skrog's bin directory " +
			"precedes Docker Desktop on PATH. Then you can uninstall Docker Desktop."
		return r
	}
	return c
}
