// Copyright 2021 Google LLC
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

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"cuttlefish/host-orchestrator/orchestrator"
	apiv1 "cuttlefish/liboperator/api/v1"
	"cuttlefish/liboperator/operator"
)

const (
	defaultAndroidBuildURL = "https://androidbuildinternal.googleapis.com"
	defaultCVDArtifactsDir = "/var/lib/cuttlefish-common"
)

func startHttpServer(port string) error {
	log.Println(fmt.Sprint("Host Orchestrator is listening at http://localhost:", port))

	// handler is nil, so DefaultServeMux is used.
	return http.ListenAndServe(fmt.Sprint(":", port), nil)
}

func startHttpsServer(port string, certPath string, keyPath string) error {
	log.Println(fmt.Sprint("Host Orchestrator is listening at https://localhost:", port))
	return http.ListenAndServeTLS(fmt.Sprint(":", port),
		certPath,
		keyPath,
		// handler is nil, so DefaultServeMux is used.
		//
		// Using DefaultServerMux in both servers (http and https) is not a problem
		// as http.ServeMux instances are thread safe.
		nil)
}

func fromEnvOrDefault(key string, def string) string {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	return val
}

// Whether a device file request should be intercepted and served from the signaling server instead
func maybeIntercept(path string) *string {
	if path == "/js/server_connector.js" {
		alt := fmt.Sprintf("%s%s", operator.DefaultInterceptDir, path)
		return &alt
	}
	return nil
}

func start(starters []func() error) {
	wg := new(sync.WaitGroup)
	wg.Add(len(starters))
	for _, starter := range starters {
		go func(f func() error) {
			defer wg.Done()
			if err := f(); err != nil {
				log.Fatal(err)
			}
		}(starter)
	}
	wg.Wait()
}

func main() {
	socketPath := fromEnvOrDefault("ORCHESTRATOR_SOCKET_PATH", operator.DefaultSocketPath)
	httpPort := fromEnvOrDefault("ORCHESTRATOR_HTTP_PORT", operator.DefaultHttpPort)
	httpsPort := fromEnvOrDefault("ORCHESTRATOR_HTTPS_PORT", operator.DefaultHttpsPort)
	tlsCertDir := fromEnvOrDefault("ORCHESTRATOR_TLS_CERT_DIR", operator.DefaultTLSCertDir)
	certPath := tlsCertDir + "/cert.pem"
	keyPath := tlsCertDir + "/key.pem"

	pool := operator.NewDevicePool()
	polledSet := operator.NewPolledSet()
	config := apiv1.InfraConfig{
		Type: "config",
		IceServers: []apiv1.IceServer{
			apiv1.IceServer{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
	abURL := fromEnvOrDefault("ORCHESTRATOR_ANDROID_BUILD_URL", defaultAndroidBuildURL)
	imRootDir := fromEnvOrDefault("ORCHESTRATOR_CVD_ARTIFACTS_DIR", defaultCVDArtifactsDir)
	artifactDownloader := orchestrator.NewSignedURLArtifactDownloader(http.DefaultClient, abURL)
	cvdDownloader := orchestrator.NewCVDDownloader(artifactDownloader)
	om := orchestrator.NewMapOM()
	im := orchestrator.NewInstanceManager(imRootDir, om, cvdDownloader)

	deviceServerLoop := operator.SetupDeviceEndpoint(pool, config, socketPath)
	go func() {
		err := deviceServerLoop()
		log.Fatal("Error with device endpoint: ", err)
	}()
	r := operator.CreateHttpHandlers(pool, polledSet, config, maybeIntercept, true /*acceptsWS*/)
	orchestrator.SetupInstanceManagement(r, im, om)
	// The host orchestrator currently has no use for this, since clients won't connect
	// to it directly, however they probably will once the multi-device feature matures.
	fs := http.FileServer(http.Dir(operator.DefaultStaticFilesDir))
	r.PathPrefix("/").Handler(fs)
	http.Handle("/", r)

	starters := []func() error{
		func() error { return operator.SetupDeviceEndpoint(pool, config, socketPath)() },
		func() error { return startHttpsServer(httpsPort, certPath, keyPath) },
		func() error { return startHttpServer(httpPort) },
	}
	start(starters)
}