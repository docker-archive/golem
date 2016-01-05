# Golem Integration Test Runner

The golem integration test runner is a flexible and robust way to run integration tests on top of Docker. It is designed for running tests which require complicated components such as proxies, databases, and docker. The test runner leverages docker's ability to run docker inside of a docker container and docker compose for orchastrating test components.

### Key Features
- Leverages docker compose for setting up a test
- Runs each test inside its own test container for isolation
- Customizable run configuration for testing development builds
- Log capture of each test component for failure analysis
- Parallel test execution and multi-configuration tests

### Planned Features
- Ability to run on a swarm cluster for test scaling
- Web UI for realtime test monitoring and log analysis

### Goals
- Optimized for test driven development. Tests are able to leverage a cache to avoid rebuilding components during test setup.
- Easily fit into CI workflow.
- Handle complicated matrix testing.

## Configuration
Golem is configured through toml files (default named "golem.conf") in the directory containing a test suite.
Each configuration file may specify multiple suite configuration.

### Configuration example

```
[[suite]]
  # name is used to set the name of this suite, if none is set here then the name
  # should be set by the runner configuration or using the directory name
  name = "registry"

  # dind (or "Docker in Docker") is used to run a docker daemon inside the test container. This will
  # always be set if docker compose is used.
  dind=true

  # images which should exist in the test container
  # automatically set dind to true
  images=[ "nginx:1.9", "golang:1.4", "hello-world:latest" ]

  [[suite.pretest]]
    command="/bin/sh ./install_certs.sh localregistry"

  [[suite.testrunner]]
    command="bats -t ."
    format="tap"
    env=["TEST_REPO=hello-world", "TEST_TAG=latest", "TEST_USER=testuser", "TEST_PASSWORD=passpassword", "TEST_REGISTRY=localregistry", "TEST_SKIP_PULL=true"]

  # customimage allow runtime selection of an image inside the container
  # automatically set dind to true
  [[suite.customimage]]
    # tag is the tag that will exist for the image inside the container
    tag="golem-distribution:latest"
    # default is the default image to use from docker instance which
    # is building the golem test containers
    default="registry:2.2.1"
  [[suite.customimage]]
    tag="golem-registry:latest"
    default="registry:0.9.1"

```

