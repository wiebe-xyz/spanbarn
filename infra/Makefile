SHELL := /bin/bash

ANSIBLE_ROLES_PATH ?= roles:../rapid-root/infra/roles
INVENTORY ?= inventory.ini

setup-runners:
	ANSIBLE_ROLES_PATH=$(ANSIBLE_ROLES_PATH) ansible-playbook -i $(INVENTORY) github-runner.yml

setup-runners-macos:
	ANSIBLE_ROLES_PATH=$(ANSIBLE_ROLES_PATH) ansible-playbook -i $(INVENTORY) github-runner-macos.yml -K

# Recover a runner that GitHub shows as online/busy but which never executes
# the job it accepted, or one that crash-loops on a deleted registration.
# Re-running setup-runners-macos does NOT fix either: it skips any runner whose
# listener process is alive, and treats a stale .runner file as "configured".
#
#   make restart-runners-macos                        # every runner on this host
#   make restart-runners-macos RUNNER=spanbarn-mac-2  # just one
#
# Add RECONFIGURE=1 when the runner is gone from GitHub's side and needs
# re-registering (requires GITHUB_TOKEN).
RESTART_EXTRA = -e github_runner_force_restart=true
ifdef RUNNER
RESTART_EXTRA += -e '{"github_runner_restart_names":["$(RUNNER)"]}'
endif
ifdef RECONFIGURE
RESTART_EXTRA += -e github_runner_force_reconfigure=true
endif

restart-runners-macos:
	ANSIBLE_ROLES_PATH=$(ANSIBLE_ROLES_PATH) ansible-playbook -i $(INVENTORY) github-runner-macos.yml -K $(RESTART_EXTRA)

ping:
	ansible -i $(INVENTORY) -m ping runners
	ansible -i $(INVENTORY) -m ping macos_runners

help:
	@echo "Available targets:"
	@echo "  setup-runners           Provision Linux GitHub Actions runners"
	@echo "  setup-runners-macos     Provision macOS GitHub Actions runners"
	@echo "  restart-runners-macos   Recover wedged/stale runners (RUNNER=<name>, RECONFIGURE=1)"
	@echo "  ping                    Test runner host connectivity"
