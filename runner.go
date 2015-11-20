package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/bugsnag/osext"
	"github.com/dmcgowan/golem/buildutil"
	"github.com/dmcgowan/golem/clientutil"
	"github.com/dmcgowan/golem/versionutil"
	"github.com/docker/distribution/reference"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/jlhawn/dockramp/build"
)

func main() {
	name := filepath.Base(os.Args[0])
	if name == "golem_runner" {
		runnerMain()
		return
	}
	var (
		distributionImage     string
		legacyRegistryImage   string
		notaryImage           string
		dockerVersion         string
		previousDockerVersion string
		buildCache            string
		testDir               string
	)
	co := clientutil.NewClientOptions()

	// Move Docker Specific options to separate type
	flag.StringVar(&distributionImage, "-registry", "registry:2.1.1", "Distribution image")
	flag.StringVar(&legacyRegistryImage, "-legacy-registry", "registry:0.9.1", "Registry V1 image")
	flag.StringVar(&notaryImage, "-notary-image", "distribution/notary_notaryserver:0.1.4", "Notary Server image")
	flag.StringVar(&dockerVersion, "dv", "1.9.0", "Docker version to test")
	flag.StringVar(&previousDockerVersion, "pv", "1.8.3", "Previous Docker version (for upgrade from testing)")
	flag.StringVar(&buildCache, "bc", "", "Build cache location")
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

	// TODO: Look for golem.conf in testDir

	// TODO: Support non-linux by downloading and replacing executable path
	executablePath, err := osext.Executable()
	if err != nil {
		logrus.Fatalf("Error getting path to executable: %s", err)
	}

	dv, err := versionutil.ParseVersion(dockerVersion)
	if err != nil {
		logrus.Fatalf("Invalid docker version %q: %v", dockerVersion, err)
	}
	pv, err := versionutil.ParseVersion(previousDockerVersion)
	if err != nil {
		logrus.Fatalf("Invalid docker version %q: %v", previousDockerVersion, err)
	}

	images := []reference.NamedTagged{
		ensureTagged(distributionImage),
		ensureTagged(legacyRegistryImage),
		ensureTagged(notaryImage),
	}
	if buildCache == "" {
		td, err := ioutil.TempDir("", "build-cache-")
		if err != nil {
			logrus.Fatalf("Error creating tempdir: %v", err)
		}
		buildCache = td
		defer os.RemoveAll(td)
	}
	c := buildutil.NewFSBuildCache(buildCache)

	client, err := dockerclient.NewClient(co.DaemonURL())
	if err != nil {
		logrus.Fatalf("Failed to create client: %v", err)
	}
	if tlsConfig := co.TLSConfig(); tlsConfig != nil {
		client.TLSConfig = tlsConfig
		client.HTTPClient.Transport.(*http.Transport).TLSClientConfig = tlsConfig
	}

	v, err := client.Version()
	if err != nil {
		logrus.Fatal("Error getting version")
	}
	serverVersion, err := versionutil.ParseVersion(v.Get("Version"))
	if err != nil {
		logrus.Fatalf("Unexpected version value: %s", v.Get("Version"))
	}
	if required := versionutil.StaticVersion(1, 9, 0); serverVersion.LessThan(required) {
		logrus.Fatalf("Requires at least docker version %s, have %s", required, serverVersion)
	}

	logrus.Debugf("Running %s", executablePath)

	// Create temp build directory
	td, err := ioutil.TempDir("", "golem-")
	if err != nil {
		logrus.Fatalf("Unable to create tempdir: %v", err)
	}
	defer os.RemoveAll(td)

	// Create Dockerfile in tempDir
	df, err := os.OpenFile(filepath.Join(td, "Dockerfile"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logrus.Fatalf("Error creating docker file: %v", err)
	}
	defer df.Close()

	fmt.Fprintf(df, "FROM %s\n", "dmcgowan/golem:latest")

	// Add base Docker images to load
	for _, ref := range images {
		ensureImage(client, ref)
	}

	// TODO: Use name derived of hash of current docker version + image ids
	it, err := os.OpenFile(filepath.Join(td, "images.tar"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logrus.Fatalf("Error creating images tar file: %v", err)
	}
	if err := saveImages(client, it, images); err != nil {
		it.Close()
		logrus.Fatalf("Error saving images: %v", err)
	}
	if err := it.Close(); err != nil {
		logrus.Fatalf("Error closing image tar: %v", err)
	}
	fmt.Fprintln(df, "COPY ./images.tar /images.tar")

	// Add Docker Binaries (docker test specific)
	c.InstallVersion(dv, filepath.Join(td, "docker"))
	c.InstallVersion(pv, filepath.Join(td, "docker-previous"))
	fmt.Fprintln(df, "COPY ./docker /usr/bin/docker")
	fmt.Fprintln(df, "COPY ./docker-previous /usr/bin/docker-previous")
	// TODO: Handle init files

	buildutil.CopyFile(executablePath, filepath.Join(td, "golem_runner"), 0755)
	fmt.Fprintln(df, "COPY ./golem_runner /usr/bin/golem_runner")

	if err := df.Close(); err != nil {
		logrus.Fatalf("Error closing dockerfile: %v", err)
	}

	builder, err := build.NewBuilder(co.DaemonURL(), co.TLSConfig(), td, "", "golemrunner:latest")
	if err != nil {
		logrus.Fatalf("Error creating builder: %v", err)
	}

	if err := builder.Run(); err != nil {
		logrus.Fatalf("Error building: %v", err)
	}

	// TODO: Start for each test, or setup use libcompose

	// Start test container
	hc := &dockerclient.HostConfig{
		Binds:      []string{fmt.Sprintf("%s:/runner:ro", testDir)},
		Privileged: true,
	}
	env := []string{}
	if storageDriver := os.Getenv("DOCKER_GRAPHDRIVER"); storageDriver != "" {
		env = append(env, "DOCKER_GRAPHDRIVER="+storageDriver)
	}

	cc := dockerclient.CreateContainerOptions{
		Config: &dockerclient.Config{
			Image: "golemrunner:latest",
			Cmd:   []string{"/usr/bin/golem_runner"},
			//Mounts: []dockerclient.Mount{
			//	{
			//		Source:      testDir,
			//		Destination: "/runner",
			//		Mode:        "ro",
			//		RW:          false,
			//	},
			//},
			Env:        env,
			WorkingDir: "/runner",
		},
		HostConfig: hc,
	}
	container, err := client.CreateContainer(cc)
	if err != nil {
		logrus.Fatalf("Error creating container: %v", err)
	}

	if err := client.StartContainer(container.ID, hc); err != nil {
		logrus.Fatalf("Error starting container: %v", err)
	}

	attachOptions := dockerclient.AttachToContainerOptions{
		Container:    container.ID,
		OutputStream: os.Stdout,
		ErrorStream:  os.Stderr,
		Logs:         true,
		Stream:       true,
		Stdout:       true,
		Stderr:       true,
	}
	if err := client.AttachToContainer(attachOptions); err != nil {
		logrus.Fatalf("Error attaching to container: %v", err)
	}

}

func ensureTagged(image string) reference.NamedTagged {
	ref, err := reference.Parse(image)
	if err != nil {
		logrus.Fatalf("Invalid reference %q: %v", image, err)
	}
	named, ok := ref.(reference.NamedTagged)
	if !ok {
		logrus.Fatalf("Image reference must have name and tag: %s", image)
	}

	return named
}

func ensureImage(client *dockerclient.Client, ref reference.NamedTagged) {
	// TODO: Use ID to generate hash of all images for caching exports
	_, err := client.InspectImage(ref.String())
	if err == nil {
		logrus.Debugf("Image found locally %s", ref.String())
		return
	}
	if err != dockerclient.ErrNoSuchImage {
		logrus.Fatalf("Error inspecting image %q: %v", ref, err)
	}

	logrus.Infof("Pulling image %s", ref.String())

	pullOptions := dockerclient.PullImageOptions{
		Repository:   ref.Name(),
		Tag:          ref.Tag(),
		OutputStream: os.Stdout,
	}
	if err := client.PullImage(pullOptions, dockerclient.AuthConfiguration{}); err != nil {
		logrus.Fatalf("Error pulling image %q: %v", ref, err)
	}
}

// Save images
func saveImages(client *dockerclient.Client, out io.Writer, images []reference.NamedTagged) error {
	var names []string
	for _, ref := range images {
		names = append(names, ref.String())
	}
	logrus.Debugf("Exporting images %s", strings.Join(names, " "))
	ec := dockerclient.ExportImagesOptions{
		Names:        names,
		OutputStream: out,
	}
	return client.ExportImages(ec)
}

func runnerMain() {
	logrus.Print("Runner!")

	// TODO: Parse runner options
	flag.Parse()
	// TODO: Load options from test directory

	pc, pk, err := StartDaemon("/usr/bin/docker-previous")
	if err != nil {
		logrus.Fatalf("Error starting daemon: %s", err)
	}

	logrus.Printf("Loading images at /images.tar")
	ti, err := os.Open("/images.tar")
	if err != nil {
		logrus.Fatalf("Unable to open /images.tar: %v", err)
	}
	defer ti.Close()
	loadOptions := dockerclient.LoadImageOptions{
		InputStream: ti,
	}
	if err := pc.LoadImage(loadOptions); err != nil {
		logrus.Fatalf("Unable to load images: %v", err)
	}

	logrus.Printf("Stopping daemon")
	if err := pk(); err != nil {
		logrus.Fatalf("Error killing daemon %v", err)
	}

	logrus.Printf("Running pre-test scripts")
	if err := RunScript("/bin/sh", "./install_certs.sh", "localregistry"); err != nil {
		logrus.Fatalf("Error running pre-test script: %v", err)
	}

	logrus.Printf("Starting daemon")
	_, k, err := StartDaemon("/usr/bin/docker")
	if err != nil {
		logrus.Fatalf("Error starting daemon: %s", err)
	}

	logrus.Printf("TODO: Run tests")

	testRunner := exec.Command("./test_runner.sh", "registry")
	testRunner.Stdout = os.Stdout
	testRunner.Stderr = os.Stderr
	if err := testRunner.Start(); err != nil {
		logrus.Infof("Error starting testprocess: %s", err)
	} else {
		if err := testRunner.Wait(); err != nil {
			logrus.Infof("Testing process failed: %s", err)
		}
	}

	if err := k(); err != nil {
		logrus.Fatalf("Error killing daemon %v", err)
	}
}

// RunScript rungs the script command attaching
// results to stdout and stdout
func RunScript(script string, args ...string) error {
	cmd := exec.Command(script, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start script: %s", err)
	}
	return cmd.Wait()
}

// StartDaemon starts a daemon using the provided binary returning
// a client to the binary, a close function, and error.
func StartDaemon(binary string) (*dockerclient.Client, func() error, error) {
	// Get Docker version of process
	previousVersion, err := versionutil.BinaryVersion(binary)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get binary version: %s", err)
	}

	logrus.Printf("Starting daemon with %s", binary)
	binaryArgs := []string{}
	if previousVersion.LessThan(versionutil.StaticVersion(1, 8, 0)) {
		binaryArgs = append(binaryArgs, "--daemon")
	} else {
		binaryArgs = append(binaryArgs, "daemon")
	}
	binaryArgs = append(binaryArgs, "--log-level=debug")
	if storageDriver := os.Getenv("DOCKER_GRAPHDRIVER"); storageDriver != "" {
		binaryArgs = append(binaryArgs, "--storage-driver="+storageDriver)
	}
	cmd := exec.Command("/usr/bin/docker-previous", binaryArgs...)
	// TODO: Capture
	//cmd.Stdout = os.Stdout
	//cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("could not start daemon: %s", err)
	}

	logrus.Debugf("Waiting for daemon to start")
	time.Sleep(2 * time.Second)

	client, err := dockerclient.NewClientFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("could not initialize client: %s", err)
	}

	// Wait for it to start
	for i := 0; ; i++ {
		v, err := client.Version()
		if err == nil {
			logrus.Printf("Established connection to daemon with version %s", v.Get("Version"))
			break
		}
		if i >= 10 {
			logrus.Fatalf("Failed to establish connection to daemon, check logs, quitting")
		}
		time.Sleep(time.Second)
	}

	return client, cmd.Process.Kill, nil
}
