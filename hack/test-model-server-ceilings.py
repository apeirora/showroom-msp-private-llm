#!/usr/bin/env python3
"""Check provider policy flags and reject invalid Helm inputs."""
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "charts/private-llm-operator"

def render(*settings):
    return subprocess.run(["helm", "template", "ceiling-test", str(CHART),
                           "--show-only", "templates/manager-deployment.yaml",
                           "--set", "ocm.enabled=false", "--set", "traefik.enabled=false",
                           *settings], capture_output=True, text=True, check=False)

flags = {"cpuMilli": "max-model-cpu-milli", "memoryMiB": "max-model-memory-mib",
         "contextSize": "max-context-size", "parallelSlots": "max-parallel-slots"}
default = render()
assert default.returncode == 0, default.stderr
for flag in flags.values():
    assert f"--{flag}=0" in default.stdout
values = {"cpuMilli": 6000, "memoryMiB": 8192, "contextSize": 16384, "parallelSlots": 4}
settings = [arg for key, value in values.items()
            for arg in ("--set", f"modelServerCeilings.{key}={value}")]
raised = render(*settings)
assert raised.returncode == 0, raised.stderr
for key, flag in flags.items():
    assert f"--{flag}={values[key]}" in raised.stdout
for setting in ("cpuMilli=16001", "memoryMiB=-1", "contextSize=131073", "parallelSlots=17"):
    invalid = render("--set", f"modelServerCeilings.{setting}")
    assert invalid.returncode != 0, setting
    assert "modelServerCeilings" in invalid.stderr, invalid.stderr
invalid = render("--set-string", "modelServerCeilings.cpuMilli=6000")
assert invalid.returncode != 0, "quoted CPU value must fail the integer schema"
print("PASS: provider ceiling defaults, explicit flags, and invalid Helm inputs")
