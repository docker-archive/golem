package main

import (
	"io"
	"net/http"
	"path/filepath"

	"github.com/dmcgowan/golem/clientutil"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/jlhawn/dockramp/build"
)

type DockerClient struct {
	*dockerclient.Client
	options *clientutil.ClientOptions
}

func NewDockerClient(co *clientutil.ClientOptions) (DockerClient, error) {
	client, err := dockerclient.NewClient(co.DaemonURL())
	if err != nil {
		return DockerClient{}, err
	}
	if tlsConfig := co.TLSConfig(); tlsConfig != nil {
		client.TLSConfig = tlsConfig
		client.HTTPClient.Transport.(*http.Transport).TLSClientConfig = tlsConfig
	}
	return DockerClient{
		Client:  client,
		options: co,
	}, nil
}

func (dc DockerClient) createRequest(method, path string, body io.Reader) (*http.Request, error) {
	fullPath := filepath.Join(dc.options.DaemonURL(), path)
	return http.NewRequest(method, fullPath, body)
}

func (dc DockerClient) do(req *http.Request) (*http.Response, error) {
	return dc.Client.HTTPClient.Do(req)
}

func (dc DockerClient) NewBuilder(contextDirectory, dockerfilePath, repoTag string) (*build.Builder, error) {
	return build.NewBuilder(dc.options.DaemonURL(), dc.options.TLSConfig(), contextDirectory, dockerfilePath, repoTag)
}
