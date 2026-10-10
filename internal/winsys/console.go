package winsys

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// ReadSecret prints prompt and reads one line from the console without echoing
// it, for PINs. It switches off the console's echo mode while reading, and puts
// the mode back afterwards (defer runs even if reading fails).
func ReadSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", errors.New("a PIN can only be typed at an interactive console")
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(h, mode)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr) // the Enter key wasn't echoed either
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
