package selenium

import "testing"

func testExtension(t *testing.T, c config) {
	var co ChromeOptions
	if *runningUnderDocker {
		co.Args = []string{"--no-sandbox"}
	}
	path := "testing/make_page_red.crx"
	if err := co.AddExtension(path); err != nil {
		t.Fatalf("co.AddExtension(%q) returned error: %v", path, err)
	}

	caps := Capabilities{
		"browserName":   c.browser,
		"chromeOptions": co,
	}
	wd, err := NewRemote(caps, c.addr)
	if err != nil {
		t.Fatalf("NewRemote(_, _) returned error: %v", err)
	}
	defer wd.Quit()

	// TODO(minusnine): interact with the extension and make sure the page turns
	// red.

	const extID = "bkhkdlenbkmokhgobcccamljmdakhoie"
	t.Fail()
}
