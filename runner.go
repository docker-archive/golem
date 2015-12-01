package main

import (
	"archive/tar"
	"encoding/json"
	"errors"
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
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/jlhawn/dockramp/build"
	"github.com/termie/go-shutil"
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
		logrus.Fatalf("Unable to create tempdir: %v", err)
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
	hc := &dockerclient.HostConfig{
		Privileged: true,
	}
	env := []string{}
	if storageDriver := os.Getenv("DOCKER_GRAPHDRIVER"); storageDriver != "" {
		env = append(env, "DOCKER_GRAPHDRIVER="+storageDriver)
	}

	cc := dockerclient.CreateContainerOptions{
		Config: &dockerclient.Config{
			Image:      "golemrunner:latest",
			Cmd:        []string{"/usr/bin/golem_runner"},
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

// Save images
func saveImages(client DockerClient, out io.Writer, images []string) error {
	logrus.Debugf("Exporting images %s", strings.Join(images, " "))
	ec := dockerclient.ExportImagesOptions{
		Names:        images,
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

	if err := RunScript("/usr/bin/docker-previous", "images"); err != nil {
		logrus.Fatalf("Error running docker images")
	}

	logrus.Printf("Stopping daemon")
	if err := pk(); err != nil {
		logrus.Fatalf("Error killing daemon %v", err)
	}

	if err := RunScript("ls", "-l", "/runner"); err != nil {
		logrus.Fatalf("Error running docker images")
	}
	if err := RunScript("/bin/sh", "-c", "\"pwd\""); err != nil {
		logrus.Fatalf("Error running docker images")
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

type tag struct {
	Tag   reference.NamedTagged
	Image string
}

func tarCopy(w *tar.Writer, r *tar.Reader) error {
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			// end of tar archive
			return nil
		}
		if err != nil {
			return err
		}
		if err := w.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			return err
		}
	}
}

func addFile(w *tar.Writer, name string, contents []byte) error {
	// TODO: Create file info
	fi, err := os.Stat("/etc/hosts")
	if err != nil {
		return err
	}
	h, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	h.Name = name
	h.Size = int64(len(contents))
	if err := w.WriteHeader(h); err != nil {
		return err
	}
	if _, err := w.Write(contents); err != nil {
		return err
	}
	return nil
}

func copyImageTarWithTagMap(source io.Reader, target string, tags []tag) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	tr := tar.NewReader(source)

	layers := map[string]struct{}{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			// end of tar archive
			break
		}
		logrus.Debugf("Copying file %q", hdr.Name)
		if filename := filepath.Base(hdr.Name); len(filename) >= 64 {
			layers[filename] = struct{}{}
		}
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}

	repositories := map[string]map[string]string{}
	for _, t := range tags {
		if _, ok := layers[t.Image]; !ok {
			return fmt.Errorf("missing layer %s", t.Image)
		}
		m, ok := repositories[t.Tag.Name()]
		if ok {
			m[t.Tag.Tag()] = t.Image
		} else {
			repositories[t.Tag.Name()] = map[string]string{
				t.Tag.Tag(): t.Image,
			}
		}
	}

	c, err := json.Marshal(repositories)
	if err != nil {
		return err
	}

	logrus.Debugf("Writing repositories with %s", string(c))
	if err := addFile(tw, "repositories", c); err != nil {
		return err
	}

	return tw.Close()
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
		return id, nil
	}
	logrus.Debugf("Building image, could not find in cache: %v", err)

	// Calculate configuration hash
	// Check if in index
	// Check if image exists

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

	// TODO: Check if has tar of images in cache (sort images and hash)
	// TODO: Create tar of images, save in cache

	r, w := io.Pipe()
	errC := make(chan error)
	go func() {
		err := copyImageTarWithTagMap(r, filepath.Join(td, "images.tar"), tags)
		if err != nil {
			logrus.Error("Error copying image with tag map: %v", err)
			r.CloseWithError(err)
		}
		errC <- err
		close(errC)

	}()
	if err := saveImages(client, w, images); err != nil {
		w.CloseWithError(err)
		logrus.Fatalf("Error saving images: %v", err)
	}
	if err := w.Close(); err != nil {
		logrus.Fatalf("Error closing pipe: %v", err)
	}
	if err := <-errC; err != nil {
		logrus.Fatalf("Error copying to tag map: %v", err)
	}
	fmt.Fprintln(df, "COPY ./images.tar /images.tar")

	// Add Docker Binaries (docker test specific)
	c.BuildCache.InstallVersion(conf.DockerVersion, filepath.Join(td, "docker"))
	c.BuildCache.InstallVersion(conf.DockerLoadVersion, filepath.Join(td, "docker-previous"))
	fmt.Fprintln(df, "COPY ./docker /usr/bin/docker")
	fmt.Fprintln(df, "COPY ./docker-previous /usr/bin/docker-previous")
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
