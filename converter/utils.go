package converter

import (
	"fmt"
	"os/exec"
	"runtime"
)

func GetEncoderName() string {
	if IsCommandAvailable(FFMPEGEncoder) {
		return FFMPEGEncoder
	} else {
		panic(fmt.Sprintf("command `%s` not found", FFMPEGEncoder))
	}
}

func IsCommandAvailable(name string) bool {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("where", name)
	} else {
		cmd = exec.Command("which", name)
	}

	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}
