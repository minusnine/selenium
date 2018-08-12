// Binary init downloads the necessary files to perform an integration test between this WebDriver client and multiple versions of Selenium and browsers.
package main

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"github.com/blang/semver"
	"github.com/golang/glog"
	"github.com/google/go-github/github"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

var downloadBrowsers = flag.Bool("download_browsers", true, "If true, download the Firefox and Chrome browsers.")

type file struct {
	url      string
	name     string
	hash     string
	hashType string // default is sha256
	rename   []string
	browser  bool
}

var files = []file{
	{
		url:  "http://selenium-release.storage.googleapis.com/3.4/selenium-server-standalone-3.4.0.jar",
		name: "selenium-server-standalone-3.4.jar",
		hash: "21cbbd775678821b6b72c208b8d59664a4c7381b3c50b008b331914d2834ec8d",
	},
	{
		url:    "https://chromedriver.storage.googleapis.com/2.31/chromedriver_linux64.zip",
		name:   "chromedriver_2.31_linux64.zip",
		hash:   "3e372ef676beb3a03aba72089ec0624bb9d3b52597635f907d4c23390fb485a0",
		rename: []string{"chromedriver", "chromedriver-linux64-2.31"},
	},
	{
		// This is a recent nightly. Update this path periodically.
		url:     "https://archive.mozilla.org/pub/firefox/nightly/2017/08/2017-08-21-10-03-50-mozilla-central/firefox-57.0a1.en-US.linux-x86_64.tar.bz2",
		name:    "firefox-57.0a1.en-US.linux-x86_64.tar.bz2",
		hash:    "77c57356935f66a5a59b1b2cffeaa53b70204195e6a7b15ee828fd3308561e46",
		browser: true,
		rename:  []string{"firefox", "firefox-nightly"},
	},
	{
		url:    "https://saucelabs.com/downloads/sc-4.4.9-linux.tar.gz",
		name:   "sauce-connect-4.4.9-linux.tar.gz",
		hash:   "b1bedccc2690b48d6708ac71f23189c85b0da62c56ee943a1b20d8f17fa8bbde",
		rename: []string{"sc-4.4.9-linux", "sauce-connect"},
	},
}

func latestGitHubRelease(ctx context.Context, g gitHubRelease) (url string, err error) {
	client := github.NewClient(nil)

	rels, _, err := client.Repositories.ListReleases(ctx, g.owner, g.repo, nil)
	if err != nil {
		return "", err
	}
	var latest semver.Version
	var latestRelease *github.RepositoryRelease
	for _, r := range rels {
		ver, err := semver.ParseTolerant(*r.TagName)
		if err != nil {
			glog.V(1).Infof("Invalid tag name: %s/%s %s", g.owner, g.repo, *r.TagName)
			continue
		}
		if ver.GT(latest) {
			latest = ver
			latestRelease = r
		}
	}
	for _, a := range latestRelease.Assets {
		if a.BrowserDownloadURL == nil {
			continue
		}
		if strings.Contains(*a.BrowserDownloadURL, g.filter) {
			return *a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("release for %s/%s containing %q not found", g.owner, g.repo, g.filter)
}

func addChrome(ctx context.Context) error {
	const (
		// Bucket URL: https://console.cloud.google.com/storage/browser/chromium-browser-continuous/?pli=1
		storageBktName = "chromium-browser-snapshots"
		prefixLinux64  = "Linux_x64"
		lastChangeFile = "Linux_x64/LAST_CHANGE"
		chromeFilename = "chrome-linux.zip"
	)
	gcsPath := fmt.Sprintf("gs://%s/", storageBktName)
	client, err := storage.NewClient(ctx, option.WithHTTPClient(http.DefaultClient))
	if err != nil {
		return fmt.Errorf("cannot create a storage client for downloading the chrome browser: %v", err)
	}
	bkt := client.Bucket(storageBktName)
	r, err := bkt.Object(lastChangeFile).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("cannot create a reader for %s%s file: %v", gcsPath, lastChangeFile, err)
	}
	defer r.Close()
	// Read the last change file content for the latest build directory name
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return fmt.Errorf("cannot read from %s%s file: %v", gcsPath, lastChangeFile, err)
	}
	latestChromeBuild := string(data)
	latestChromePackage := path.Join(prefixLinux64, latestChromeBuild, chromeFilename)
	cpAttrs, err := bkt.Object(latestChromePackage).Attrs(ctx)
	if err != nil {
		return fmt.Errorf("cannot get the chrome package %s%s attrs: %v", gcsPath, latestChromePackage, err)
	}
	files = append(files, file{
		name:     chromeFilename,
		browser:  true,
		hash:     hex.EncodeToString(cpAttrs.MD5),
		hashType: "md5",
		url:      cpAttrs.MediaLink,
	})
	return nil
}

type gitHubRelease struct {
	owner, repo, filter string
}

var gitHubReleases = []gitHubRelease{{
	owner:  "mozilla",
	repo:   "geckodriver",
	filter: "-linux64",
}}

func latestSeleniumRelease(ctx context.Context) (url string, err error) {
	const (
		// Bucket URL: https://console.cloud.google.com/storage/browser/selenium-release/?pli=1
		// The object name resembles: 3.8/selenium-server-standalone-3.8.1.jar
		storageBktName = "selenium-release"
		prefixLinux64  = "Linux_x64"
	)
	client, err := storage.NewClient(ctx, option.WithHTTPClient(http.DefaultClient))
	if err != nil {
		return "", fmt.Errorf("cannot create a storage client for downloading the chrome browser: %v", err)
	}
	bkt := client.Bucket(storageBktName)

	object := ""
	latest := semver.Version{}
	it := bkt.Objects(ctx, nil)
	for {
		o, err := it.Next()
		if err != nil {
			if err == iterator.Done {
				break
			}
			return "", err
		}

		// The file name of interest is of the form
		// "3.8/selenium-server-standalone-3.8.1.jar".
		const filePrefix = "selenium-server-standalone-"
		i := strings.Index(o.Name, filePrefix)
		if i < 0 {
			continue
		}
		// Strip off everything through the prefix, plus the ".jar" suffix.
		n := o.Name[i+len(filePrefix) : len(o.Name)-4]
		os, err := semver.ParseTolerant(n)
		if err != nil {
			glog.V(1).Infof("Error parsing object name %s in bucket %s: %s", o.Name, o.Bucket, err)
			continue
		}
		if os.GT(latest) {
			latest = os
			object = o.Name
		}
	}
	if object == "" {
		return "", fmt.Errorf("no release found")
	}
	return object, nil
}

func main() {
	flag.Parse()
	ctx := context.Background()

	if *downloadBrowsers {
		if err := addChrome(ctx); err != nil {
			glog.Errorf("unable to Download Google Chrome browser: %v", err)
		}
	}
	var wg sync.WaitGroup
	/*
		for _, file := range files {
			wg.Add(1)
			file := file
			go func() {
				if err := handleFile(file); err != nil {
					glog.Exitf("Error handling %s: %s", file.name, err)
				}
				wg.Done()
			}()
		}
	*/

	for _, r := range gitHubReleases {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()

			url, err := latestGitHubRelease(ctx, r)
			if err != nil {
				glog.Exitf("Error handling %s/%s: %s", r.owner, r.repo, err)
			}
			err = handleFile(file{
				url:  url,
				name: path.Base(url),
			})
			if err != nil {
				glog.Exitf("Error handling %s/%s: %s", r.owner, r.repo, err)
			}
		}()
	}

	wg.Add(1)
	go func() {
		// TODO(ekg): return the MD5 sum from the object and check it.
		url, err := latestSeleniumRelease(ctx)
		if err != nil {
			glog.Exitf("Error fetching the latest Selenium release: %s", err)
		}
		err = handleFile(file{
			url:  url,
			name: path.Base(url),
		})
		if err != nil {
			glog.Exitf("Error fetching the latest Selenium release: %s", err)
		}
	}()
	wg.Wait()
}

func handleFile(file file) error {
	if file.browser && !*downloadBrowsers {
		glog.Infof("Skipping %q because --download_browser is not set.", file.name)
		return nil
	}

	if _, err := os.Stat(file.name); err != nil {
		glog.Infof("Downloading %q from %q", file.name, file.url)
		if err := downloadFile(file); err != nil {
			return err
		}
	}

	switch path.Ext(file.name) {
	case ".zip":
		glog.Infof("Unzipping %q", file.name)
		if err := exec.Command("unzip", "-o", file.name).Run(); err != nil {
			return fmt.Errorf("Error unzipping %q: %v", file.name, err)
		}
	case ".gz":
		glog.Infof("Unzipping %q", file.name)
		if err := exec.Command("tar", "-xzf", file.name).Run(); err != nil {
			return fmt.Errorf("Error unzipping %q: %v", file.name, err)
		}
	case ".bz2":
		glog.Infof("Unzipping %q", file.name)
		if err := exec.Command("tar", "-xjf", file.name).Run(); err != nil {
			return fmt.Errorf("Error unzipping %q: %v", file.name, err)
		}
	}
	if rename := file.rename; len(rename) == 2 {
		glog.Infof("Renaming %q to %q", rename[0], rename[1])
		os.RemoveAll(rename[1]) // Ignore error.
		if err := os.Rename(rename[0], rename[1]); err != nil {
			glog.Warningf("Error renaming %q to %q: %v", rename[0], rename[1], err)
		}
	}
	return nil
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
	var h hash.Hash
	switch strings.ToLower(file.hashType) {
	case "md5":
		h = md5.New()
	default:
		h = sha256.New()
	}
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("%s: error downloading %q: %v", file.name, file.url, err)
	}
	return nil
}
