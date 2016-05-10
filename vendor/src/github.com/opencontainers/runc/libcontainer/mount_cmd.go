package libcontainer

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Attempts a mount cmd
func DoMountCmd(deviceName, source, dest string, args []string) error {
	cmd := exec.Command("mount", append([]string{source, dest}, args...)...)
	var out bytes.Buffer
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		e := fmt.Errorf("Failed to mount %s device %s to %s with arguments %v: %s - %s", deviceName, source, dest, args, err, strings.TrimRight(out.String(), "\n"))
		fmt.Fprintf(os.Stderr, "%s\n", e)
		return e
	}
	fmt.Fprintf(os.Stderr, "Succeeded in mounting %s device %s to %s with arguments %v\n", deviceName, source, dest, args)
	return nil
}
