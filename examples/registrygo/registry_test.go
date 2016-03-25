package registrygo

import (
	"testing"

	"github.com/docker/golem/examples/registrygo/helpers"
)

func TestPush(t *testing.T) {
	imageName := "localregistry:5000/testpush"
	if err := helpers.TempImage(imageName); err != nil {
		t.Fatal(err)
	}

	if err := helpers.DockerRun("push", imageName); err != nil {
		t.Fatal(err)
	}
}
