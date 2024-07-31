package ansible

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/redhatinsights/rhc-worker-playbook/internal/constants"
	"github.com/redhatinsights/rhc-worker-playbook/internal/exec"
	"github.com/rjeczalik/notify"
)

// Event represents data from an Ansible Runner event.
type Event struct {
	Event       string `json:"event"`
	UUID        string `json:"uuid"`
	Counter     int    `json:"counter"`
	Stdout      string `json:"stdout"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	RunnerIdent string `json:"runner_ident"`
	Created     string `json:"created"`
	EventData   struct {
		CRCDispatcherCorrelationID string `json:"crc_dispatcher_correlation_id"`
		CRCDispatcherErrorCode     string `json:"crc_dispatcher_error_code"`
		CRCDispatcherErrorDetails  string `json:"crc_dispatcher_error_details"`
	} `json:"event_data"`
}

// RunPlaybook creates an ansible-runner job to run the provided playbook. The
// function returns a channel over which the caller can receive ansible-runner
// events as they happen. The channel is closed when the job completes.
func RunPlaybook(id string, playbook []byte, correlationID string) (chan Event, error) {
	privateDataDir := filepath.Join(constants.StateDir, id)

	if err := os.WriteFile(filepath.Join(constants.StateDir, id+".yaml"), playbook, 0600); err != nil {
		return nil, fmt.Errorf("cannot write playbook to temp file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(privateDataDir, "artifacts", id, "job_events"), 0755); err != nil {
		return nil, fmt.Errorf("cannot create private state dir: %v", err)
	}

	events := make(chan Event)

	// publish an "executor_on_start" event to signal cloud connector that a run
	// event has started. This is run on a goroutine in case the events
	// channel doesn't have a receiver connected yet to avoid blocking the
	// continuation of this function.
	go func() {
		events <- Event{
			Event:     "executor_on_start",
			UUID:      uuid.New().String(),
			Counter:   -1,
			Stdout:    "",
			StartLine: 0,
			EndLine:   0,
			EventData: struct {
				CRCDispatcherCorrelationID string `json:"crc_dispatcher_correlation_id"`
				CRCDispatcherErrorCode     string `json:"crc_dispatcher_error_code"`
				CRCDispatcherErrorDetails  string `json:"crc_dispatcher_error_details"`
			}{
				CRCDispatcherCorrelationID: correlationID,
			},
		}
	}()

	// started is a closure that's passed to the StartProcess function as its
	// exec.ProcessStartedFunc handler. This function is invoked on a goroutine
	// after the process has been started. It exists as a named closure within
	// this function to capture some relevant values that aren't otherwise
	// passed in as part of the exec.ProcessStartedFunc signature (id,
	// privateDataDir, etc.).
	started := func(pid int, stdout, stderr io.ReadCloser) {
		slog.Debug("process started", "pid", pid, "runner_ident", id)

		// start a goroutine to watch for event files being written to the
		// job_events directory. When a relevant event file is detected, it gets
		// marshaled into JSON and sent to the events channel.
		go func() {
			c := make(chan notify.EventInfo, 1)
			if err := notify.Watch(filepath.Join(privateDataDir, "artifacts", id, "job_events"), c, notify.InMovedTo); err != nil {
				slog.Error(
					"failed to watch private data dir",
					"dir",
					privateDataDir,
					"err",
					err,
				)
				return
			}
			defer notify.Stop(c)

			for e := range c {
				slog.Debug("notify event", "event", e.Event(), "path", e.Path())
				switch e.Event() {
				case notify.InMovedTo:
					if strings.HasSuffix(e.Path(), ".json") {
						data, err := os.ReadFile(e.Path())
						if err != nil {
							slog.Error("cannot read file", "file", e.Path(), "error", err)
							continue
						}

						var event Event
						if err := json.Unmarshal(data, &event); err != nil {
							slog.Error("cannot unmarshal data", "data", data, "error", err)
							continue
						}

						events <- event
						slog.Info("event sent", "event", event)
					}
				}
			}
		}()

		// start a goroutine that watches for the "status" file. When it gets
		// written to, its contents are read, and if the status is "failed", a
		// final "failed" event is written to the events channel. No action is
		// taken if the status is "successful".
		go func() {
			c := make(chan notify.EventInfo, 1)
			if err := notify.Watch(filepath.Join(privateDataDir, "artifacts", id, "status"), c, notify.InCloseWrite); err != nil {
				slog.Error("failed to watch status file", "error", err)
			}
			defer notify.Stop(c)

			for e := range c {
				slog.Debug("notify event", "event", e.Event(), "path", e.Path())
				switch e.Event() {
				case notify.InCloseWrite:
					data, err := os.ReadFile(e.Path())
					if err != nil {
						slog.Error("failed to read status file", "error", err)
						continue
					}
					switch string(data) {
					case "failed":
						// publish an "executor_on_failed" event to signal
						// cloud connector that a run has failed.
						events <- Event{
							Event:     "executor_on_failed",
							UUID:      uuid.New().String(),
							Counter:   -1,
							StartLine: 0,
							EndLine:   0,
							EventData: struct {
								CRCDispatcherCorrelationID string `json:"crc_dispatcher_correlation_id"`
								CRCDispatcherErrorCode     string `json:"crc_dispatcher_error_code"`
								CRCDispatcherErrorDetails  string `json:"crc_dispatcher_error_details"`
							}{
								CRCDispatcherCorrelationID: correlationID,
								CRCDispatcherErrorCode:     "UNDEFINED_ERROR",
							},
						}
					case "successful":
					default:
						slog.Error("unsupported status case", "status", string(data))
					}
					close(events)
				}
			}
		}()

		// Block the remainder of the routine until the process exits. When it
		// does, clean up.
		err := exec.WaitProcess(pid, func(pid int, state *os.ProcessState) {
			slog.Debug("process stopped", "pid", pid, "runner_ident", id)
			if err := os.Remove(filepath.Join(constants.StateDir, id+".yaml")); err != nil {
				slog.Error(
					"cannot remove file",
					"file",
					filepath.Join(constants.StateDir, id+".yaml"),
					"error",
					err,
				)
			}
			close(events)
		})
		if err != nil {
			slog.Error("process stopped with error", "err", err)
			return
		}
	}

	err := exec.StartProcess(
		"/usr/bin/python3",
		[]string{
			"-m",
			"ansible_runner",
			"run",
			"--ident",
			id,
			"--json",
			"--playbook",
			filepath.Join(constants.StateDir, id+".yaml"),
			privateDataDir,
		},
		[]string{
			"PATH=/sbin:/bin:/usr/sbin:/usr/bin",
			"PYTHONPATH=" + filepath.Join(constants.LibDir, "rhc-worker-playbook"),
			"ANSIBLE_COLLECTIONS_PATH=" + filepath.Join(
				constants.DataDir,
				"rhc-worker-playbook",
				"ansible",
				"collections",
				"ansible_collections",
			),
		},
		started,
	)
	if err != nil {
		return nil, fmt.Errorf("cannot start process: %v", err)
	}

	return events, nil
}
