package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestBrowserWardrobe runs the real server against a browser. The OIDC provider,
// profiles and textures are synthetic; no external identity or user data is used.
func TestBrowserWardrobe(t *testing.T) {
	if os.Getenv("ATTO_BROWSER_TEST") != "1" {
		t.Skip("set ATTO_BROWSER_TEST=1 with npm dependencies installed")
	}
	s, _ := testServer(t)
	u, _ := seed(t, s, "wardrobe-browser", "Wardrobe")
	seed(t, s, "wardrobe-conflict", "TakenName")
	for i := 0; i < 6; i++ {
		model := "default"
		if i%2 == 1 {
			model = "slim"
		}
		if err := s.setSkin(u.ID, browserPNG(t, i, 64), model, ""); err != nil {
			t.Fatal(err)
		}
	}
	cookie, _ := browser(t, s, u)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	s.cfg.BaseURL = srv.URL
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "upload.png"), browserPNG(t, 6, 64), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.png"), browserPNG(t, 7, 32), 0600); err != nil {
		t.Fatal(err)
	}
	fixture, err := json.Marshal(map[string]string{"url": srv.URL, "cookie": cookie.Value, "uuid": u.ID, "dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(dir, "fixture.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../tests/browser.cjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script, fixturePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func browserPNG(t *testing.T, variant, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, height))
	palettes := []color.NRGBA{{98, 133, 183, 255}, {182, 104, 98, 255}, {184, 190, 193, 255}, {136, 112, 155, 255}, {73, 93, 108, 255}, {211, 149, 75, 255}, {73, 138, 145, 255}, {167, 121, 74, 255}}
	fill := func(x, y, w, h int, c color.NRGBA) {
		draw.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	hair := color.NRGBA{70, 57, 50, 255}
	face := color.NRGBA{219, 179, 146, 255}
	shirt := palettes[variant%len(palettes)]
	pants := color.NRGBA{58, 65, 77, 255}
	fill(0, 0, 32, 16, hair)
	fill(8, 8, 8, 8, face)
	fill(8, 8, 8, 2, hair)
	fill(9, 12, 2, 1, color.NRGBA{242, 235, 224, 255})
	fill(13, 12, 2, 1, color.NRGBA{242, 235, 224, 255})
	fill(10, 12, 1, 1, hair)
	fill(13, 12, 1, 1, hair)
	fill(11, 14, 2, 1, color.NRGBA{154, 100, 83, 255})
	fill(0, 16, 16, 16, pants)
	fill(16, 16, 24, 16, shirt)
	fill(40, 16, 16, 16, shirt)
	fill(20, 20, 8, 2, face)
	fill(23, 22, 2, 8, color.NRGBA{200, 192, 173, 255})
	fill(44, 28, 4, 4, face)
	fill(0, 29, 16, 3, hair)
	if height == 64 {
		fill(16, 48, 16, 16, pants)
		fill(32, 48, 16, 16, shirt)
		fill(36, 60, 4, 4, face)
		fill(16, 61, 16, 3, hair)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
