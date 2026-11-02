package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/PiniPP/credorbit/internal/tui/demo"
)

// TestPrintFrames prints static frames for visual review:
//
//	CREDORBIT_FRAMES=menu,accounts go test ./internal/tui -run TestPrintFrames -v
func TestPrintFrames(t *testing.T) {
	list := os.Getenv("CREDORBIT_FRAMES")
	if list == "" {
		t.Skip("set CREDORBIT_FRAMES to print frames")
	}
	ascii := os.Getenv("CREDORBIT_FRAMES_ASCII") == "1"
	w, h := 100, 32
	_, _ = fmt.Sscanf(os.Getenv("CREDORBIT_FRAMES_SIZE"), "%dx%d", &w, &h)
	for _, s := range strings.Split(list, ",") {
		out := RenderStatic(demo.New(), s, w, h, "default", ascii)
		if os.Getenv("CREDORBIT_FRAMES_ANSI") != "1" {
			out = ansiStrip(out)
		}
		fmt.Printf("=== %s (%dx%d ascii=%v)\n%s\n", s, w, h, ascii, out)
	}
}
