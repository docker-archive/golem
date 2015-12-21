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
	suites        suites
}

func NewConfigurationManager() *ConfigurationManager {
	m := &ConfigurationManager{
		flagResolver: newFlagResolver(),
	}

	// TODO: support extra images
	flag.Var(&m.dockerVersion, "docker-version", "Docker version to test")
	flag.Var(m.suites, "s", "Path to test suite to run")

	return m
}

func (c *ConfigurationManager) RunnerConfiguration(loadDockerVersion versionutil.Version) (RunnerConfiguration, error) {
	// TODO: eliminate suites and just use arguments
	var conf string
	// Get first flag
	if flag.NArg() == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return RunnerConfiguration{}, err
		}
		conf = filepath.Join(cwd, "golem.conf")
		logrus.Debugf("No configuration given, trying current directory %s", conf)
	} else {
		absPath, err := filepath.Abs(flag.Arg(0))
		if err != nil {
			return RunnerConfiguration{}, err
		}

		info, err := os.Stat(absPath)
		if err != nil {
			return RunnerConfiguration{}, err
		}
		if info.IsDir() {
			absPath = filepath.Join(absPath, "golem.conf")
			if _, err := os.Stat(absPath); err != nil {
				return RunnerConfiguration{}, err

			}
		}
		conf = absPath

	}

	suites, err := ParseSuites(flag.Args())
	if err != nil {
		return RunnerConfiguration{}, err
	}

	// TODO: Support non-linux by downloading and replacing executable path
	executablePath, err := osext.Executable()
	if err != nil {
		return RunnerConfiguration{}, fmt.Errorf("error getting path to executable: %s", err)
	}

	runnerConfig := RunnerConfiguration{
		ExecutableName: "golem_runner",
		ExecutablePath: executablePath,
	}

	for _, suite := range suites {
		resolver := MultiResolver(c.flagResolver, suite, globalDefault)

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
		}

		// Set runner arguments
		args := []string{"-command", suite.Command}
		if resolver.Dind() {
			args = append(args, "-docker")

		}
		for _, pretest := range suite.Pretest {
			args = append(args, "-prescript", pretest)
		}

		args = append(args, suite.Args...)
		registrySuite.Instances = append(registrySuite.Instances, RunConfiguration{
			Name: "default",
			Env:  suite.Env,
			Args: args,
		})
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

type ConfigurationSuite struct {
	config suiteConfiguration

	path         string
	base         reference.NamedTagged
	images       []reference.NamedTagged
	customImages []CustomImage

	resolvedName string

	// Target information
	Pretest []string
	Command string
	Args    []string
	Env     []string
}

func (cs *ConfigurationSuite) SetName(name string) {
	cs.resolvedName = name
}

func (cs *ConfigurationSuite) Name() string {
	return cs.resolvedName
}

func (cs *ConfigurationSuite) Path() string {
	return cs.path
}

func (cs *ConfigurationSuite) BaseImage() reference.NamedTagged {
	return cs.base
}

func (cs *ConfigurationSuite) Dind() bool {
	return cs.config.Dind
}

func (cs *ConfigurationSuite) Images() []reference.NamedTagged {
	return cs.images
}
func (cs *ConfigurationSuite) CustomImages() []CustomImage {
	return cs.customImages
}

func newSuiteConfiguration(path string, config suiteConfiguration) (*ConfigurationSuite, error) {
	customImages := make([]CustomImage, 0, len(config.CustomImages))
	for _, value := range config.CustomImages {
		ref, err := reference.Parse(value.Tag)
		if err != nil {
			return nil, err
		}
		target, ok := ref.(reference.NamedTagged)
		if !ok {
			return nil, fmt.Errorf("expecting name:tag for image target, got %s", value.Tag)
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
			return nil, err
		}
		images = append(images, named)
	}

	var base reference.NamedTagged
	if config.Base != "" {
		var err error
		base, err = getNamedTagged(config.Base)
		if err != nil {
			return nil, err
		}
	}

	name := config.Name
	if name == "" {
		name = filepath.Base(path)
	}

	return &ConfigurationSuite{
		config:       config,
		path:         path,
		base:         base,
		customImages: customImages,
		images:       images,

		resolvedName: name,

		Pretest: config.Pretest,
		Command: config.Testrunner,
		Args:    config.Testargs,
		Env:     config.Testenv,
	}, nil
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

func ParseSuites(suites []string) (map[string]*ConfigurationSuite, error) {
	configs := map[string]*ConfigurationSuite{}
	for _, suite := range suites {
		logrus.Debugf("Handling suite %s", suite)
		absPath, err := filepath.Abs(suite)
		if err != nil {
			return nil, fmt.Errorf("could not resolve %s: %s", suite, err)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("error statting %s: %s", suite, err)
		}
		if info.IsDir() {
			absPath = filepath.Join(absPath, "golem.conf")
			if _, err := os.Stat(absPath); err != nil {
				return nil, fmt.Errorf("error statting %s: %s", filepath.Join(suite, "golem.conf"), err)
			}
		}

		confBytes, err := ioutil.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("unable to open configuration file %s: %s", absPath, err)
		}

		// Load
		var conf suitesConfiguration
		if err := toml.Unmarshal(confBytes, &conf); err != nil {
			return nil, fmt.Errorf("error unmarshalling %s: %s", absPath, err)
		}

		logrus.Debugf("Found %d test suites in %s", len(conf.Suites), suite)
		for _, sc := range conf.Suites {
			p := filepath.Dir(absPath)
			suiteConfig, err := newSuiteConfiguration(p, sc)
			if err != nil {
				return nil, err
			}

			name := suiteConfig.Name()
			_, ok := configs[name]
			for i := 1; ok; i++ {
				name = fmt.Sprintf("%s-%d", suiteConfig.Name(), i)
				_, ok = configs[name]
			}
			suiteConfig.SetName(name)
			configs[name] = suiteConfig
		}
	}

	return configs, nil
}

type customimageConfiguration struct {
	Tag     string `toml:"tag"`
	Default string `toml:"default"`
}

type suitesConfiguration struct {
	Suites []suiteConfiguration `toml:"suite"`
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

	// Pretest is the commands to run before the test starts
	Pretest []string `toml:"pretest"`

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
	CustomImages []customimageConfiguration `toml:"customimage"`
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
