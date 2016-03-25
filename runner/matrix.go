package runner

func expandCustomImageMatrix(images []CustomImage) [][]CustomImage {
	imageMatrix := make([][]CustomImage, 0, len(images))
	for _, img := range images {
		if len(imageMatrix) == 0 {
			imageMatrix = append(imageMatrix, []CustomImage{img})
			continue
		}
		var exists bool
		for i, target := range imageMatrix[0] {
			// if column already exists, duplicate all rows with same CustomImage
			if target.Target.String() == img.Target.String() {
				exists = true
				matrixEnd := len(imageMatrix)
				for j := 0; j < matrixEnd; j++ {
					if j > 0 && !equalCustomImage(imageMatrix[0][i], imageMatrix[j][i]) {
						continue
					}
					// Is same CustomImage
					imagesCopy := append([]CustomImage{}, imageMatrix[j]...)
					imagesCopy[i] = img
					imageMatrix = append(imageMatrix, imagesCopy)
				}
				break
			}
		}

		// If image did not exist, add column by adding to each row
		if !exists {
			for i := range imageMatrix {
				imageMatrix[i] = append(imageMatrix[i], img)
			}
		}
	}

	return imageMatrix
}

func equalCustomImage(i1, i2 CustomImage) bool {
	if i1.Source != i2.Source {
		return false
	}
	if i1.Target.String() != i2.Target.String() {
		return false
	}
	if i1.Version != i2.Version {
		return false
	}

	return true
}
