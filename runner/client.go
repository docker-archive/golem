package runner

import (
	"fmt"

	"github.com/Sirupsen/logrus"
	"github.com/docker/golem/clientutil"
	"github.com/docker/golem/versionutil"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/jlhawn/dockramp/build"
)

// DockerClient represents the docker client used by the runner
type DockerClient struct {
	*dockerclient.Client
	options *clientutil.ClientOptions
}

// NewDockerClient creates a new docker client from client options
func NewDockerClient(co *clientutil.ClientOptions) (client DockerClient, err error) {
	tlsConfig := co.TLSConfig()
	var dc *dockerclient.Client
	if tlsConfig != nil {
		dc, err = dockerclient.NewTLSClient(co.DaemonURL(), co.ClientCertFile(), co.ClientKeyFile(), co.CACertFile())
		if err != nil {
			return
		}
	} else {
		dc, err = dockerclient.NewClient(co.DaemonURL())
		if err != nil {
			return
		}
	}

	return DockerClient{
		Client:  dc,
		options: co,
	}, nil
}

// NewBuilder creates a new docker builder using the given client
func (dc DockerClient) NewBuilder(contextDirectory, dockerfilePath, repoTag string) (*build.Builder, error) {
	return build.NewBuilder(dc.options.DaemonURL(), dc.options.TLSConfig(), contextDirectory, dockerfilePath, repoTag)
}

// CheckServerVersion checks that the server version is atleast
// the provided version, throws an error if not
func (dc DockerClient) CheckServerVersion(version versionutil.Version) error {
	v, err := dc.Version()
	if err != nil {
		return fmt.Errorf("error getting version: %v", err)
	}

	serverVersion, err := versionutil.ParseVersion(v.Get("Version"))
	if err != nil {
		return fmt.Errorf("error parsing version %s: %v", v.Get("Version"), err)
	}

	if serverVersion.LessThan(version) {
		return fmt.Errorf("unsupported Docker version %s, golem requires running on at least %s", serverVersion, version)
	}

	logrus.Debugf("Client connected to server with version %s", serverVersion)

	return nil
}
