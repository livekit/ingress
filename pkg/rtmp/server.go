package rtmp

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/google/uuid"
)

func Launch() error {
	cmd := exec.Command("rtsp-simple-server")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

func NewUrl() string {
	return fmt.Sprintf("rtmp://localhost:1935/live/%s", uuid.NewString())
}
