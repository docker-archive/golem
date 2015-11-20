package main

// Suite Configuration Ordering
// Golem defaults
// Suite directory golem.conf
// Run directory golem.conf
// Environment variables
// Command line flags

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Sirupsen/logrus"
	"github.com/bugsnag/osext"
	"github.com/dmcgowan/golem/versionutil"
	"github.com/docker/distribution/reference"
)

var globalDefault Resolver

func init() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	globalDefault = defaultResolver{
		base: assertTagged("dmcgowan/golem:latest"),
		path: cwd,
	}
}

type customImageMap map[string]CustomImage

func (m customImageMap) String() string {
	values := []string{}
	for k, v := range m {
		values = append(values, fmt.Sprintf("%s,%s", k, v))
	}
	return strings.Join(values, " ")
}

func (m customImageMap) Set(value string) error {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return errors.New("invalid custome image format, expected \"name,reference\"")
	}
	ref, err := reference.Parse(parts[0])
	if err != nil {
		return err
	}
	namedTagged, ok := ref.(reference.NamedTagged)
	if !ok {
		return fmt.Errorf("reference %s must contain name and tag", ref.String())
	}
	source, err := reference.ParseNamed(parts[1])
	if err != nil {
		return err
	}

	m[parts[0]] = CustomImage{
		Source: source.String(),
		Target: namedTagged,
	}

	return nil
}

type configurationVersion versionutil.Version

func (v *configurationVersion) String() string {
	return versionutil.Version(*v).String()
}

func (v *configurationVersion) Set(value string) error {
	dv, err := versionutil.ParseVersion(value)
	if err != nil {
		return err
	}
	*v = configurationVersion(dv)

	return nil
}

type configurationPath string

func (v *configurationPath) String() string {
	return string(*v)
}

func (v *configurationPath) Set(value string) error {
	absConf, err := filepath.Abs(value)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absConf); err != nil {
		return err
	}
	return nil
}

type testSuite struct {
	name string
	path string
}

type suites map[string]string

func (s suites) String() string {
	names := []string{}
	for name := range s {
		names = append(names, name)
	}
	return strings.Join(names, ",")
}

func (s suites) Set(value string) error {
	absPath, err := filepath.Abs(value)
	if err != nil {
		return err
	}
	if info, err := os.Stat(absPath); err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New("expecting suite to be given as directory")
	}

	s[filepath.Base(filepath.Dir(absPath))] = absPath

	return nil
}

type ConfigurationManager struct {
	flagResolver  *flagResolver
	dockerVersion configurationVersion
	configFile    configurationPath
	suites        suites
}

func NewConfigurationManager() *ConfigurationManager {
	m := &ConfigurationManager{
		flagResolver: newFlagResolver(),
	}

	// TODO: support extra images
	flag.Var(&m.dockerVersion, "docker-version", "Docker version to test")
	flag.Var(&m.configFile, "c", "Configuration file for running tests")
	flag.Var(m.suites, "s", "Path to test suite to run")

	return m
}

func (c *ConfigurationManager) RunnerConfiguration(loadDockerVersion versionutil.Version) (RunnerConfiguration, error) {
	conf := string(c.configFile)
	if conf == "" && len(c.suites) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return RunnerConfiguration{}, err
		}
		conf = filepath.Join(cwd, "golem.conf")

	}

	// Load Global default and update suite list
	cf, err := ParseConfigurationFile(conf, map[string]string(c.suites))
	if err != nil {
		return RunnerConfiguration{}, err
	}

	// Ensure no duplicated paths or names

	// TODO: Support non-linux by downloading and replacing executable path
	executablePath, err := osext.Executable()
	if err != nil {
		return RunnerConfiguration{}, fmt.Errorf("error getting path to executable: %s", err)
	}

	runnerConfig := RunnerConfiguration{
		ExecutableName: "golem_runner",
		ExecutablePath: executablePath,
	}

	suites := cf.Suites()
	for _, suite := range suites {
		// TODO: Create flag Resolver
		// TODO: Create global default Resolver
		resolver := MultiResolver(c.flagResolver, suite.Resolver, globalDefault)

		baseConf := BaseImageConfiguration{
			Base:              resolver.BaseImage(),
			ExtraImages:       resolver.Images(),
			CustomImages:      resolver.CustomImages(),
			DockerLoadVersion: loadDockerVersion,
			DockerVersion:     versionutil.Version(c.dockerVersion),
		}

		registrySuite := SuiteConfiguration{
			Name:           resolver.Name(),
			Path:           resolver.Path(),
			BaseImage:      baseConf,
			DockerInDocker: resolver.Dind(),
			Instances: []RunConfiguration{
				{
					Name: "default",
				},
			},
		}
		runnerConfig.Suites = append(runnerConfig.Suites, registrySuite)
	}

	return runnerConfig, nil
}

type Resolver interface {
	Name() string
	Path() string
	BaseImage() reference.NamedTagged
	Dind() bool
	Images() []reference.NamedTagged
	CustomImages() []CustomImage
}

type flagResolver struct {
	customImages customImageMap
}

func newFlagResolver() *flagResolver {
	fr := &flagResolver{
		customImages: customImageMap{},
	}

	flag.Var(fr.customImages, "i", "Set a custom image for running tests")

	return fr
}

func (fr *flagResolver) Name() string {
	return ""
}

func (fr *flagResolver) Path() string {
	return ""
}

func (fr *flagResolver) BaseImage() reference.NamedTagged {
	return nil
}

func (fr *flagResolver) Dind() bool {
	return false
}

func (fr *flagResolver) Images() []reference.NamedTagged {
	return nil
}

func (fr *flagResolver) CustomImages() []CustomImage {
	customImages := make([]CustomImage, 0, len(fr.customImages))
	for _, ci := range fr.customImages {
		customImages = append(customImages, ci)
	}
	return customImages
}

// defaultResolver is used to inject defaults
type defaultResolver struct {
	base reference.NamedTagged
	path string
}

func (dr defaultResolver) Name() string {
	return "default"
}

func (dr defaultResolver) Path() string {
	return dr.path
}

func (dr defaultResolver) BaseImage() reference.NamedTagged {
	return dr.base
}

func (dr defaultResolver) Dind() bool {
	return false
}

func (dr defaultResolver) Images() []reference.NamedTagged {
	return nil
}

func (dr defaultResolver) CustomImages() []CustomImage {
	return nil
}

type multiResolver struct {
	resolvers []Resolver
}

func MultiResolver(resolver ...Resolver) Resolver {
	return multiResolver{
		resolvers: resolver,
	}
}

func (mr multiResolver) Name() string {
	// Return first non-empty value
	for _, r := range mr.resolvers {
		if name := r.Name(); name != "" {
			return name
		}
	}
	return ""
}

func (mr multiResolver) Path() string {
	// Return first non-empty value
	for _, r := range mr.resolvers {
		if path := r.Path(); path != "" {
			return path
		}
	}
	return ""
}

func (mr multiResolver) BaseImage() reference.NamedTagged {
	for _, r := range mr.resolvers {
		if base := r.BaseImage(); base != nil {
			return base
		}
	}
	return nil
}

func (mr multiResolver) Dind() bool {
	// True if any resolve returns true
	for _, r := range mr.resolvers {
		if r.Dind() {
			return true
		}
	}
	return false
}

func (mr multiResolver) Images() []reference.NamedTagged {
	imageSet := map[string]reference.NamedTagged{}
	// Merge all sets
	for _, r := range mr.resolvers {
		for _, named := range r.Images() {
			imageSet[named.String()] = named
		}
	}
	images := make([]reference.NamedTagged, 0, len(imageSet))
	for _, named := range imageSet {
		images = append(images, named)
	}
	return images
}

func (mr multiResolver) CustomImages() []CustomImage {
	imageSet := map[string]CustomImage{}
	// Loop in reverse to ensure that base values get overwritten
	for i := len(mr.resolvers) - 1; i >= 0; i-- {
		for _, ci := range mr.resolvers[i].CustomImages() {
			imageSet[ci.Target.String()] = ci
		}
	}
	images := make([]CustomImage, 0, len(imageSet))
	for _, ci := range imageSet {
		images = append(images, ci)
	}
	return images
}

type suiteResolver struct {
	*suiteConfiguration
	path         string
	base         reference.NamedTagged
	images       []reference.NamedTagged
	customImages []CustomImage
}

func (sr *suiteResolver) Name() string {
	return sr.suiteConfiguration.Name
}

func (sr *suiteResolver) Path() string {
	return sr.path
}

func (sr *suiteResolver) BaseImage() reference.NamedTagged {
	return sr.base
}

func (sr *suiteResolver) Dind() bool {
	return sr.suiteConfiguration.Dind
}

func (sr *suiteResolver) Images() []reference.NamedTagged {
	return sr.images
}
func (sr *suiteResolver) CustomImages() []CustomImage {

	return sr.customImages
}

type ConfigurationSuite struct {
	Resolver

	// Target information
}

type ConfigurationFile struct {
	root   string
	suites map[string]ConfigurationSuite
}

func (c *ConfigurationFile) addSuiteConfig(path string, config *suiteConfiguration) error {
	customImages := make([]CustomImage, 0, len(config.CustomImages))
	for name, value := range config.CustomImages {
		ref, err := reference.Parse(name)
		if err != nil {
			return err
		}
		target, ok := ref.(reference.NamedTagged)
		if !ok {
			return fmt.Errorf("expecting name:tag for image target, got %s", name)
		}

		customImages = append(customImages, CustomImage{
			Source: value.Default,
			Target: target,
		})
	}
	images := make([]reference.NamedTagged, 0, len(config.Images))
	for _, image := range config.Images {
		named, err := getNamedTagged(image)
		if err != nil {
			return err
		}
		images = append(images, named)
	}

	resolver := &suiteResolver{
		suiteConfiguration: config,
		path:               path,
		customImages:       customImages,
		images:             images,
	}
	if config.Base != "" {
		var err error
		resolver.base, err = getNamedTagged(config.Base)
		if err != nil {
			return err
		}
	}

	c.suites[config.Name] = ConfigurationSuite{
		Resolver: resolver,
		// TODO: Add run target information
	}

	return nil
}

func getNamedTagged(image string) (reference.NamedTagged, error) {
	ref, err := reference.Parse(image)
	if err != nil {
		return nil, err
	}
	named, ok := ref.(reference.NamedTagged)
	if !ok {
		return nil, fmt.Errorf("Image reference must have name and tag: %s", image)
	}
	return named, nil
}

func (c *ConfigurationFile) addSuite(path, name string) error {
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.root, path)
	}

	confBytes, err := ioutil.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("unable to open configuration file %s: %s", path, err)
		}
		// TODO: Add test runner detection so this does not need to return an error
		return errors.New("no suite configuration found")
	}

	sc := suiteConfigurationFile{}
	if err := toml.Unmarshal(confBytes, &sc); err != nil {
		return err
	}

	if sc.Suite == nil {
		// TODO: Handle support for no relevant configuration
		return errors.New("no suite configuration found")
	}

	return c.addSuiteConfig(path, sc.Suite)
}

func (c *ConfigurationFile) Suites() map[string]ConfigurationSuite {
	return c.suites
}

func ParseConfigurationFile(conf string, suites map[string]string) (*ConfigurationFile, error) {
	c := &ConfigurationFile{
		root:   filepath.Dir(conf),
		suites: map[string]ConfigurationSuite{},
	}
	if len(suites) > 0 {
		for name, path := range suites {
			if err := c.addSuite(string(path), name); err != nil {
				return nil, err
			}
		}
	} else {
		confBytes, err := ioutil.ReadFile(conf)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("unable to open configuration file %s: %s", conf, err)
			}
			// TODO: Add test runner detection so this does not need to return an error
			return nil, errors.New("no suite configuration found")
		}

		ac := anyConfigurationFile{}
		if err := toml.Unmarshal(confBytes, &ac); err != nil {
			return nil, err
		}

		if ac.Suite != nil && len(ac.Suites) > 0 {
			return nil, fmt.Errorf("ambiguous configuration: must be single suite or set of suites")
		}
		if len(ac.Suites) > 0 {
			for name, suite := range ac.Suites {
				if err := c.addSuite(suite.Directory, name); err != nil {
					return nil, err
				}
			}
		}
		if ac.Suite != nil {
			// Add single configuration
			if ac.Suite.Name == "" {
				ac.Suite.Name = filepath.Base(filepath.Dir(conf))
			}
			if err := c.addSuiteConfig(filepath.Dir(conf), ac.Suite); err != nil {
				return nil, err
			}
		}
	}

	return c, nil
}

type customimageConfiguration struct {
	Default string `toml:"default"`
}

type suiteConfiguration struct {
	// Name is used to set the name of this suite, if none is set here then the name
	// should be set by the runner configuration or using the directory name
	Name string `toml:"name"`

	// Dind (or "Docker in Docker") used to determine whether a docker daemon will be run
	// inside the test container
	Dind bool `toml:"dind"`

	// Base is the base image to build the test from
	Base string `toml:"baseimage"`

	// Testrunner determines what will be used to run the tests
	// Supprted test runners ["go", "bats"]
	Testrunner string `toml:"testrunner"`

	// Testargs are additional arguments to give to the test runner
	Testargs []string `toml:"testargs"`

	// Testenv are additional environment variables for the test runner process
	Testenv []string `toml:"testenv"`

	// Images which should exist in the test container
	// automatically set dind to true
	Images []string `toml:"images"`

	// CustomImages allow runtime selection of an image inside the container
	// automatically set dind to true
	CustomImages map[string]customimageConfiguration `toml:"customimages"`
}

type directoryConfiguration struct {
	Directory string `toml:"directory"`
}

type suiteConfigurationFile struct {
	Suite *suiteConfiguration `toml:"suite"`
}

type anyConfigurationFile struct {
	suiteConfigurationFile
	Suites map[string]*directoryConfiguration `toml:"suites"`
}

func assertTagged(image string) reference.NamedTagged {
	ref, err := reference.Parse(image)
	if err != nil {
		logrus.Panicf("Invalid reference %q: %v", image, err)
	}
	named, ok := ref.(reference.NamedTagged)
	if !ok {
		logrus.Panicf("Image reference must have name and tag: %s", image)
	}

	return named
}
