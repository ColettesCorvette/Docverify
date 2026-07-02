package docverify

import (
	"context"
	"os"
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

func TestPreprocessForLLMSkipsSmallFiles(t *testing.T) {
	tmp, err := os.CreateTemp("", "small-*.png")
	if err != nil {
		t.Fatalf("création fichier temporaire: %v", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write([]byte("tiny")); err != nil {
		t.Fatalf("écriture fichier temporaire: %v", err)
	}
	tmp.Close()

	path, cleanup := preprocessForLLM(context.Background(), tmp.Name())
	defer cleanup()
	if path != tmp.Name() {
		t.Errorf("preprocessForLLM = %q, attendu le chemin original %q (fichier sous le seuil)", path, tmp.Name())
	}
}
