package ui

import (
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"time"
)

// thumb holds a decoded YouTube thumbnail prepared for kitty graphics.
type thumb struct {
	path    string
	pathB64 string
	w, h    int
}

var thumbClient = &http.Client{Timeout: 10 * time.Second}

func downloadThumb(videoID string) (*thumb, error) {
	url := fmt.Sprintf("https://i.ytimg.com/vi/%s/mqdefault.jpg", videoID)

	resp, err := thumbClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}

	img, err := jpeg.Decode(resp.Body)
	if err != nil {
		return nil, err
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgb := make([]byte, w*h*3)
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			rgb[i] = byte(r >> 8)
			rgb[i+1] = byte(g >> 8)
			rgb[i+2] = byte(bl >> 8)
			i += 3
		}
	}

	path := fmt.Sprintf("%s/youterm-thumb-%d-%s", thumbTmpDir(), os.Getpid(), videoID)
	if err := os.WriteFile(path, rgb, 0600); err != nil {
		return nil, err
	}

	return &thumb{
		path:    path,
		pathB64: base64.StdEncoding.EncodeToString([]byte(path)),
		w:       w,
		h:       h,
	}, nil
}

func thumbTmpDir() string {
	if _, err := os.Stat("/dev/shm"); err == nil {
		return "/dev/shm"
	}
	return os.TempDir()
}

// displayCmd returns the escape sequence that transmits and displays
// the thumbnail at the current cursor position in one step (a=T).
// C=1 keeps the cursor where it is so callers can draw text alongside.
func (t *thumb) displayCmd(cols, rows int) string {
	return fmt.Sprintf("\x1b_Ga=T,t=f,f=24,s=%d,v=%d,c=%d,r=%d,C=1,q=2;%s\x1b\\",
		t.w, t.h, cols, rows, t.pathB64)
}

func (t *thumb) cleanup() {
	if t.path != "" {
		os.Remove(t.path)
	}
}
