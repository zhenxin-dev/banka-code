package tools

import (
	"strings"
	"testing"
)

const sandboxTestWorkspace = "/tmp/banka-sandbox-test"

func TestValidateCommandAllowsWorkspaceCommands(t *testing.T) {
	tests := []string{
		"echo hello",
		"go test ./...",
		"git status",
		"ls internal/",
		"cat README.md",
		"chmod +x build.sh",
		"echo test > output.txt",
		"echo test >> output.txt",
		"export FOO=bar",
		"",
		"   ",
		"echo hello | grep hello",
		"cat " + sandboxTestWorkspace + "/README.md",
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			if got := validateCommand(command, sandboxTestWorkspace); got != "" {
				t.Fatalf("validateCommand(%q) = %q, want allowed", command, got)
			}
		})
	}
}

func TestValidateCommandRejectsEscapesAndDangerousOperations(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"ls /etc", "/etc"},
		{"cat /etc/passwd", "/etc/passwd"},
		{"rm -rf /", "/"},
		{`cat C:\Windows\System32\config`, "Windows absolute paths"},
		{"ls /tmp/exploit", "/tmp/exploit"},
		{"cat ../../secret", "../../secret"},
		{"ls ../outside", "../outside"},
		{"cat ../../../etc/passwd", "../../../etc/passwd"},
		{"cd /tmp", "/tmp"},
		{"cd ../..", "../.."},
		{"echo x > /etc/passwd", "/etc/passwd"},
		{"echo x >> /tmp/exploit", "/tmp/exploit"},
		{"export PATH=/evil", "PATH"},
		{"export LD_PRELOAD=/evil.so", "LD_PRELOAD"},
		{"export HOME=/evil", "HOME"},
		{"PATH=/evil cmd", "PATH"},
		{"sudo rm -rf /", "sudo"},
		{"su root", "su"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			got := validateCommand(test.command, sandboxTestWorkspace)
			if got == "" || !strings.Contains(got, test.want) {
				t.Fatalf("validateCommand(%q) = %q, want rejection containing %q", test.command, got, test.want)
			}
		})
	}
}
