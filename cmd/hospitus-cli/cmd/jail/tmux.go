package jail

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newTmuxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tmux <jail-name> [session-name]",
		Short: "Attach to a tmux session inside a jail",
		Long: `Attach to a tmux session inside a jail for persistent shell access.

Tmux sessions persist across disconnects, making them ideal for:
  - Long-running provisioning tasks (pkg install, etc.)
  - Sessions that need to survive connection drops
  - Multi-window/pane workflows

If no session exists, one is created automatically.
If tmux is not installed, you'll be prompted to install it.

Subcommands:
  list    - List active tmux sessions in a jail
  kill    - Kill a tmux session
  send    - Send a command to a tmux session

NOTE: This command runs locally via jexec (not through the API).

Examples:
  # Attach to default session (named after jail)
  hospitus jail tmux myjail

  # Attach to named session
  hospitus jail tmux myjail mysession

  # List sessions
  hospitus jail tmux list myjail

  # Kill a session
  hospitus jail tmux kill myjail mysession

  # Send a command to a session
  hospitus jail tmux send myjail "pkg install -y nginx"`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runTmuxAttach,
	}

	// Add subcommands
	cmd.AddCommand(newTmuxListCommand())
	cmd.AddCommand(newTmuxKillCommand())
	cmd.AddCommand(newTmuxSendCommand())

	return cmd
}

func newTmuxListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list <jail-name>",
		Short: "List tmux sessions in a jail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTmuxList(cmd, args[0])
		},
	}
}

func newTmuxKillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <jail-name> <session-name>",
		Short: "Kill a tmux session in a jail",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTmuxKill(cmd, args[0], args[1])
		},
	}
}

func newTmuxSendCommand() *cobra.Command {
	var session string

	cmd := &cobra.Command{
		Use:   "send <jail-name> <command>",
		Short: "Send a command to a tmux session",
		Long: `Send a command to a tmux session in a jail.

The command is sent followed by Enter to execute it.
Use this for automated provisioning or background tasks.

Examples:
  # Send to default session
  hospitus jail tmux send myjail "pkg install -y nginx"

  # Send to specific session
  hospitus jail tmux send myjail "make install" --session build`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTmuxSend(cmd, args[0], args[1], session)
		},
	}

	cmd.Flags().StringVarP(&session, "session", "s", "", "Session name (default: jail name)")

	return cmd
}

// runTmuxAttach attaches to a tmux session in the jail
func runTmuxAttach(cmd *cobra.Command, args []string) error {
	jailName := args[0]
	sessionName := jailName // Default session name is jail name
	if len(args) > 1 {
		sessionName = args[1]
	}

	// Check if jexec command exists
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return fmt.Errorf("'jexec' command not found - tmux requires running on the FreeBSD host")
	}

	hasTmux, err := checkTmuxInstalled(jailName)
	if err != nil {
		return fmt.Errorf("failed to check tmux: %w", err)
	}

	if !hasTmux {
		fmt.Fprintln(os.Stderr, "tmux is not installed in the jail.")
		fmt.Fprintln(os.Stderr, "Install it with: hospitus jail exec "+jailName+" 'pkg install -y tmux'")
		fmt.Fprint(os.Stderr, "Install now? [y/N] ")

		var response string
		_, _ = fmt.Scanln(&response)
		if !strings.EqualFold(response, "y") {
			return fmt.Errorf("tmux not installed")
		}

		// Install tmux
		fmt.Fprintln(cmd.OutOrStdout(), "Installing tmux...")
		installCmd := exec.Command(jexecPath, jailName, "/bin/sh", "-c", "env ASSUME_ALWAYS_YES=yes pkg install -y tmux")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install tmux: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "tmux installed successfully")
	}

	// Check if session exists
	sessions, _ := listTmuxSessions(jailName)
	sessionExists := false
	for _, s := range sessions {
		if s == sessionName {
			sessionExists = true
			break
		}
	}

	// Create session if it doesn't exist
	if !sessionExists {
		fmt.Fprintf(cmd.OutOrStdout(), "Creating new tmux session '%s'...\n", sessionName)
		createCmd := exec.Command(jexecPath, jailName, "tmux", "new-session", "-d", "-s", sessionName)
		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("failed to create tmux session: %w", err)
		}
	}

	fmt.Fprintln(os.Stderr, "Note: tmux session runs locally via jexec (not through the API)")
	fmt.Fprintf(cmd.OutOrStdout(), "Attaching to tmux session '%s' in jail %s\n", sessionName, jailName)
	fmt.Fprintln(cmd.OutOrStdout(), "Detach with: Ctrl+b d")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	tmuxCmd := exec.Command(jexecPath, jailName, "tmux", "attach", "-t", sessionName)
	tmuxCmd.Stdin = os.Stdin
	tmuxCmd.Stdout = os.Stdout
	tmuxCmd.Stderr = os.Stderr

	return tmuxCmd.Run()
}

// runTmuxList lists tmux sessions in the jail
func runTmuxList(cmd *cobra.Command, jailName string) error {
	sessions, err := listTmuxSessionsFull(jailName)
	if err != nil {
		return err
	}

	if len(sessions) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No tmux sessions found in jail", jailName)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Tmux sessions in jail %s:\n", jailName)
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-10s %-10s %s\n", "SESSION", "WINDOWS", "ATTACHED", "CREATED")
	fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("-", 60))

	for _, s := range sessions {
		attached := "no"
		if s.attached {
			attached = "yes"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-10d %-10s %s\n", s.name, s.windows, attached, s.created)
	}

	return nil
}

// runTmuxKill kills a tmux session in the jail
func runTmuxKill(cmd *cobra.Command, jailName, sessionName string) error {
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return fmt.Errorf("'jexec' command not found - tmux requires running on the FreeBSD host")
	}

	killCmd := exec.Command(jexecPath, jailName, "tmux", "kill-session", "-t", sessionName)
	output, err := killCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to kill tmux session: %s", strings.TrimSpace(string(output)))
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Killed tmux session '%s' in jail %s\n", sessionName, jailName)
	return nil
}

// runTmuxSend sends a command to a tmux session
func runTmuxSend(cmd *cobra.Command, jailName, command, sessionName string) error {
	if sessionName == "" {
		sessionName = jailName
	}

	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return fmt.Errorf("'jexec' command not found - tmux requires running on the FreeBSD host")
	}

	// Check if session exists
	sessions, _ := listTmuxSessions(jailName)
	sessionExists := false
	for _, s := range sessions {
		if s == sessionName {
			sessionExists = true
			break
		}
	}

	// Create the session rather than refusing. This command exists for
	// provisioning that runs unattended, and the only other way to get a
	// session was to attach to one — which needs a terminal and a person at
	// it. A script following the guide met "tmux session 'web' does not exist"
	// and had nothing non-interactive to do about it.
	if !sessionExists {
		fmt.Fprintf(cmd.OutOrStdout(), "Creating new tmux session '%s'...\n", sessionName)
		createCmd := exec.Command(jexecPath, jailName, "tmux", "new-session", "-d", "-s", sessionName)
		if output, err := createCmd.CombinedOutput(); err != nil {
			// Another process may have created it between the check above and
			// this call — two scripts provisioning the same jail, typically.
			// tmux says "duplicate session", which is the state we wanted.
			if !strings.Contains(string(output), "duplicate session") {
				return fmt.Errorf("failed to create tmux session: %s", strings.TrimSpace(string(output)))
			}
		}
	}

	sendCmd := exec.Command(jexecPath, jailName, "tmux", "send-keys", "-t", sessionName, command, "Enter")
	output, err := sendCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to send command: %s", strings.TrimSpace(string(output)))
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Sent command to tmux session '%s' in jail %s\n", sessionName, jailName)
	fmt.Fprintf(cmd.OutOrStdout(), "Attach to view output: hospitus jail tmux %s %s\n", jailName, sessionName)

	return nil
}

// checkTmuxInstalled checks if tmux is installed in the jail
func checkTmuxInstalled(jailName string) (bool, error) {
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return false, err
	}

	checkCmd := exec.Command(jexecPath, jailName, "which", "tmux")
	err = checkCmd.Run()
	return err == nil, nil
}

// listTmuxSessions lists tmux session names in the jail
func listTmuxSessions(jailName string) ([]string, error) {
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return nil, err
	}

	listCmd := exec.Command(jexecPath, jailName, "tmux", "list-sessions", "-F", "#{session_name}")
	output, err := listCmd.Output()
	if err != nil {
		return []string{}, nil // No sessions
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []string
	for _, line := range lines {
		if line != "" {
			sessions = append(sessions, line)
		}
	}

	return sessions, nil
}

// tmuxSessionInfo holds tmux session details
type tmuxSessionInfo struct {
	name     string
	windows  int
	attached bool
	created  string
}

// formatTmuxCreated renders a session's creation time the way the CREATED
// column of "hospitus jail list" does. Anything unparseable is passed through, so
// a future tmux that reports something else still shows it rather than nothing.
func formatTmuxCreated(raw string) string {
	secs, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return raw
	}
	return time.Unix(secs, 0).Format("2006-01-02 15:04")
}

// listTmuxSessionsFull lists tmux sessions with full details
func listTmuxSessionsFull(jailName string) ([]tmuxSessionInfo, error) {
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return nil, err
	}

	// session_created is a Unix timestamp. Its companion
	// session_created_string was dropped from tmux and expands to nothing on
	// current versions — 3.7b returns an empty field — which left the CREATED
	// column blank for every session.
	listCmd := exec.Command(jexecPath, jailName, "tmux", "list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_attached}:#{session_created}")
	output, err := listCmd.Output()
	if err != nil {
		return []tmuxSessionInfo{}, nil // No sessions
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []tmuxSessionInfo
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 4 {
			continue
		}

		windows := 0
		_, _ = fmt.Sscanf(parts[1], "%d", &windows)

		sessions = append(sessions, tmuxSessionInfo{
			name:     parts[0],
			windows:  windows,
			attached: parts[2] == "1",
			created:  formatTmuxCreated(parts[3]),
		})
	}

	return sessions, nil
}
