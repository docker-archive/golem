package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/bugsnag/osext"
	"github.com/dmcgowan/golem/buildutil"
	"github.com/dmcgowan/golem/clientutil"
	"github.com/dmcgowan/golem/versionutil"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/termie/go-shutil"
)

const (
	TarLib      = false
	HashVersion = "1"
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
		cacheDir              string
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

	conf := BaseImageConfiguration{
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
			{
				Source: notaryImage,
				Target: ensureTagged("golem-notary:latest"),
			},
		},
		DockerLoadVersion: pv,
		DockerVersion:     dv,
	}
	baseImage, err := BuildBaseImage(client, conf, c)
	if err != nil {
		logrus.Fatalf("failure building base image: %v", err)
	}

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

	fmt.Fprintf(df, "FROM %s\n", baseImage)

	// TODO: Move to base image
	buildutil.CopyFile(executablePath, filepath.Join(td, "golem_runner"), 0755)
	fmt.Fprintln(df, "COPY ./golem_runner /usr/bin/golem_runner")

	logrus.Debugf("Copying %s to %s", testDir, filepath.Join(td, "runner"))
	if err := shutil.CopyTree(testDir, filepath.Join(td, "runner"), nil); err != nil {
		logrus.Fatalf("Error copying test directory: %v", err)
	}
	//logrus.Debugf("Symlinking %s to %s", testDir, filepath.Join(td, "runner"))
	//if err := os.Symlink(testDir, filepath.Join(td, "runner")); err != nil {
	//	logrus.Fatalf("Error closing dockerfile: %v", err)
	//}
	fmt.Fprintln(df, "COPY ./runner/ /runner")

	if err := df.Close(); err != nil {
		logrus.Fatalf("Error closing dockerfile: %v", err)
	}

	builder, err := client.NewBuilder(td, "", "golemrunner:latest")
	if err != nil {
		logrus.Fatalf("Error creating builder: %v", err)
	}

	if err := builder.Run(); err != nil {
		logrus.Fatalf("Error building: %v", err)
	}

	// TODO: Start for each test, or setup use libcompose

	// Start test container
	// TODO: Derive these values from config
	nocache := false
	contName := "golem-test-1"
	volumeName := contName + "-graph"
	cont, err := client.InspectContainer(contName)
	if err == nil {
		removeOptions := dockerclient.RemoveContainerOptions{
			ID:            cont.ID,
			RemoveVolumes: true,
		}
		if err := client.RemoveContainer(removeOptions); err != nil {
			logrus.Fatalf("Error removing existing container %s: %v", contName, err)
		}
	}

	vol, err := client.InspectVolume(volumeName)
	if err == nil {
		if nocache {
			if err := client.RemoveVolume(vol.Name); err != nil {
				logrus.Fatalf("Error removing volume %s: %v", vol.Name, err)
			}
			vol = nil
		}
	}

	if vol == nil {
		createOptions := dockerclient.CreateVolumeOptions{
			Name:   volumeName,
			Driver: "local",
		}
		vol, err = client.CreateVolume(createOptions)
		if err != nil {
			logrus.Fatalf("Error creating volume: %v", err)
		}
	}

	// TODO: Defer container and volume cleanup

	logrus.Debugf("Mounting %s to %s", vol.Mountpoint, "/var/lib/docker")
	hc := &dockerclient.HostConfig{
		Binds:      []string{fmt.Sprintf("%s:/var/lib/docker", vol.Mountpoint)},
		Privileged: true,
	}
	env := []string{"DOCKER_GRAPHDRIVER=" + getGraphDriver()}
	cc := dockerclient.CreateContainerOptions{
		Name: contName,
		Config: &dockerclient.Config{
			Image:      "golemrunner:latest",
			Cmd:        []string{"/usr/bin/golem_runner"},
			Env:        env,
			WorkingDir: "/runner",
			Volumes: map[string]struct{}{
				"/var/log/docker": struct{}{},
			},
			VolumeDriver: "local",
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

func getGraphDriver() string {
	d := os.Getenv("DOCKER_GRAPHDRIVER")
	switch d {
	case "":
		return "overlay"
	default:
		return d
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

func ensureImage(client DockerClient, image string) (string, error) {
	info, err := client.InspectImage(image)
	if err == nil {
		logrus.Debugf("Image found locally %s", image)
		return info.ID, nil
	}
	if err != dockerclient.ErrNoSuchImage {
		logrus.Errorf("Error inspecting image %q: %v", image, err)
		return "", err
	}

	// Image must be tagged reference if it does not exist
	ref, err := reference.Parse(image)
	if err != nil {
		logrus.Debugf("Image is not valid reference %q: %v", image, err)
	}
	tagged, ok := ref.(reference.NamedTagged)
	if !ok {
		logrus.Debugf("Tagged reference required %q", image)
		return "", errors.New("invalid reference, tag needed")
	}

	logrus.Infof("Pulling image %s", tagged.String())

	pullOptions := dockerclient.PullImageOptions{
		Repository:   tagged.Name(),
		Tag:          tagged.Tag(),
		OutputStream: os.Stdout,
	}
	if err := client.PullImage(pullOptions, dockerclient.AuthConfiguration{}); err != nil {
		logrus.Errorf("Error pulling image %q: %v", tagged.String(), err)
		return "", err
	}
	// TODO: Get pulled digest and inspect by digest
	info, err = client.InspectImage(tagged.String())
	if err != nil {
		return "", nil
	}

	return info.ID, nil
}

func saveImage(client DockerClient, filename, imgID string) error {
	// TODO: must not exist
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating image tar file: %v", err)
	}
	defer f.Close()
	logrus.Debugf("Exporting image %s to %s", imgID, filename)
	ec := dockerclient.ExportImageOptions{
		Name:         imgID,
		OutputStream: f,
	}
	return client.ExportImage(ec)
}

func saveTagMap(filename string, tags []tag) error {
	m := map[string][]string{}
	for _, t := range tags {
		m[t.Image] = append(m[t.Image], t.Tag.String())
	}

	mf, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error opening images.json file: %v", err)
	}
	defer mf.Close()

	if err := json.NewEncoder(mf).Encode(m); err != nil {
		return fmt.Errorf("error encoding tag map: %v")
	}

	return nil
}

func runnerMain() {
	logrus.Print("Runner!")

	// TODO: Parse runner options
	flag.Parse()

	// Check if empty
	info, err := ioutil.ReadDir("/var/lib/docker")
	if err != nil {
		logrus.Fatalf("Error reading /var/lib/docker: %v", err)
	}

	if len(info) != 0 {
		// TODO: Check whether or not this is expected to be clean
		logrus.Infof("/var/lib/docker is not clean")
	}

	scriptCapturer := newFileCapturer("scripts")
	defer scriptCapturer.Close()
	loadCapturer := newFileCapturer("load")
	defer loadCapturer.Close()
	daemonCapturer := newFileCapturer("daemon")
	defer daemonCapturer.Close()
	testCapturer := NewConsoleLogCapturer()
	defer testCapturer.Close()

	// Load tag map
	logrus.Printf("Loading docker images")
	pc, pk, err := StartDaemon("/usr/bin/docker-load", loadCapturer)
	if err != nil {
		logrus.Fatalf("Error starting daemon: %v", err)
	}

	// TODO: Remove all containers
	containers, err := pc.ListContainers(dockerclient.ListContainersOptions{All: true})
	if err != nil {
		logrus.Fatalf("Error listing containers: %v", err)
	}
	for _, container := range containers {
		logrus.Printf("Removing container %s", container.ID)
		removeOptions := dockerclient.RemoveContainerOptions{
			ID:            container.ID,
			RemoveVolumes: true,
			Force:         true,
		}
		if err := pc.RemoveContainer(removeOptions); err != nil {
			logrus.Fatalf("Error removing container: %v", err)
		}
	}

	if err := syncImages(pc, "/images"); err != nil {
		logrus.Fatalf("Error syncing images: %v", err)
	}

	if err := RunScript(scriptCapturer, "/usr/bin/docker-load", "images"); err != nil {
		logrus.Fatalf("Error running docker images")
	}

	logrus.Printf("Stopping daemon")
	if err := pk(); err != nil {
		logrus.Fatalf("Error killing daemon %v", err)
	}

	logrus.Printf("Running pre-test scripts")
	if err := RunScript(scriptCapturer, "/bin/sh", "./install_certs.sh", "localregistry"); err != nil {
		logrus.Fatalf("Error running pre-test script: %v", err)
	}

	logrus.Printf("Starting daemon")
	client, k, err := StartDaemon("/usr/bin/docker", daemonCapturer)
	if err != nil {
		logrus.Fatalf("Error starting daemon: %s", err)
	}

	logrus.Printf("Build compose images")
	if err := RunScript(scriptCapturer, "docker-compose", "build", "--no-cache"); err != nil {
		logrus.Fatalf("Error running docker compose build: %v", err)
	}

	logrus.Printf("Dump existing images")
	images, err := client.ListImages(dockerclient.ListImagesOptions{})
	if err != nil {
		logrus.Fatalf("Unable to list images: %v", err)
	}
	for _, image := range images {
		logrus.Printf("Found image %s: %#v", image.ID, image.RepoTags)
	}

	logrus.Printf("TODO: Run tests")
	// TODO: If docker compose configured

	if err := RunScript(scriptCapturer, "docker-compose", "up", "-d"); err != nil {
		logrus.Fatalf("Error running docker compose up: %v", err)
	}

	go func() {
		composeCapturer := newFileCapturer("compose")
		defer composeCapturer.Close()
		logrus.Debugf("Listening for logs")
		if err := RunScript(composeCapturer, "docker-compose", "logs"); err != nil {
			logrus.Fatalf("Error running docker compose logs: %v", err)
		}
	}()

	if err := RunScript(testCapturer, "./test_runner.sh", "registry"); err != nil {
		logrus.Fatalf("Error running test script: %v", err)
	}

	if err := k(); err != nil {
		logrus.Fatalf("Error killing daemon %v", err)
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

// RunScript rungs the script command attaching
// results to stdout and stdout
func RunScript(lc LogCapturer, script string, args ...string) error {
	cmd := exec.Command(script, args...)
	cmd.Stdout = lc.Stdout()
	cmd.Stderr = lc.Stderr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start script: %s", err)
	}
	return cmd.Wait()
}

// StartDaemon starts a daemon using the provided binary returning
// a client to the binary, a close function, and error.
func StartDaemon(binary string, lc LogCapturer) (*dockerclient.Client, func() error, error) {
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
	binaryArgs = append(binaryArgs, "--storage-driver="+getGraphDriver())
	cmd := exec.Command(binary, binaryArgs...)
	cmd.Stdout = lc.Stdout()
	cmd.Stderr = lc.Stderr()
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

type tagMap map[string][]string

func listDiff(l1, l2 []string) ([]string, []string) {
	sort.Strings(l1)
	sort.Strings(l2)

	removed := []string{}
	added := []string{}

	i1 := 0
	i2 := 0
	for i1 < len(l1) && i2 < len(l2) {
		r := strings.Compare(l1[i1], l2[i2])
		switch {
		case r < 0:
			removed = append(removed, l1[i1])
			i1++
		case r > 0:
			added = append(added, l2[i2])
			i2++
		default:
			i1++
			i2++
		}
	}
	for i1 < len(l1) {
		removed = append(removed, l1[i1])
		i1++
	}
	for i2 < len(l2) {
		added = append(added, l2[i2])
		i2++
	}

	return removed, added
}

func syncImages(client *dockerclient.Client, imageRoot string) error {
	logrus.Printf("Syncing images from %s", imageRoot)
	f, err := os.Open(filepath.Join(imageRoot, "images.json"))
	if err != nil {
		return fmt.Errorf("error opening image json file: %v", err)
	}
	defer f.Close()

	var m tagMap
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return fmt.Errorf("error decoding images json: %v", err)
	}

	allTags := map[string]struct{}{}
	neededImages := map[string]struct{}{}
	for imageID, tags := range m {
		neededImages[imageID] = struct{}{}
		for _, t := range tags {
			allTags[t] = struct{}{}
		}
	}

	images, err := client.ListImages(dockerclient.ListImagesOptions{})
	if err != nil {
		return fmt.Errorf("error listing images: %v", err)
	}

	for _, img := range images {
		expectedTags, ok := m[img.ID]
		if ok {
			delete(neededImages, img.ID)

			repoTags := filterRepoTags(img.RepoTags)
			logrus.Printf("Tags for %s: %#v", img.ID, repoTags)

			// Sync tags for image ID
			removedTags, addedTags := listDiff(repoTags, expectedTags)
			for _, t := range addedTags {
				if err := tagImage(client, img.ID, t); err != nil {
					return err
				}
			}
			for _, t := range removedTags {
				// Check if this image tag conflicts with an expected
				// tag, in which case force tag will update
				if _, ok := allTags[t]; !ok {
					logrus.Debugf("Removing tag %s", t)
					if err := client.RemoveImage(t); err != nil {
						return fmt.Errorf("error removing tag %s: %v", t, err)
					}
				}
			}
		} else {
			removeOptions := dockerclient.RemoveImageOptions{
				Force: true,
			}
			if err := client.RemoveImageExtended(img.ID, removeOptions); err != nil {
				return fmt.Errorf("error moving image %s: %v", img.ID, err)
			}
		}

	}

	for imageID := range neededImages {
		tags, ok := m[imageID]
		if !ok {
			return fmt.Errorf("missing image %s in tag map", imageID)
		}
		_, err := client.InspectImage(imageID)
		if err != nil {
			tf, err := os.Open(filepath.Join(imageRoot, imageID+".tar"))
			if err != nil {
				return fmt.Errorf("error opening image tar %s: %v", imageID, err)
			}
			defer tf.Close()
			loadOptions := dockerclient.LoadImageOptions{
				InputStream: tf,
			}
			if err := client.LoadImage(loadOptions); err != nil {
				return fmt.Errorf("error loading image %s: %v", imageID, err)
			}
		}
		for _, t := range tags {
			if err := tagImage(client, imageID, t); err != nil {
				return err
			}
		}
	}

	return nil
}

func filterRepoTags(tags []string) []string {
	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != "<none>" && tag != "<none>:<none>" {
			filtered = append(filtered, tag)
		}
	}
	return filtered
}

func tagImage(client *dockerclient.Client, img, tag string) error {
	ref, err := reference.Parse(tag)
	if err != nil {
		return fmt.Errorf("invalid tag %s: %v", tag, err)
	}
	namedTagged, ok := ref.(reference.NamedTagged)
	if !ok {
		return fmt.Errorf("expecting named tagged reference: %s", tag)
	}
	tagOptions := dockerclient.TagImageOptions{
		Repo:  namedTagged.Name(),
		Tag:   namedTagged.Tag(),
		Force: true,
	}
	if err := client.TagImage(img, tagOptions); err != nil {
		return fmt.Errorf("error tagging image %s as %s: %v", img, tag, err)
	}

	return nil
}

type tag struct {
	Tag   reference.NamedTagged
	Image string
}

type ImageCache struct {
	root string
}

func (ic *ImageCache) imageFile(dgst digest.Digest) string {
	return filepath.Join(ic.root, dgst.Algorithm().String(), dgst.Hex())
}

func (ic *ImageCache) GetImage(dgst digest.Digest) (string, error) {
	f, err := os.Open(ic.imageFile(dgst))
	if err != nil {
		// TODO: Detect does not exist error and return const error
		return "", err
	}
	defer f.Close()

	b, err := ioutil.ReadAll(f)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(b)), nil
}

func (ic *ImageCache) SaveImage(dgst digest.Digest, id string) error {
	fp := ic.imageFile(dgst)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return err
	}
	f, err := os.Create(fp)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s", id); err != nil {
		return err
	}
	logrus.Debugf("Saved %s->%s at %s", dgst, id, fp)
	return nil
}

type CustomImage struct {
	Source string
	Target reference.NamedTagged
}

type CacheConfiguration struct {
	ImageCache *ImageCache
	BuildCache buildutil.BuildCache
}

type BaseImageConfiguration struct {
	Base         reference.Named
	ExtraImages  []reference.NamedTagged
	CustomImages []CustomImage

	DockerLoadVersion versionutil.Version
	DockerVersion     versionutil.Version

	// Images (References and Targets)
	// Image index (keyed by hash of images + versions)
	// Docker load version
	// Docker version
	// Runner binary
}

// BuildBaseImage builds a base image using the given configuration
// and returns an image id for the given image
func BuildBaseImage(client DockerClient, conf BaseImageConfiguration, c CacheConfiguration) (string, error) {
	tags := []tag{}
	images := []string{}
	for _, ref := range conf.ExtraImages {
		id, err := ensureImage(client, ref.String())
		if err != nil {
			return "", err
		}
		tags = append(tags, tag{
			Tag:   ref,
			Image: id,
		})
		images = append(images, id)
	}
	for _, ci := range conf.CustomImages {
		id, err := ensureImage(client, ci.Source)
		if err != nil {
			return "", err
		}
		tags = append(tags, tag{
			Tag:   ci.Target,
			Image: id,
		})

		images = append(images, id)
	}

	dgstr := digest.Canonical.New()
	// Add runner options
	fmt.Fprintf(dgstr.Hash(), "Version: %s\n", HashVersion)
	fmt.Fprintln(dgstr.Hash())
	fmt.Fprintln(dgstr.Hash())

	// TODO: Incorporate image id
	fmt.Fprintf(dgstr.Hash(), "%s\n\n", conf.Base.String())

	// TODO: Sort tags, write
	for _, t := range tags {
		fmt.Fprintf(dgstr.Hash(), "%s %s\n", t.Tag.String(), t.Image)
	}
	fmt.Fprintln(dgstr.Hash())

	fmt.Fprintln(dgstr.Hash(), conf.DockerLoadVersion)
	fmt.Fprintln(dgstr.Hash(), conf.DockerVersion)

	imageHash := dgstr.Digest()

	id, err := c.ImageCache.GetImage(imageHash)
	if err == nil {
		logrus.Debugf("Found image in cache for %s: %s", imageHash, id)
		info, err := client.InspectImage(id)
		if err == nil {
			logrus.Debugf("Cached image found locally %s", info.ID)
			return id, nil
		}
		logrus.Errorf("Unable to find cached image %s: %v", id, err)
	} else {
		logrus.Debugf("Building image, could not find in cache: %v", err)
	}

	// Create temp build directory
	td, err := ioutil.TempDir("", "golem-")
	if err != nil {
		return "", fmt.Errorf("unable to create tempdir: %s", err)
	}
	defer os.RemoveAll(td)

	// Create Dockerfile in tempDir
	df, err := os.OpenFile(filepath.Join(td, "Dockerfile"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("unable to create dockerfile: %s", err)
	}
	defer df.Close()

	fmt.Fprintf(df, "FROM %s\n", conf.Base)

	imagesDir := filepath.Join(td, "images")
	if err := os.Mkdir(imagesDir, 0755); err != nil {
		return "", fmt.Errorf("unable to make images directory: %v", err)
	}

	for _, img := range images {
		if err := saveImage(client, filepath.Join(imagesDir, img+".tar"), img); err != nil {
			return "", fmt.Errorf("error saving image %s: %v", img, err)
		}

	}

	if err := saveTagMap(filepath.Join(imagesDir, "images.json"), tags); err != nil {
		return "", fmt.Errorf("error saving tag map: %v", err)
	}

	fmt.Fprintln(df, "COPY ./images /images")

	// Add Docker Binaries (docker test specific)
	c.BuildCache.InstallVersion(conf.DockerVersion, filepath.Join(td, "docker"))
	c.BuildCache.InstallVersion(conf.DockerLoadVersion, filepath.Join(td, "docker-load"))
	fmt.Fprintln(df, "COPY ./docker /usr/bin/docker")
	fmt.Fprintln(df, "COPY ./docker-load /usr/bin/docker-load")
	// TODO: Handle init files

	// TODO: Install executable
	//buildutil.CopyFile(executablePath, filepath.Join(td, "golem_runner"), 0755)
	//fmt.Fprintln(df, "COPY ./golem_runner /usr/bin/golem_runner")

	// Call build
	builder, err := client.NewBuilder(td, "", "")
	if err != nil {
		logrus.Errorf("Error creating builder: %v", err)
		return "", err
	}

	if err := builder.Run(); err != nil {
		logrus.Errorf("Error building: %v", err)
		return "", err
	}

	// Update index
	imageId := builder.ImageID()

	if err := c.ImageCache.SaveImage(imageHash, imageId); err != nil {
		logrus.Errorf("Unable to save image by hash %s: %s", imageHash, imageId)
	}

	return imageId, nil
}
