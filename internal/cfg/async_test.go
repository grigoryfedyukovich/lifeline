package cfg

import "testing"

func TestCallSiteRecordsAsynchronousLaunch(t *testing.T) {
	_, sites, _, body := buildWithCallSites(t, "package p\nfunc launched(){};func delayed(){};func called(){}\nfunc f(){go launched();defer delayed();called()}", "f")
	for _, name := range []string{"launched", "delayed", "called"} {
		site, ok := sites[callNamed(t, body, name)]
		if !ok || site.Async != (name == "launched") {
			t.Errorf("%s: %+v present=%v", name, site, ok)
		}
	}
}
