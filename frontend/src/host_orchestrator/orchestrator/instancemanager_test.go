// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/google/android-cuttlefish/frontend/src/liboperator/api/v1"
	"github.com/google/android-cuttlefish/frontend/src/liboperator/operator"
)

func TestCreateCVDInvalidRequestsEmptyFields(t *testing.T) {
	im := &CVDToolInstanceManager{}
	var validRequest = apiv1.CreateCVDRequest{
		BuildInfo: &apiv1.BuildInfo{
			BuildID: "1234",
			Target:  "aosp_cf_x86_64_phone-userdebug",
		},
	}
	// Make sure the valid request is indeed valid.
	if err := validateRequest(&validRequest); err != nil {
		t.Fatalf("the valid request is not valid")
	}
	var tests = []struct {
		corruptRequest func(r *apiv1.CreateCVDRequest)
	}{
		{func(r *apiv1.CreateCVDRequest) { r.BuildInfo = nil }},
		{func(r *apiv1.CreateCVDRequest) { r.BuildInfo.BuildID = "" }},
		{func(r *apiv1.CreateCVDRequest) { r.BuildInfo.Target = "" }},
	}

	for _, test := range tests {
		req := validRequest
		test.corruptRequest(&req)
		_, err := im.CreateCVD(req)
		var appErr *operator.AppError
		if !errors.As(err, &appErr) {
			t.Errorf("error type <<\"%T\">> not found in error chain", appErr)
		}
		var emptyFieldErr EmptyFieldError
		if !errors.As(err, &emptyFieldErr) {
			t.Errorf("error type <<\"%T\">> not found in error chain", emptyFieldErr)
		}
	}
}

type testCVDDwnlder struct {
	count int
}

func (d *testCVDDwnlder) Download(_ string, _ AndroidBuild) error {
	d.count += 1
	return nil
}

func TestCreateCVDToolCVDIsDownloadedOnce(t *testing.T) {
	execContext := func(name string, args ...string) *exec.Cmd {
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	om := NewMapOM()
	cvdDwnlder := &testCVDDwnlder{}
	im := NewCVDToolInstanceManager(execContext, cvdBinAB, IMPaths{}, cvdDwnlder, om)
	r1 := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}
	r2 := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "2", Target: "foo"}}

	op1, _ := im.CreateCVD(r1)
	op2, _ := im.CreateCVD(r2)

	om.Wait(op1.Name, 1*time.Second)
	om.Wait(op2.Name, 1*time.Second)

	if cvdDwnlder.count == 0 {
		t.Error("cvd was never downloaded")
	}
	if cvdDwnlder.count > 1 {
		t.Errorf("cvd was downloaded more than once, it was <<%d>> times", cvdDwnlder.count)
	}
}

func TestCreateCVDSameTargetArtifactsIsDownloadedOnce(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	fetchCVDExecCounter := 0
	execContext := func(name string, args ...string) *exec.Cmd {
		if contains(args, "fetch") {
			fetchCVDExecCounter += 1
		}
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	paths := IMPaths{
		CVDBin:           dir + "/cvd",
		ArtifactsRootDir: dir + "/artifacts",
		HomesRootDir:     dir + "/homes",
	}
	om := NewMapOM()
	cvdDwnlder := &testCVDDwnlder{}
	im := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, cvdDwnlder, om)
	r1 := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}
	r2 := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}

	op1, _ := im.CreateCVD(r1)
	op2, _ := im.CreateCVD(r2)

	om.Wait(op1.Name, 1*time.Second)
	om.Wait(op2.Name, 1*time.Second)

	if fetchCVDExecCounter == 0 {
		t.Error("`cvd fetch` was never executed")
	}
	if fetchCVDExecCounter > 1 {
		t.Errorf("`cvd fetch` was downloaded more than once, it was <<%d>> times", fetchCVDExecCounter)
	}
}

func TestCreateCVDInstanceHomeDirAlreadyExist(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	execContext := func(name string, args ...string) *exec.Cmd {
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	paths := IMPaths{
		CVDBin:           dir + "/cvd",
		ArtifactsRootDir: dir + "/artifacts",
		HomesRootDir:     dir + "/homes",
	}
	om := NewMapOM()
	cvdDwnlder := &testCVDDwnlder{}
	im1 := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, cvdDwnlder, om)
	r := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}
	op, _ := im1.CreateCVD(r)
	om.Wait(op.Name, 1*time.Second)
	// The second instance manager is created with the same im paths as the previous instance
	// manager, this will lead to create an instance home dir that already exist.
	im2 := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, cvdDwnlder, om)

	op, _ = im2.CreateCVD(r)

	op, _ = om.Wait(op.Name, 1*time.Second)
	if op.Result.Error.Error == "" {
		t.Error("expected error due instance home dir already existing")
	}
}

func TestCreateCVDVerifyRootDirectoriesAreCreated(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	execContext := func(name string, args ...string) *exec.Cmd {
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	paths := IMPaths{
		CVDBin:           dir + "/cvd",
		ArtifactsRootDir: dir + "/artifacts",
		HomesRootDir:     dir + "/homes",
	}
	om := NewMapOM()
	cvdDwnlder := &testCVDDwnlder{}
	im := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, cvdDwnlder, om)
	r := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}

	op, _ := im.CreateCVD(r)

	om.Wait(op.Name, 1*time.Second)

	expected := "drwxrwxr--"
	stats, _ := os.Stat(paths.ArtifactsRootDir)
	if stats.Mode().String() != expected {
		t.Errorf("expected <<%q>, got %q", expected, stats.Mode().String())
	}
	stats, _ = os.Stat(paths.HomesRootDir)
	if stats.Mode().String() != expected {
		t.Errorf("expected <<%q>, got %q", expected, stats.Mode().String())
	}
}

func TestCreateCVDVerifyFetchCVDCmdArgs(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	var usedCmdName string
	var usedCmdArgs []string
	execContext := func(name string, args ...string) *exec.Cmd {
		if contains(args, "fetch") {
			usedCmdName = name
			usedCmdArgs = args
		}
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	paths := IMPaths{
		CVDBin:           dir + "/cvd",
		ArtifactsRootDir: dir + "/artifacts",
		HomesRootDir:     dir + "/homes",
	}
	om := NewMapOM()
	im := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, &testCVDDwnlder{}, om)
	r := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}

	op, _ := im.CreateCVD(r)

	om.Wait(op.Name, 1*time.Second)
	if usedCmdName != "sudo" {
		t.Errorf("expected 'sudo', got %q", usedCmdName)
	}
	expectedCmdArgs := []string{
		"-u", "_cvd-executor", envVarAndroidHostOut + "=", envVarHome + "=", paths.CVDBin, "fetch",
		"--default_build=1/foo", "--directory=" + paths.ArtifactsRootDir + "/1_foo",
	}
	if !reflect.DeepEqual(usedCmdArgs, expectedCmdArgs) {
		t.Errorf("invalid args\nexpected: %+v\ngot:      %+v", expectedCmdArgs, usedCmdArgs)
	}
}

func TestCreateCVDVerifyStartCVDCmdArgs(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	var usedCmdName string
	var usedCmdArgs []string
	execContext := func(name string, args ...string) *exec.Cmd {
		if contains(args, "start") {
			usedCmdName = name
			usedCmdArgs = args
		}
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	paths := IMPaths{
		CVDBin:           dir + "/cvd",
		ArtifactsRootDir: dir + "/artifacts",
		HomesRootDir:     dir + "/homes",
	}
	om := NewMapOM()
	im := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, &testCVDDwnlder{}, om)
	r := apiv1.CreateCVDRequest{BuildInfo: &apiv1.BuildInfo{BuildID: "1", Target: "foo"}}

	op, _ := im.CreateCVD(r)

	om.Wait(op.Name, 1*time.Second)
	if usedCmdName != "sudo" {
		t.Errorf("expected 'sudo', got %q", usedCmdName)
	}
	artifactsDir := paths.ArtifactsRootDir + "/1_foo"
	homeDir := paths.HomesRootDir + "/cvd-1"
	expectedCmdArgs := []string{
		"-u", "_cvd-executor", envVarAndroidHostOut + "=" + artifactsDir, envVarHome + "=" + homeDir,
		paths.CVDBin, "start", daemonArg, reportAnonymousUsageStatsArg,
		"--base_instance_num=1", "--system_image_dir=" + artifactsDir,
	}
	if !reflect.DeepEqual(usedCmdArgs, expectedCmdArgs) {
		t.Errorf("invalid args\nexpected: %+v\ngot:      %+v", expectedCmdArgs, usedCmdArgs)
	}
}

func TestCreateCVDSucceeds(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	execContext := func(name string, args ...string) *exec.Cmd {
		return buildTestCmd()
	}
	cvdBinAB := AndroidBuild{ID: "1", Target: "xyzzy"}
	paths := IMPaths{
		CVDBin:           dir + "/cvd",
		ArtifactsRootDir: dir + "/artifacts",
		HomesRootDir:     dir + "/homes",
	}
	om := NewMapOM()
	cvdDwnlder := &testCVDDwnlder{}
	im := NewCVDToolInstanceManager(execContext, cvdBinAB, paths, cvdDwnlder, om)
	buildInfo := &apiv1.BuildInfo{BuildID: "1", Target: "foo"}
	r := apiv1.CreateCVDRequest{BuildInfo: buildInfo}

	op, _ := im.CreateCVD(r)

	op, _ = om.Wait(op.Name, 1*time.Second)

	cvd := op.Result.Response.(*apiv1.CVD)
	expectedName := "cvd-1"
	if cvd.Name != expectedName {
		t.Errorf("expected %q, got %q", expectedName, cvd.Name)
	}
	if !reflect.DeepEqual(cvd.BuildInfo, buildInfo) {
		t.Errorf("expected: %+v, got: %+v", buildInfo, cvd.BuildInfo)
	}
}

type FakeArtifactDownloader struct {
	t       *testing.T
	content string
}

func (d *FakeArtifactDownloader) Download(dst io.Writer, _ AndroidBuild, name string) error {
	r := strings.NewReader(d.content)
	if _, err := io.Copy(dst, r); err != nil {
		d.t.Fatal(err)
	}
	return nil
}

func TestCVDDownloaderDownloadBinaryAlreadyExist(t *testing.T) {
	const fetchCVDContent = "bar"
	dir := tempDir(t)
	defer removeDir(t, dir)
	filename := dir + "/cvd"
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = f.Write([]byte(fetchCVDContent))
	if err != nil {
		t.Fatal(err)
	}
	ad := &FakeArtifactDownloader{t, "foo"}
	cd := NewCVDDownloader(ad)

	err = cd.Download(filename, AndroidBuild{ID: "1", Target: "xyzzy"})

	if err != nil {
		t.Errorf("epected <<nil>> error, got %#v", err)
	}
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	actual := string(content)
	if actual != fetchCVDContent {
		t.Errorf("expected <<%q>>, got %q", fetchCVDContent, actual)
	}
}

func TestCVDDownloaderDownload(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	filename := dir + "/cvd"
	ad := &FakeArtifactDownloader{t, "foo"}
	cd := NewCVDDownloader(ad)

	cd.Download(filename, AndroidBuild{ID: "1", Target: "xyzzy"})

	content, _ := ioutil.ReadFile(filename)
	actual := string(content)
	expected := "foo"
	if actual != expected {
		t.Errorf("expected <<%q>>, got %q", expected, actual)
	}
}

func TestCVDDownloaderDownload0750FileAccessIsSet(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	filename := dir + "/cvd"
	ad := &FakeArtifactDownloader{t, "foo"}
	cd := NewCVDDownloader(ad)

	cd.Download(filename, AndroidBuild{ID: "1", Target: "xyzzy"})

	stats, _ := os.Stat(filename)
	var expected os.FileMode = 0750
	if stats.Mode() != expected {
		t.Errorf("expected <<%+v>>, got %+v", expected, stats.Mode())
	}
}

type AlwaysFailsArtifactDownloader struct {
	err error
}

func (d *AlwaysFailsArtifactDownloader) Download(_ io.Writer, _ AndroidBuild, _ string) error {
	return d.err
}

func TestCVDDownloaderDownloadingFails(t *testing.T) {
	dir := tempDir(t)
	defer removeDir(t, dir)
	filename := dir + "/cvd"
	expectedErr := errors.New("error")
	cd := NewCVDDownloader(&AlwaysFailsArtifactDownloader{err: expectedErr})

	err := cd.Download(filename, AndroidBuild{ID: "1", Target: "xyzzy"})

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected <<%+v>>, got %+v", expectedErr, err)
	}
	if _, err := os.Stat(filename); err == nil {
		t.Errorf("file must not have been created")
	}
}

type roundTripFunc func(r *http.Request) (*http.Response, error)

func (s roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return s(r)
}

func newMockClient(rt roundTripFunc) *http.Client {
	return &http.Client{Transport: rt}
}

func newResponseBody(content string) io.ReadCloser {
	return ioutil.NopCloser(strings.NewReader(content))
}

func TestSignedURLArtifactDownloaderDownload(t *testing.T) {
	fetchCVDBinContent := "001100"
	getSignedURLRequestURI := "/android/internal/build/v3/builds/1/xyzzy/attempts/latest/artifacts/foo/url?redirect=false"
	downloadRequestURI := "/android-build/builds/X/Y/Z"
	url := "https://someurl.fake"
	mockClient := newMockClient(func(r *http.Request) (*http.Response, error) {
		res := &http.Response{
			StatusCode: http.StatusOK,
		}
		reqURI := r.URL.RequestURI()
		if reqURI == getSignedURLRequestURI {
			resURL := url + downloadRequestURI
			res.Body = newResponseBody(`{"signedUrl": "` + resURL + `"}`)
		} else if reqURI == downloadRequestURI {
			res.Body = newResponseBody(fetchCVDBinContent)
		} else {
			t.Fatalf("invalide request URI: %q\n", reqURI)
		}
		return res, nil
	})
	d := NewSignedURLArtifactDownloader(mockClient, url)

	var b bytes.Buffer
	d.Download(io.Writer(&b), AndroidBuild{ID: "1", Target: "xyzzy"}, "foo")

	actual := b.String()
	if actual != fetchCVDBinContent {
		t.Errorf("expected <<%q>>, got %q", fetchCVDBinContent, actual)
	}
}

func TestSignedURLArtifactDownloaderDownloadWithError(t *testing.T) {
	errorMessage := "No latest build attempt for build 1"
	url := "https://something.fake"
	mockClient := newMockClient(func(r *http.Request) (*http.Response, error) {
		errJSON := `{
			"error": {
				"code": 401,
				"message": "` + errorMessage + `"
			}
		}`
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       newResponseBody(errJSON),
		}, nil
	})
	d := NewSignedURLArtifactDownloader(mockClient, url)

	var b bytes.Buffer
	err := d.Download(io.Writer(&b), AndroidBuild{ID: "1", Target: "xyzzy"}, "foo")

	if !strings.Contains(err.Error(), errorMessage) {
		t.Errorf("expected to contain <<%q>> in error: %#v", errorMessage, err)
	}
}

func TestBuildGetSignedURL(t *testing.T) {
	baseURL := "http://localhost:1080"

	t.Run("regular build id", func(t *testing.T) {
		expected := "http://localhost:1080/android/internal/build/v3/builds/1/xyzzy/attempts/latest/artifacts/foo/url?redirect=false"

		actual := BuildGetSignedURL(baseURL, AndroidBuild{ID: "1", Target: "xyzzy"}, "foo")

		if actual != expected {
			t.Errorf("expected <<%q>>, got %q", expected, actual)
		}
	})

	t.Run("url-escaped android build params", func(t *testing.T) {
		expected := "http://localhost:1080/android/internal/build/v3/builds/1%3F/xyzzy%3F/attempts/latest/artifacts/foo/url?redirect=false"

		actual := BuildGetSignedURL(baseURL, AndroidBuild{ID: "1?", Target: "xyzzy?"}, "foo")

		if actual != expected {
			t.Errorf("expected <<%q>>, got %q", expected, actual)
		}
	})
}

// Creates a temporary directory for the test to use returning its path.
// Each subsequent call creates a unique directory; if the directory creation
// fails, `tempDir` terminates the test by calling Fatal.
func tempDir(t *testing.T) string {
	name, err := ioutil.TempDir("", "cuttlefishTestDir")
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// Removes the directory at the passed path.
// If deletion fails, `removeDir` terminates the test by calling Fatal.
func removeDir(t *testing.T, name string) {
	if err := os.RemoveAll(name); err != nil {
		t.Fatal(err)
	}
}

type fakeMainFunc func(*testing.T)

// NOTE: This test is not a regular unit tests. It simulates a fake binary execution.
func TestFakeBinaryMain(t *testing.T) {}

// Creates a new exec.Cmd, which will call the `TestFakeBinaryMain` function through the execution
// of the `go test` binary using the parameter `--test.run`.
func buildTestCmd() *exec.Cmd {
	cs := []string{"--test.run=" + funcName(TestFakeBinaryMain)}
	cmd := exec.Command(os.Args[0], cs...)
	return cmd
}

func funcName(fn fakeMainFunc) string {
	name := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	return name[strings.LastIndex(name, ".")+1:]
}

func contains(values []string, t string) bool {
	for _, v := range values {
		if v == t {
			return true
		}
	}
	return false
}
