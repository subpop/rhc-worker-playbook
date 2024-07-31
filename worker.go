package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/redhatinsights/rhc-worker-playbook/internal/ansible"
	"github.com/redhatinsights/rhc-worker-playbook/internal/config"
	"github.com/redhatinsights/rhc-worker-playbook/internal/exec"
	"github.com/redhatinsights/yggdrasil/ipc"
	"github.com/redhatinsights/yggdrasil/worker"
)

func rx(
	w *worker.Worker,
	addr string,
	id string,
	responseTo string,
	metadata map[string]string,
	data []byte,
) error {
	slog.Info("message received", "message-id", id)

	returnURL, has := metadata["return_url"]
	if !has {
		return fmt.Errorf("invalid metadata: missing return_url")
	}

	correlationID, has := metadata["crc_dispatcher_correlation_id"]
	if !has {
		return fmt.Errorf("invalid metadata: missing crc_dispatcher_correlation_id")
	}

	if config.DefaultConfig.VerifyPlaybook {
		d, err := verifyPlaybook(data, config.DefaultConfig.InsightsCoreGPGCheck)
		if err != nil {
			return fmt.Errorf("cannot verify playbook: %v", err)
		}
		data = d
	}

	events, err := ansible.RunPlaybook(id, data, correlationID)
	if err != nil {
		return fmt.Errorf("cannot run playbook: %v", err)
	}

	// start a goroutine that receives ansible-runner events as they are
	// emitted.
	go func() {
		// TODO: support metadata["response_interval"] batch processing
		for event := range events {
			slog.Debug("ansible-runner event", "event", event)

			responseData, err := json.Marshal(event)
			if err != nil {
				slog.Error("cannot marshal event", "error", err)
				continue
			}

			err = w.EmitEvent(ipc.WorkerEventNameWorking, event.UUID, id, map[string]string{
				"message": string(responseData),
			})
			if err != nil {
				slog.Error("cannot emit event", "event", ipc.WorkerEventNameWorking, "error", err)
				continue
			}

			_, _, _, err = w.Transmit(
				returnURL,
				event.UUID,
				id,
				map[string]string{},
				responseData,
			)
			if err != nil {
				slog.Error("cannot transmit data", "error", err)
				continue
			}
		}
	}()

	return nil
}

// verifyPlaybook calls out via subprocess to insights-client's
// ansible.playbook_verifier Python module, passes data as the process's
// standard input. If the playbook passes verification, the playbook, stripped
// of "insights_signature" variables is returned.
func verifyPlaybook(data []byte, insightsCoreGPGCheck bool) ([]byte, error) {
	env := []string{
		"PATH=/sbin:/bin:/usr/sbin:/usr/bin",
	}
	args := []string{
		"-m",
		"insights.client.apps.ansible.playbook_verifier",
		"--quiet",
		"--payload",
		"noop",
		"--content-type",
		"noop",
	}
	if !insightsCoreGPGCheck {
		args = append(args, "--no-gpg")
		env = append(env, "BYPASS_GPG=True")
	}
	stdin := bytes.NewReader(data)
	stdout, stderr, code, err := exec.RunProcess("/usr/bin/insights-client", args, env, stdin)
	if err != nil {
		slog.Debug(
			"cannot verify playbook",
			"code",
			code,
			"stdout",
			string(stdout),
			"stderr",
			string(stderr),
		)
		return nil, fmt.Errorf("cannot verify playbook: %v", err)
	}

	if code > 0 {
		return nil, fmt.Errorf("playbook verification failed: %v", string(stderr))
	}

	yaml.RegisterCustomUnmarshaler[bool](func(b1 *bool, b2 []byte) error {
		if strings.ToLower(string(b2)) == "yes" || strings.ToLower(string(b2)) == "on" ||
			strings.ToLower(string(b2)) == "true" {
			*b1 = true
		} else {
			*b1 = false
		}
		return nil
	})

	yaml.RegisterCustomMarshaler[bool](func(b bool) ([]byte, error) {
		if b {
			return []byte("yes"), nil
		}
		return []byte("no"), nil
	})

	type Playbook struct {
		Name   string                 `yaml:"name"`
		Hosts  string                 `yaml:"hosts"`
		Become bool                   `yaml:"become"`
		Vars   map[string]interface{} `yaml:"vars"`
		Tasks  []yaml.MapSlice        `yaml:"tasks"`
	}
	var playbook []Playbook
	if err := yaml.UnmarshalWithOptions(stdout, &playbook); err != nil {
		return nil, fmt.Errorf("cannot unmarshal playbook: %v", err)
	}
	// ansible-runner returns errors when handed binary field values, so
	// remove it before handing off the playbook to ansible-runner.
	delete(playbook[0].Vars, "insights_signature")

	playbookData, err := yaml.MarshalWithOptions(playbook, yaml.IndentSequence(false))
	if err != nil {
		return nil, fmt.Errorf("cannot marshal playbook: %v", err)
	}

	return playbookData, nil
}
