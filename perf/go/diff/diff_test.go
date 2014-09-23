package diff

import (
	"os"
	"path/filepath"
	"testing"
)

import (
	"github.com/stretchr/testify/assert"
)

const (
	TESTDATA_DIR = "testdata"
)

func TestDiff(t *testing.T) {
	// Assert different images with the same dimensions.
	assertDiffs(t, "4029959456464745507", "16465366847175223174",
		&DiffMetrics{
			NumDiffPixels:     16,
			PixelDiffPercent:  0.0064,
			PixelDiffFilePath: filepath.Join(os.TempDir(), "diff1.png"),
			MaxRGBDiffs:       []int{54, 100, 125}})
	assertDiffs(t, "5024150605949408692", "11069776588985027208",
		&DiffMetrics{
			NumDiffPixels:     2233,
			PixelDiffPercent:  0.8932,
			PixelDiffFilePath: filepath.Join(os.TempDir(), "diff2.png"),
			MaxRGBDiffs:       []int{0, 0, 1}})
	// Assert the same image.
	assertDiffs(t, "5024150605949408692", "5024150605949408692",
		&DiffMetrics{
			NumDiffPixels:     0,
			PixelDiffPercent:  0,
			PixelDiffFilePath: filepath.Join(os.TempDir(), "diff3.png"),
			MaxRGBDiffs:       []int{0, 0, 0}})
	// Assert different images with different dimensions.
	assertDiffs(t, "ffce5042b4ac4a57bd7c8657b557d495", "fffbcca7e8913ec45b88cc2c6a3a73ad",
		&DiffMetrics{
			NumDiffPixels:     571674,
			PixelDiffPercent:  89.324066,
			PixelDiffFilePath: filepath.Join(os.TempDir(), "diff4.png"),
			MaxRGBDiffs:       []int{255, 255, 255}})
}

func assertDiffs(t *testing.T, d1, d2 string, expectedDiffMetrics *DiffMetrics) {
	img1, err := openImage(filepath.Join(TESTDATA_DIR, d1+".png"))
	if err != nil {
		t.Fatal("Failed to open test file: ", err)
	}
	img2, err := openImage(filepath.Join(TESTDATA_DIR, d2+".png"))
	if err != nil {
		t.Fatal("Failed to open test file: ", err)
	}

	diffMetrics, err := Diff(img1, img2, expectedDiffMetrics.PixelDiffFilePath)
	if err != nil {
		t.Error("Unexpected error: ", err)
	}
	assert.Equal(t, diffMetrics, expectedDiffMetrics)
}