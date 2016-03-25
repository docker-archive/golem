package runner

import (
	"testing"

	"github.com/docker/distribution/reference"
)

func mustImage(source, target, version string) CustomImage {
	ref, err := reference.Parse(target)
	if err != nil {
		panic(err)
	}
	namedTagged, ok := ref.(reference.NamedTagged)
	if !ok {
		panic("must provided named tagged for image target")
	}

	return CustomImage{
		Source:  source,
		Target:  namedTagged,
		Version: version,
	}
}

func TestImageMatrixExpansion(t *testing.T) {
	startImages := []CustomImage{
		mustImage("golem-image1:v1.10.1", "image1:latest", "1.10.1"),
		mustImage("golem-image2:v1.10.1", "image2:latest", "1.10.1"),
		mustImage("golem-image3:v1.10.1", "image3:latest", "1.10.1"),
		mustImage("golem-image2:v1.10.2", "image2:latest", "1.10.2"),
		mustImage("golem-image2:v1.10.3", "image2:latest", "1.10.3"),
		mustImage("golem-image1:v1.11.1", "image1:latest", "1.11.1"),
		mustImage("golem-image4:v1.10.1", "image4:latest", "1.10.1"),
	}
	matrix := [][]CustomImage{
		{startImages[0], startImages[1], startImages[2], startImages[6]},
		{startImages[0], startImages[3], startImages[2], startImages[6]},
		{startImages[0], startImages[4], startImages[2], startImages[6]},
		{startImages[5], startImages[1], startImages[2], startImages[6]},
		{startImages[5], startImages[3], startImages[2], startImages[6]},
		{startImages[5], startImages[4], startImages[2], startImages[6]},
	}

	expanded := expandCustomImageMatrix(startImages)

	if len(expanded) != len(matrix) {
		t.Log("Expanded:", expanded)
		t.Fatalf("Unexpected matrix size %d, expected %d", len(expanded), len(matrix))
	}

	for i := range matrix {
		if len(expanded[i]) != len(matrix[i]) {
			t.Fatalf("Unexpected array size %d, expected %d", len(expanded[i]), len(matrix[i]))
		}

		for j := range matrix[i] {
			if !equalCustomImage(expanded[i][j], matrix[i][j]) {
				t.Fatalf("Unexpected custom image %v, expected %v", expanded[i][j], matrix[i][j])
			}
		}
	}
}
