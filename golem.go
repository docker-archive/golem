package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/Sirupsen/logrus"
	"github.com/docker/golem/buildutil"
	"github.com/docker/golem/clientutil"
	"github.com/docker/golem/runner"
	"github.com/docker/golem/versionutil"
)

func main() {
	name := filepath.Base(os.Args[0])
	if name == "golem_runner" {
		runnerMain()
		return
	}
	var (
		dockerBinary string
		cacheDir     string
		buildCache   string
	)
	co := clientutil.NewClientOptions()
	cm := runner.NewConfigurationManager()

	// Move Docker Specific options to separate type
	flag.StringVar(&dockerBinary, "db", "", "Docker binary to test")
	flag.StringVar(&cacheDir, "cache", "", "Cache directory")
	flag.StringVar(&buildCache, "build-cache", "", "Build cache location, if outside of default cache directory")
	// TODO: Add swarm flag and host option

	flag.Parse()

	// TODO: Allow quiet and verbose mode
	logrus.SetLevel(logrus.DebugLevel)

	if cacheDir == "" {
		td, err := ioutil.TempDir("", "build-cache-")
		if err != nil {
			logrus.Fatalf("Error creating tempdir: %v", err)
		}
		cacheDir = td
		defer os.RemoveAll(td)
	}

	if buildCache == "" {
		buildCache = filepath.Join(cacheDir, "builds")
		if err := os.MkdirAll(buildCache, 0755); err != nil {
			logrus.Fatalf("Error creating build cache directory")
		}
	}
	c := runner.CacheConfiguration{
		ImageCache: runner.NewImageCache(filepath.Join(cacheDir, "images")),
		BuildCache: buildutil.NewFSBuildCache(buildCache),
	}

	var dockerVersion *versionutil.Version
	if dockerBinary != "" {
		v, err := versionutil.BinaryVersion(dockerBinary)
		if err != nil {
			logrus.Fatalf("Error getting binary version of %s: %v", dockerBinary, err)
		}
		logrus.Debugf("Using local binary with version %s", v.String())
		if err := c.BuildCache.PutVersion(v, dockerBinary); err != nil {
			logrus.Fatalf("Error putting %s in cache as %s: %v", dockerBinary, v, err)
		}

		dockerVersion = &v
	}

	client, err := runner.NewDockerClient(co)
	if err != nil {
		logrus.Fatalf("Failed to create client: %v", err)
	}

	if dockerVersion == nil {
		v, err := client.Version()
		if err != nil {
			logrus.Fatalf("Error getting version: %v", err)
		}

		serverVersion, err := versionutil.ParseVersion(v.Get("Version"))
		if err != nil {
			logrus.Fatalf("Unexpected version value: %s", v.Get("Version"))
		}
		// TODO: Check cache here to ensure that load will not have issues
		logrus.Debugf("Using docker daemon for image export, version %s", serverVersion)

		dockerVersion = &serverVersion
	}

	// TODO(dmcgowan): Add warning when using <1.10, no content addressable identifiers

	r, err := cm.CreateRunner(*dockerVersion, c)
	if err != nil {
		logrus.Fatalf("Error creating runner: %v", err)
	}

	if err := r.Build(client); err != nil {
		logrus.Fatalf("Error building test images: %v", err)
	}

	if err := r.Run(client); err != nil {
		logrus.Fatalf("Error running tests: %v", err)
	}
}

func runnerMain() {
	var (
		command string
		dind    bool
		clean   bool
	)

	// TODO: Parse runner options
	flag.StringVar(&command, "command", "bats", "Command to run")
	flag.BoolVar(&dind, "docker", false, "Whether to run docker")
	flag.BoolVar(&clean, "clean", false, "Whether to ensure /var/lib/docker is empty")

	flag.Parse()

	// TODO: Allow quiet and verbose mode
	logrus.SetLevel(logrus.DebugLevel)

	logrus.Debugf("Runner!")

	// Check if has compose file
	composeFile := "/runner/docker-compose.yml"
	var composeCapturer runner.LogCapturer
	if _, err := os.Stat(composeFile); err == nil {
		composeCapturer = newFileCapturer("compose")
		defer composeCapturer.Close()
	} else {
		logrus.Debugf("No compose file found at %s", composeFile)
	}
	logrus.Debugf("Environment: %#v", os.Environ())

	scriptCapturer := newFileCapturer("scripts")
	defer scriptCapturer.Close()
	loadCapturer := newFileCapturer("load")
	defer loadCapturer.Close()
	daemonCapturer := newFileCapturer("daemon")
	defer daemonCapturer.Close()
	testCapturer := runner.NewConsoleLogCapturer()
	defer testCapturer.Close()

	instanceF, err := os.Open("/instance.json")
	if err != nil {
		logrus.Fatalf("Error opening instance file: %v", err)
	}

	var instanceConfig runner.RunConfiguration
	if err := json.NewDecoder(instanceF).Decode(&instanceConfig); err != nil {
		logrus.Fatalf("Error decoding instance configuration: %v", err)
	}

	suiteConfig := runner.SuiteRunnerConfiguration{
		DockerLoadLogCapturer: loadCapturer,
		DockerLogCapturer:     daemonCapturer,

		RunConfiguration: instanceConfig,
		SetupLogCapturer: scriptCapturer,
		TestCapturer:     testCapturer,

		CleanDockerGraph: clean,
		DockerInDocker:   dind,
	}

	if composeCapturer != nil {
		suiteConfig.ComposeCapturer = composeCapturer
		suiteConfig.ComposeFile = composeFile

	}

	r := runner.NewSuiteRunner(suiteConfig)

	if err := r.Setup(); err != nil {
		logrus.Fatalf("Setup error: %v", err)
	}

	runErr := r.RunTests()

	if err := r.TearDown(); err != nil {
		logrus.Errorf("TearDown error: %v", err)
	}

	if runErr != nil {
		logrus.Fatalf("Test errored: %v", runErr)
	}
}

func newFileCapturer(name string) runner.LogCapturer {
	basename := filepath.Join("/var/log/docker", name)
	lc, err := runner.NewFileLogCapturer(basename)
	if err != nil {
		logrus.Fatalf("Error creating file capturer for %s: %v", basename, err)
	}

	return lc
}
