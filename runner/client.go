package runner

import (
	"fmt"
	"net/http"
	"os"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/engine-api/client"
	"github.com/docker/golem/clientutil"
	"github.com/docker/golem/versionutil"
	"github.com/jlhawn/dockramp/build"
)

// DockerClient represents the docker client used by the runner
type DockerClient struct {
	*client.Client
	options *clientutil.ClientOptions
}

// newDockerClient creates a new docker client from client options
func newDockerClient(co *clientutil.ClientOptions) (DockerClient, error) {
	var httpClient *http.Client
	tlsConfig := co.TLSConfig()
	host := co.DaemonURL()

	if tlsConfig != nil {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		}
	}

	apiClient, err := client.NewClient(host, os.Getenv("DOCKER_API_VERSION"), httpClient, nil)
	if err != nil {
		return DockerClient{}, err
	}

	return DockerClient{
		Client:  apiClient,
		options: co,
	}, nil
}

// NewBuilder creates a new docker builder using the given client
func (dc DockerClient) NewBuilder(contextDirectory, dockerfilePath, repoTag string) (*build.Builder, error) {
	if dc.options == nil {
		return nil, fmt.Errorf("missing client options, cannot create builder")
	}
	return build.NewBuilder(dc.options.DaemonURL(), dc.options.TLSConfig(), contextDirectory, dockerfilePath, repoTag)
}

// CheckServerVersion checks that the server version is atleast
// the provided version, throws an error if not
func (dc DockerClient) CheckServerVersion(version versionutil.Version) error {
	ctx := context.Background()
	v, err := dc.ServerVersion(ctx)
	if err != nil {
		return fmt.Errorf("error getting version: %v", err)
	}

	serverVersion, err := versionutil.ParseVersion(v.Version)
	if err != nil {
		return fmt.Errorf("error parsing version %s: %v", v.Version, err)
	}

	if serverVersion.LessThan(version) {
		return fmt.Errorf("unsupported Docker version %s, golem requires running on at least %s", serverVersion, version)
	}

	logrus.Debugf("Client connected to server with version %s", serverVersion)

	return nil
}
