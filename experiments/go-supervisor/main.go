// Command go-supervisor is a non-shipping benchmark prototype. It exercises
// the small process-and-heartbeat loop being evaluated for a possible future
// Ruk supervisor without duplicating the production CLI.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type fixtureState struct {
	AssignmentID string `json:"assignmentId"`
	HeartbeatAt  string `json:"heartbeatAt"`
}

func readState(filename string) (fixtureState, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return fixtureState{}, err
	}
	var state fixtureState
	if err := json.Unmarshal(data, &state); err != nil {
		return fixtureState{}, err
	}
	if state.AssignmentID == "" {
		return fixtureState{}, errors.New("fixture assignmentId must not be empty")
	}
	return state, nil
}

func writeState(filename string, state fixtureState) error {
	directory := filepath.Dir(filename)
	temporary, err := os.CreateTemp(directory, ".ruk-go-heartbeat-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(state); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filename)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 1
}

func supervise(stateFile string, heartbeatEvery time.Duration, command []string) (code int, resultErr error) {
	state, err := readState(stateFile)
	if err != nil {
		return 1, fmt.Errorf("read fixture state: %w", err)
	}
	if len(command) == 0 {
		return 1, errors.New("a child command is required after --")
	}

	child := exec.Command(command[0], command[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		return 1, fmt.Errorf("start child: %w", err)
	}
	waited := false
	defer func() {
		if waited || child.Process == nil {
			return
		}
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	wait := make(chan error, 1)
	go func() { wait <- child.Wait() }()
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)

	for {
		select {
		case err := <-wait:
			waited = true
			return exitCode(err), nil
		case observed := <-ticker.C:
			state.HeartbeatAt = observed.UTC().Format(time.RFC3339Nano)
			if err := writeState(stateFile, state); err != nil {
				_ = child.Process.Kill()
				<-wait
				waited = true
				return 1, fmt.Errorf("write heartbeat: %w", err)
			}
		case received := <-interrupts:
			if err := child.Process.Signal(received); err != nil {
				_ = child.Process.Kill()
			}
		}
	}
}

func main() {
	stateFile := flag.String("state", "", "path to a benchmark fixture state file")
	heartbeat := flag.Duration("heartbeat", 100*time.Millisecond, "heartbeat interval")
	flag.Parse()
	if *stateFile == "" || *heartbeat <= 0 {
		fmt.Fprintln(os.Stderr, "go-supervisor: --state and a positive --heartbeat are required")
		os.Exit(2)
	}
	code, err := supervise(*stateFile, *heartbeat, flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "go-supervisor: %v\n", err)
	}
	os.Exit(code)
}
