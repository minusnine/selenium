// The init binary downloads the WebDriver JARs and the ChromeDriver binary.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"

	"github.com/golang/glog"
)

// file represents a file to download.
type file struct {
	// url is the URL from which to download the file.
	url string
	// name is the target filename to store the file.
	name string
	// hash is the expected sha256 hash of the file contents.
	hash string
	// rename specifies a pair of filenames: the which to rename from and to.
	rename []string
}

var files = []file{
	{
		url:  "http://selenium-release.storage.googleapis.com/3.0/selenium-server-standalone-3.0.1.jar",
		name: "selenium-server-standalone-3.0.1.jar",
		hash: "1537b6d1b259191ed51586378791bc62b38b0cb18ae5ba1433009dc365e9f26b",
	},
	{
		url:  "http://selenium-release.storage.googleapis.com/2.53/selenium-server-standalone-2.53.1.jar",
		name: "selenium-server-standalone-2.53.1.jar",
		hash: "1cce6d3a5ca5b2e32be18ca5107d4f21bddaa9a18700e3b117768f13040b7cf8",
	},
	{
		url:    "https://chromedriver.storage.googleapis.com/2.26/chromedriver_linux64.zip",
		name:   "chromedriver_linux64.zip",
		hash:   "59e6b1b1656a20334d5731b3c5a7400f92a9c6f5043bb4ab67f1ccf1979ee486",
		rename: []string{"chromedriver", "chromedriver-linux64-2.26"},
	},
}

func main() {
	flag.Parse()

	for _, file := range files {
		if !fileSameHash(file.name, file.hash) {
			if err := downloadFile(file); err != nil {
				glog.Exit(err.Error())
			}
		} else {
			glog.Infof("Skipping file %q which has already been downloaded.", file.name)
		}
		if ext := path.Ext(file.name); ext == ".zip" {
			if err := exec.Command("unzip", file.name).Run(); err != nil {
				glog.Exitf("Error unzipping %q: %v", file.name, err)
			}
		}
		if rename := file.rename; len(rename) == 2 {
			if err := os.Rename(rename[0], rename[1]); err != nil {
				glog.Warningf("Error renaming %q to %q: %v", rename[0], rename[1], err)
			}
		}
	}
}

func downloadFile(file file) (err error) {
	f, err := os.Create(file.name)
	if err != nil {
		return fmt.Errorf("error creating %q: %v", file.name, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("error closing %q: %v", file.name, err)
		}
	}()

	resp, err := http.Get(file.url)
	if err != nil {
		return fmt.Errorf("%s: error downloading %q: %v", file.name, file.url, err)
	}
	defer resp.Body.Close()

	hash := sha256.New()
	tee := io.MultiWriter(f, hash)
	if _, err := io.Copy(tee, resp.Body); err != nil {
		return fmt.Errorf("%s: error downloading %q: %v", file.name, file.url, err)
	}
	if h := hex.EncodeToString(hash.Sum(nil)); h != file.hash {
		return fmt.Errorf("%s: got sha256 hash %q, want %q", file.name, h, file.hash)
	}
	return nil
}

func fileSameHash(fileName, wantHash string) bool {
	if _, err := os.Stat(fileName); err != nil {
		return false
	}
	h := sha256.New()
	f, err := os.Open(fileName)
	if err != nil {
		return false
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return false
	}

	sum := hex.EncodeToString(h.Sum(nil))
	return sum == wantHash
}
