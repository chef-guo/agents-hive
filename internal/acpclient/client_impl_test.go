package acpclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestACPClientImplReadTextFileReadsInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("line one\nline two"), 0o644))

	client := newACPClientImpl("remote-1", zap.NewNop())
	client.workspaceRoot = root

	resp, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: path})

	require.NoError(t, err)
	assert.Equal(t, "line one\nline two", resp.Content)
}

func TestACPClientImplReadTextFileRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))

	client := newACPClientImpl("remote-1", zap.NewNop())
	client.workspaceRoot = root

	_, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: outside})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "路径逃逸")
}

func TestACPClientImplRequestPermissionAllowsReadOnlyAndAsksOrDeniesDangerous(t *testing.T) {
	client := newACPClientImpl("remote-1", zap.NewNop())

	readResp, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"},
		},
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: "call_read",
			Title:      acp.Ptr("Read project file"),
			RawInput:   map[string]any{"path": "/tmp/a.txt"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, readResp.Outcome.Selected)
	assert.Equal(t, acp.PermissionOptionId("allow"), readResp.Outcome.Selected.OptionId)

	deleteResp, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"},
		},
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: "call_delete",
			Title:      acp.Ptr("Delete file"),
			RawInput:   map[string]any{"command": "rm -rf /tmp/project"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, deleteResp.Outcome.Selected)
	assert.Equal(t, acp.PermissionOptionId("reject"), deleteResp.Outcome.Selected.OptionId)
}

func TestACPClientImplWriteTextFileCreatesNewFileAndRejectsPathEscapeAndOverwrite(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "notes.txt")
	require.NoError(t, os.WriteFile(existing, []byte("old"), 0o644))
	outside := filepath.Join(t.TempDir(), "secret.txt")
	created := filepath.Join(root, "generated", "notes.md")

	client := newACPClientImpl("remote-1", zap.NewNop())
	client.workspaceRoot = root

	resp, err := client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		SessionId: "s1",
		Path:      created,
		Content:   "generated content",
	})
	require.NoError(t, err)
	assert.NotNil(t, resp.Meta)
	assert.Equal(t, "generated content", string(requireReadFile(t, created)))

	_, err = client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		SessionId: "s1",
		Path:      outside,
		Content:   "secret",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "路径逃逸")

	_, err = client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		SessionId: "s1",
		Path:      existing,
		Content:   "new",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "覆盖已有文件需要人工审批")
	assert.Equal(t, "old", string(requireReadFile(t, existing)))
}

func TestACPClientImplCreateTerminalRunsSafeReadOnlyCommand(t *testing.T) {
	root := t.TempDir()
	client := newACPClientImpl("remote-1", zap.NewNop())
	client.workspaceRoot = root

	resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		SessionId: "s1",
		Command:   "pwd",
		Cwd:       &root,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.TerminalId)

	waitResp, err := client.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{
		SessionId:  "s1",
		TerminalId: resp.TerminalId,
	})
	require.NoError(t, err)
	require.NotNil(t, waitResp.ExitCode)
	assert.Equal(t, 0, *waitResp.ExitCode)

	out, err := client.TerminalOutput(context.Background(), acp.TerminalOutputRequest{
		SessionId:  "s1",
		TerminalId: resp.TerminalId,
	})
	require.NoError(t, err)
	assert.Contains(t, out.Output, root)
	require.NotNil(t, out.ExitStatus)
	require.NotNil(t, out.ExitStatus.ExitCode)
	assert.Equal(t, 0, *out.ExitStatus.ExitCode)

	_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
		SessionId:  "s1",
		TerminalId: resp.TerminalId,
	})
	require.NoError(t, err)
}

func TestACPClientImplCreateTerminalRejectsDangerousCommand(t *testing.T) {
	root := t.TempDir()
	client := newACPClientImpl("remote-1", zap.NewNop())
	client.workspaceRoot = root

	_, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		SessionId: "s1",
		Command:   "rm",
		Args:      []string{"-rf", root},
		Cwd:       &root,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "危险终端命令")
}

func TestACPClientCapabilitiesAdvertiseImplementedSafeSurface(t *testing.T) {
	caps := hiveACPClientCapabilities()

	assert.True(t, caps.Fs.ReadTextFile)
	assert.True(t, caps.Fs.WriteTextFile)
	assert.True(t, caps.Terminal)
}

func requireReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
