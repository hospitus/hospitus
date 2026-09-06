package cmdutil

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ConfirmDownload decides whether to fetch a missing image.
//
// With --pull it answers yes without asking. Without it, a terminal is asked
// and anything else is declined without printing a prompt: a script, a CI job
// or a test harness has no one to answer, and AskYesNo would read EOF and
// decline anyway — after emitting a "[y/N]:" that never had a reader, which
// reads in a log like a question someone ignored.
func ConfirmDownload(prompt string, pull bool) bool {
	if pull {
		return true
	}
	if !stdinIsTerminal() {
		fmt.Println("Not running on a terminal, so the download was not offered. " +
			"Pass --pull to fetch missing images without asking.")
		return false
	}
	return AskYesNo(prompt)
}

// stdinIsTerminal reports whether standard input is a terminal. A pipe, a file
// or a closed descriptor is not, and os.Stat is enough to tell them apart
// without pulling in a terminal library.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// AskYesNo prompts the user for a yes/no answer.
func AskYesNo(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// IsImageNotFoundError checks if an error is an "image not found" error.
func IsImageNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "image") && strings.Contains(errStr, "not found")
}

// Plural returns the "s" that a count of n needs, so a total reads "1 image"
// rather than "1 images".
func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
