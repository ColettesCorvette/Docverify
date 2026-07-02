package docverify

import (
	"reflect"
	"testing"
)

func TestImageMagickArgsPrependsLimits(t *testing.T) {
	got := imageMagickArgs("input.png", "-resize", "100x100")
	want := []string{
		"-limit", "memory", "256MiB",
		"-limit", "map", "512MiB",
		"-limit", "disk", "1GiB",
		"input.png", "-resize", "100x100",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imageMagickArgs = %v, attendu %v", got, want)
	}
}
