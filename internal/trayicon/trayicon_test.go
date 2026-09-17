package trayicon

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Not a test of correctness: with ALG_ICON_DUMP set, writes the same reading
// at several source sizes so they can be compared after panel downscaling.
func TestDumpSizes(t *testing.T) {
	dir := os.Getenv("ALG_ICON_DUMP")
	if dir == "" {
		t.Skip("ALG_ICON_DUMP not set")
	}
	for _, size := range []int{24, 48, 64, 96} {
		for _, temp := range []int{47, 86, 104} {
			data, err := render(size, strconv.Itoa(temp), TempColor(temp))
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "size-"+strconv.Itoa(size)+"-"+strconv.Itoa(temp)+".png"), data, 0o644)
		}
	}
}

func TestIcons(t *testing.T) {
	icons := map[string]func() ([]byte, error){
		"047":     func() ([]byte, error) { return Temperature(47) },
		"078":     func() ([]byte, error) { return Temperature(78) },
		"096":     func() ([]byte, error) { return Temperature(96) },
		"104":     func() ([]byte, error) { return Temperature(104) },
		"offline": Offline,
	}
	for name, draw := range icons {
		data, err := draw()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != Size || b.Dy() != Size {
			t.Errorf("%s: %dx%d, want %d square", name, b.Dx(), b.Dy(), Size)
		}
		// Corners are outside the rounded plate, the centre row is inside
		// it, and somewhere on the plate the text must have left fully
		// opaque ink (the plate itself is slightly translucent).
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("%s: the corner should be transparent", name)
		}
		ink, left, right := false, Size, 0
		for y := 0; y < Size; y++ {
			for x := 0; x < Size; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a == 0xffff {
					ink = true
					left, right = min(left, x), max(right, x)
				}
			}
		}
		if !ink {
			t.Errorf("%s: no text was drawn", name)
		} else if off := (left + right + 1) - Size; off < -3 || off > 3 {
			t.Errorf("%s: text is off-centre (ink spans %d..%d)", name, left, right)
		}
		if dir := os.Getenv("ALG_ICON_DUMP"); dir != "" {
			os.WriteFile(filepath.Join(dir, "temp-"+name+".png"), data, 0o644)
		}
	}
}
