package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/efficientgo/core/backoff"
	"github.com/efficientgo/e2e"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const imageName = "nudl:e2e"

func kubectl(e *e2e.KindEnvironment, args ...string) *exec.Cmd {
	return exec.Command("kubectl", append([]string{"--kubeconfig", fmt.Sprintf("%s/kubeconfig", e.SharedDir())}, args...)...)
}

func kubectlRun(t *testing.T, e *e2e.KindEnvironment, args ...string) {
	cmd := kubectl(e, args...)
	w := &bytes.Buffer{}
	cmd.Stderr = w
	cmd.Stdout = w

	require.NoError(t, cmd.Run(), w.String())
}

func TestMain(t *testing.T) {
	e, err := e2e.NewKindEnvironment()
	require.NoError(t, err)
	t.Cleanup(e.Close)

	runnableBuilder := e.Runnable(strings.ToLower(t.Name()))
	runnable := runnableBuilder.Init(e2e.StartOptions{
		Image: imageName,
	})
	_ = runnable.Start() // lazy hack to get image loaded into the cluster

	kubectlRun(t, e, "apply", "-f", "e2e.yaml")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	bo := backoff.New(ctx, backoff.Config{})
	for {
		var err error
		if bo.Wait(); bo.Ongoing() {
			cmd := kubectl(e, "wait", "pod", "--for", "condition=Ready", "--selector", "app.kubernetes.io/name=nudl")
			if err = cmd.Run(); err == nil {
				break
			}
		} else {
			require.NoError(t, err, "timeout waiting for daemonset")
			break
		}
	}

	{
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		bo := backoff.New(ctx, backoff.Config{})
		found := false
		for {
			if bo.Wait(); bo.Ongoing() {
				cmd := exec.Command("kubectl", "--kubeconfig", fmt.Sprintf("%s/kubeconfig", e.SharedDir()), "get", "nodes", fmt.Sprintf("%s-control-plane", e.Name()), "-o", "jsonpath={.metadata.labels}")
				w := &bytes.Buffer{}
				cmd.Stderr = w
				cmd.Stdout = w
				require.NoError(t, cmd.Run(), w.String())
				labels := map[string]string{}
				t.Logf("buffer %s\n", w.String())

				require.NoError(t, json.NewDecoder(w).Decode(&labels), w.String())

				for key, value := range labels {
					if strings.HasPrefix(key, "nudl.squat.ai") {
						found = true
						t.Logf("found label %s=%s\n", key, value)
						break
					}
				}
			} else {
				break
			}
		}
		assert.True(t, found, "no label found")
		require.NoError(t, err, "timeout waiting for daemonset")
	}
}
