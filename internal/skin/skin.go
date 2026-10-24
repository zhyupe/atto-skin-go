// Package skin validates and canonicalizes textures for both uploads and migrations.
package skin

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"io"
)

const MaxBytes = 1 << 20

var ErrInvalid = errors.New("请上传不超过 1 MiB 的 64×64 或 64×32 PNG 皮肤；纤细模型须使用 64×64。")

func Normalize(r io.Reader, model string) ([]byte, error) {
	if model != "default" && model != "slim" {
		return nil, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	if len(raw) > MaxBytes {
		return nil, ErrInvalid
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 64 || (cfg.Height != 32 && cfg.Height != 64) ||
		(model == "slim" && cfg.Height != 64) {
		return nil, ErrInvalid
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrInvalid
	}
	var b bytes.Buffer
	if err = png.Encode(&b, img); err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	return b.Bytes(), nil
}
