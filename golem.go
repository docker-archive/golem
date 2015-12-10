package main

import (
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/Sirupsen/logrus"
	"github.com/bugsnag/osext"
	"github.com/dmcgowan/golem/buildutil"
	"github.com/dmcgowan/golem/clientutil"
	"github.com/dmcgowan/golem/versionutil"
	"github.com/docker/distribution/reference"
)

func main() {
	name := filepath.Base(os.Args[0])
	if name == "golem_runner" {
		runnerMain()
		return
	}
	var (
		distributionImage   string
		legacyRegistryImage string
		dockerVersion       string
		dockerBinary        string
		loadDockerVersion   string
		cacheDir            string
		buildCache          string
		testDir             string
	)
	co := clientutil.NewClientOptions()

	// Move Docker Specific options to separate type
	flag.StringVar(&distributionImage, "-registry", "registry:2.1.1", "Distribution image")
	flag.StringVar(&legacyRegistryImage, "-legacy-registry", "registry:0.9.1", "Registry V1 image")
	flag.StringVar(&dockerVersion, "dv", "1.9.0", "Docker version to test")
	flag.StringVar(&dockerBinary, "db", "", "Docker binary to test")
	flag.StringVar(&loadDockerVersion, "load-version", "1.8.3", "Previous Docker version (for upgrade from testing)")
	flag.StringVar(&cacheDir, "c", "", "Cache directory")
	flag.StringVar(&buildCache, "bc", "", "Build cache location, if outside of default cache directory")
	flag.StringVar(&testDir, "d", "", "Directory containing tests (default: current working directory)")

	flag.Parse()

	// TODO: Allow quiet and verbose mode
	logrus.SetLevel(logrus.DebugLevel)

	if testDir == "" {
		var err error
		testDir, err = os.Getwd()
		if err != nil {
			logrus.Fatalf("Unable to get working directory: %v", err)
		}
	}
	logrus.Debugf("Test directory: %s", testDir)

	// TODO: Support non-linux by downloading and replacing executable path
	executablePath, err := osext.Executable()
	if err != nil {
		logrus.Fatalf("Error getting path to executable: %s", err)
	}

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
	c := CacheConfiguration{
		ImageCache: &ImageCache{
			root: filepath.Join(cacheDir, "images"),
		},
		BuildCache: buildutil.NewFSBuildCache(buildCache),
	}

	pv, err := versionutil.ParseVersion(loadDockerVersion)
	if err != nil {
		logrus.Fatalf("Invalid docker version %q: %v", loadDockerVersion, err)
	}
	// TODO: If docker binary, get version, put in cache, set dockerVersion
	var dv versionutil.Version
	if dockerBinary != "" {
		v, err := versionutil.BinaryVersion(dockerBinary)
		if err != nil {
			logrus.Fatalf("Error getting binary version of %s: %v", dockerBinary, err)
		}
		logrus.Debugf("Using local binary with version %s", v.String())
		if err := c.BuildCache.PutVersion(v, dockerBinary); err != nil {
			logrus.Fatalf("Error putting %s in cache as %s: %v", dockerBinary, v, err)
		}
		dv = v
	} else {
		dv, err = versionutil.ParseVersion(dockerVersion)
		if err != nil {
			logrus.Fatalf("Invalid docker version %q: %v", dockerVersion, err)
		}
	}

	client, err := NewDockerClient(co)
	if err != nil {
		logrus.Fatalf("Failed to create client: %v", err)
	}

	v, err := client.Version()
	if err != nil {
		logrus.Fatalf("Error getting version: %v", err)
	}
	serverVersion, err := versionutil.ParseVersion(v.Get("Version"))
	if err != nil {
		logrus.Fatalf("Unexpected version value: %s", v.Get("Version"))
	}
	if required := versionutil.StaticVersion(1, 9, 0); serverVersion.LessThan(required) {
		logrus.Fatalf("Requires at least docker version %s, have %s", required, serverVersion)
	}

	logrus.Debugf("Running %s", executablePath)

	baseConf := BaseImageConfiguration{
		Base: ensureTagged("dmcgowan/golem:latest"),
		ExtraImages: []reference.NamedTagged{
			ensureTagged("nginx:1.9"),
			ensureTagged("golang:1.4"),
			ensureTagged("hello-world:latest"),
		},
		CustomImages: []CustomImage{
			{
				Source: distributionImage,
				Target: ensureTagged("golem-distribution:latest"),
			},
			{
				Source: legacyRegistryImage,
				Target: ensureTagged("golem-registry:latest"),
			},
		},
		DockerLoadVersion: pv,
		DockerVersion:     dv,
	}

	runnerConfig := RunnerConfiguration{
		Suites: []SuiteConfiguration{
			{
				Name:           "registry",
				Path:           testDir,
				BaseImage:      baseConf,
				DockerInDocker: true,
				Instances: []RunConfiguration{
					{
						Name: "default",
					},
				},
			},
		},
		ExecutableName: "golem_runner",
		ExecutablePath: executablePath,
	}

	runner := NewRunner(runnerConfig, c)

	if err := runner.Build(client); err != nil {
		logrus.Fatalf("Error building test images: %v", err)
	}

	if err := runner.Run(client); err != nil {
		logrus.Fatalf("Error running tests: %v", err)
	}
}

func runnerMain() {
	// TODO: Parse runner options
	flag.Parse()

	// TODO: Allow quiet and verbose mode
	logrus.SetLevel(logrus.DebugLevel)

	logrus.Debugf("Runner!")

	scriptCapturer := newFileCapturer("scripts")
	defer scriptCapturer.Close()
	loadCapturer := newFileCapturer("load")
	defer loadCapturer.Close()
	daemonCapturer := newFileCapturer("daemon")
	defer daemonCapturer.Close()
	testCapturer := NewConsoleLogCapturer()
	defer testCapturer.Close()
	composeCapturer := newFileCapturer("compose")
	defer composeCapturer.Close()

	suiteConfig := SuiteRunnerConfiguration{
		DockerInDocker:        true,
		CleanDockerGraph:      false,
		DockerLoadLogCapturer: loadCapturer,
		DockerLogCapturer:     daemonCapturer,
		SetupScripts: [][]string{
			{"/bin/sh", "/runner/install_certs.sh", "localregistry"},
		},
		SetupLogCapturer: scriptCapturer,
		ComposeFile:      "/runner/docker-compose.yml",
		ComposeCapturer:  composeCapturer,

		TestCapturer: testCapturer,
		TestCommand:  "bats",
		TestArgs:     []string{"-t", "registry"},
		TestEnv: []string{
			"TEST_REPO=hello-world",
			"TEST_TAG=latest",
			"TEST_USER=testuser",
			"TEST_PASSWORD=passpassword",
			"TEST_REGISTRY=localregistry",
			"TEST_SKIP_PULL=true",
		},
	}

	runner := NewSuiteRunner(suiteConfig)

	if err := runner.Setup(); err != nil {
		logrus.Fatalf("Setup error: %v", err)
	}

	runErr := runner.RunTests()

	if err := runner.TearDown(); err != nil {
		logrus.Errorf("TearDown error: %v", err)
	}

	if runErr != nil {
		logrus.Fatalf("Test errored: %v", runErr)
	}

}

func newFileCapturer(name string) LogCapturer {
	basename := filepath.Join("/var/log/docker", name)
	lc, err := NewFileLogCapturer(basename)
	if err != nil {
		logrus.Fatalf("Error creating file capturer for %s: %v", basename, err)
	}

	return lc
}
