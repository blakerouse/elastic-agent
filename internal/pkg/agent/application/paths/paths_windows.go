// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows

package paths

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// BinaryName is the name of the installed binary.
	BinaryName = "elastic-agent.exe"

	// DefaultBasePath is the base path used by the install command
	// for installing Elastic Agent's files.
	DefaultBasePath = `C:\Program Files`

	// ControlSocketPath is the control socket path used when installed.
	ControlSocketPath = `\\.\pipe\elastic-agent-system`

	// ControlSocketRunSymlink is not created on Windows.
	ControlSocketRunSymlink = ""

	// ServiceName is the service name when installed.
	ServiceName = "Elastic Agent"

	// ShellWrapperPath is the path to the installed shell wrapper.
	ShellWrapperPath = "" // no wrapper on Windows

	// ShellWrapper is the wrapper that is installed.
	ShellWrapper = "" // no wrapper on Windows

	// defaultAgentVaultPath is the directory for windows where the vault store is located or the
	defaultAgentVaultPath = "vault"
)

// ArePathsEqual determines whether paths are equal taking case sensitivity of os into account.
func ArePathsEqual(expected, actual string) bool {
	return strings.EqualFold(expected, actual)
}

// AgentVaultPath is the directory that contains all the files for the value
func AgentVaultPath() string {
	return filepath.Join(Config(), defaultAgentVaultPath)
}

func initialControlSocketPath(topPath string) string {
	// when installed the control address is fixed
	if RunningInstalled() {
		return ControlSocketPath
	}
	// entire string cannot be longer than 256 characters, this forces the
	// length to always be 87 characters (but unique per data path)
	return fmt.Sprintf(`\\.\pipe\elastic-agent-%x`, sha256.Sum256([]byte(Data())))
}
