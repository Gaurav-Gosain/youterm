package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"syscall"
)

// Minimal test: send a solid red 100x100 image via kitty graphics protocol.
// Tests three methods: t=d (direct), t=f (file), t=s (shm).
func main() {
	w, h := 100, 100
	size := w * h * 3
	pixels := make([]byte, size)
	// Solid red
	for i := 0; i < size; i += 3 {
		pixels[i] = 255   // R
		pixels[i+1] = 0   // G
		pixels[i+2] = 0   // B
	}

	fmt.Println("Testing kitty graphics protocol...")
	fmt.Println()

	// Method 1: t=d (direct base64) — simplest, always works if kitty is supported
	fmt.Print("Method 1 (t=d direct base64): ")
	b64data := base64.StdEncoding.EncodeToString(pixels)
	// Send in chunks of 4096
	first := true
	for len(b64data) > 0 {
		chunk := b64data
		if len(chunk) > 4096 {
			chunk = b64data[:4096]
			b64data = b64data[4096:]
		} else {
			b64data = ""
		}
		if first {
			more := 1
			if len(b64data) == 0 {
				more = 0
			}
			fmt.Fprintf(os.Stdout, "\x1b_Ga=T,f=24,s=%d,v=%d,C=1,q=2,m=%d;%s\x1b\\", w, h, more, chunk)
			first = false
		} else {
			more := 1
			if len(b64data) == 0 {
				more = 0
			}
			fmt.Fprintf(os.Stdout, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	fmt.Println(" (should see red square above)")
	fmt.Println()

	// Method 2: t=f (file path to /dev/shm)
	path := "/dev/shm/youterm-test-img"
	os.WriteFile(path, pixels, 0600)
	defer os.Remove(path)
	b64path := base64.StdEncoding.EncodeToString([]byte(path))
	fmt.Print("Method 2 (t=f file path): ")
	fmt.Fprintf(os.Stdout, "\x1b_Ga=T,t=f,f=24,s=%d,v=%d,C=1,q=2;%s\x1b\\", w, h, b64path)
	fmt.Println(" (should see red square above)")
	fmt.Println()

	// Method 3: t=s (POSIX shared memory — mpv's approach)
	shmName := "youterm-test-shm"
	shmPath := "/dev/shm/" + shmName
	fd, err := syscall.Open(shmPath, syscall.O_CREAT|syscall.O_RDWR, 0600)
	if err != nil {
		fmt.Printf("shm open error: %v\n", err)
		return
	}
	syscall.Ftruncate(fd, int64(size))
	buf, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Printf("mmap error: %v\n", err)
		syscall.Close(fd)
		return
	}
	copy(buf, pixels)

	b64name := base64.StdEncoding.EncodeToString([]byte(shmName))
	fmt.Print("Method 3 (t=s POSIX shm): ")
	fmt.Fprintf(os.Stdout, "\x1b_Ga=T,t=s,f=24,s=%d,v=%d,C=1,q=2,m=1;%s\x1b\\", w, h, b64name)
	fmt.Println(" (should see red square above)")

	syscall.Munmap(buf)
	syscall.Close(fd)
	// Don't remove — kitty should shm_unlink it

	fmt.Println()
	fmt.Println("If you see 3 red squares, all methods work.")
	fmt.Println("If only some work, report which ones.")
}
