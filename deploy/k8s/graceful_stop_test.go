package k8s

import (
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/wiebe-xyz/spanbarn/internal/queue"
)

// A RollingUpdate deployment sleeps in preStop so the old pod keeps its
// listener while endpoint removal propagates and the surge pod serves the rest.
// SIGTERM only comes after the sleep and the grace period includes it, so the
// grace period must leave room for the sleep plus the binary's own shutdown
// (10s of HTTP shutdown at least).
//
// A Recreate deployment must not sleep: its one pod leaves the endpoints at
// once and no replacement exists yet, so the sleep only adds downtime.
const shutdownHeadroomSeconds = 20

type deployment struct {
	Spec struct {
		Replicas int `yaml:"replicas"`
		Strategy struct {
			Type string `yaml:"type"`
		} `yaml:"strategy"`
		Template struct {
			Spec struct {
				TerminationGracePeriodSeconds int `yaml:"terminationGracePeriodSeconds"`
				Containers                    []struct {
					Name      string `yaml:"name"`
					Lifecycle struct {
						PreStop struct {
							Sleep struct {
								Seconds int `yaml:"seconds"`
							} `yaml:"sleep"`
						} `yaml:"preStop"`
					} `yaml:"lifecycle"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// Each queue's processing list has one consumer, so whatever a writer finds there
// while it holds no batch was left behind. That rests on one writer pod,
// replaced only after the old one has gone on a rollout, and on the reclaim delay
// outlasting the drain of a pod replaced outside a rollout (deleted or evicted).
func TestWriterReclaimAssumptions(t *testing.T) {
	raw, err := os.ReadFile("base/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var d deployment
	if err := yaml.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Replicas != 1 {
		t.Errorf("writer replicas = %d, want 1", d.Spec.Replicas)
	}
	if d.Spec.Strategy.Type != "Recreate" {
		t.Errorf("writer strategy = %q, want Recreate", d.Spec.Strategy.Type)
	}
	grace := time.Duration(d.Spec.Template.Spec.TerminationGracePeriodSeconds) * time.Second
	if queue.ReclaimDelay <= grace {
		t.Errorf("queue.ReclaimDelay %v does not outlast the writer grace period %v", queue.ReclaimDelay, grace)
	}
}

func TestPreStopSleepMatchesRolloutStrategy(t *testing.T) {
	for _, file := range []string{"base/deployment.yaml", "base/deployment-ingest.yaml"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var d deployment
		if err := yaml.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		pod := d.Spec.Template.Spec
		if len(pod.Containers) == 0 {
			t.Fatalf("%s: no containers", file)
		}
		sleep := pod.Containers[0].Lifecycle.PreStop.Sleep.Seconds
		if d.Spec.Strategy.Type == "Recreate" {
			if sleep != 0 {
				t.Errorf("%s: Recreate pods must not sleep in preStop (%ds adds downtime)", file, sleep)
			}
			continue
		}
		if sleep <= 0 {
			t.Errorf("%s: no preStop sleep; rollouts refuse connections while the endpoint is removed", file)
		}
		if pod.TerminationGracePeriodSeconds < sleep+shutdownHeadroomSeconds {
			t.Errorf("%s: terminationGracePeriodSeconds %d leaves under %ds after the %ds preStop sleep",
				file, pod.TerminationGracePeriodSeconds, shutdownHeadroomSeconds, sleep)
		}
	}
}
