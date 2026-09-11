package doctor

// checkNetwork advises on the corporate-network setup (#62): the classic "works
// at home, breaks at work" failure is a TLS-inspecting proxy whose root CA the
// engine does not trust, so `docker pull` dies with an x509 error. If a proxy is
// configured but host CAs are not imported, that is exactly the setup that
// produces it — so say so before the user hits it.
func checkNetwork() Check {
	c := Check{Name: "network", Title: "corporate network"}
	c.Run = func(f Facts) Result {
		switch {
		case f.Proxy != "" && !f.ImportHostCAs:
			r := result(c, Warn, "a proxy is set but the host's CA store is not trusted by the engine")
			r.Detail = []string{"  proxy: " + f.Proxy}
			r.Remedy = "if pulls fail with x509 / certificate errors behind a TLS-inspecting " +
				"proxy, run `skrog config set network.import-host-cas on` then `skrog restart`."
			return r
		case f.ImportHostCAs && f.Proxy != "":
			return result(c, OK, "proxy set and host CAs trusted by the engine")
		case f.ImportHostCAs:
			return result(c, OK, "host CAs trusted by the engine")
		case f.Proxy != "":
			return result(c, OK, "engine proxy configured")
		default:
			return result(c, Skip, "no corporate proxy or CA import configured")
		}
	}
	return c
}
