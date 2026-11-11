#!/usr/bin/env python3
"""Exercise installed CLI/helper bytes in private state, retaining live policy.

Python avoids compiling an acceptance driver on the Mac. No production lease or
service is changed. Only children started by this process may be cancelled.
The caller supplies a new, empty directory for receipts and fixture state.
"""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    root = args.out.resolve()
    root.mkdir(parents=True, exist_ok=False)
    state = root / "state"
    state.mkdir()
    live = Path.home() / ".nicos-dev/session-pressure"
    policy = (live / "policy.json").read_bytes()
    (state / "policy.json").write_bytes(policy)
    artifact = json.loads((live / "installed-artifact.json").read_text())
    helper = Path(artifact["path"])
    assert hashlib.sha256(helper.read_bytes()).hexdigest() == artifact["sha256"]
    destination = state / "artifacts" / ("sha256-" + artifact["sha256"]) / helper.name
    destination.parent.mkdir(parents=True)
    shutil.copy2(helper, destination)
    artifact["path"] = str(destination)
    (state / "installed-artifact.json").write_text(json.dumps(artifact) + "\n")
    cli = shutil.which("session-pressure")
    assert cli, "installed CLI is required"
    env = dict(os.environ, NDEV_SESSION_PRESSURE_HOME=str(state))
    # The installed standalone CLI owns this private integration, independently
    # of any parent front-door wrapper stamp.
    env.pop("NDEV_PRESSURE_WRAPPER", None)
    children = []
    handles = []
    receipts = []
    controlled = (
        "import pathlib,sys,time; p=pathlib.Path(sys.argv[1]); "
        "deadline=time.monotonic()+45\n"
        "while not p.exists() and time.monotonic()<deadline: time.sleep(.02)\n"
        "sys.exit(0 if p.exists() else 90)"
    )

    def command(*argv):
        result = subprocess.run([cli, "--json", *argv], env=env,
                                capture_output=True, text=True, timeout=10)
        if argv[:2] == ("work", "cancel"):
            (root / "cancel-command.log").write_text(result.stdout + result.stderr)
        assert result.returncode == 0, "installed command refused; inspect private receipt"
        return json.loads(result.stdout)

    def status():
        result = command("work", "status")["work"]
        assert result["used"] == sum(x["weight"] for x in result.get("leases", []))
        assert 0 <= result["used"] <= result["capacity"]
        return result

    def wait_for(predicate, label, seconds=35):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            current = status()
            if predicate(current):
                return current
            time.sleep(.05)
        raise AssertionError(label + "; host-pressure holds are not scheduler failures")

    def start(name, work_class):
        gate = root / (name + ".release")
        log = (root / (name + ".log")).open("w")
        handles.append(log)
        process = subprocess.Popen(
            [cli, "work", "run", "--class", work_class, "--cohort", "fixture",
             "--no-reuse", "--wait", "30s", "--progress", "jsonl", "--",
             sys.executable, "-c", controlled, str(gate)],
            env=env, stdout=log, stderr=subprocess.STDOUT)
        children.append((process, gate))
        return process, gate

    def finish(child, expected=0):
        process, gate = child
        gate.touch()
        code = process.wait(timeout=10)
        assert code == expected, "fixture child exit differs from expected outcome"

    def belongs(pid, ancestor):
        for _ in range(30):
            if pid == ancestor:
                return True
            if pid <= 1:
                return False
            value = subprocess.check_output(["ps", "-o", "ppid=", "-p", str(pid)], text=True).strip()
            if not value:
                return False
            pid = int(value)
        return False

    try:
        owner = start("release-owner", "benchmark-exclusive")
        occupied = wait_for(lambda s: s["used"] == s["capacity"], "exclusive owner did not acquire")
        stale_state = copy.deepcopy(json.loads((state / "work-leases.json").read_text()))
        dead_pid = occupied["leases"][0]["pid"]
        follower = start("release-follower", "browser")
        wait_for(lambda s: s["queue_depth"] == 1, "follower did not queue")
        released = time.monotonic()
        finish(owner)
        wait_for(lambda s: len(s.get("leases", [])) == 1 and s["leases"][0]["class"] == "browser",
                 "release did not wake follower")
        receipts.append({"case": "release_wakeup", "observed_ms": round((time.monotonic()-released)*1000)})
        finish(follower)
        wait_for(lambda s: s["used"] == 0 and s["queue_depth"] == 0, "release leaked state")

        owner = start("cancel-owner", "browser")
        current = wait_for(lambda s: len(s.get("leases", [])) == 1, "cancel fixture did not acquire")
        lease = current["leases"][0]
        assert belongs(lease["pid"], owner[0].pid), "refuse to signal a process outside this fixture"
        cancelled = command("work", "cancel", "--operation-id", lease["operation_id"], "--confirm")
        assert cancelled.get("ok")
        assert owner[0].wait(timeout=10) != 0
        wait_for(lambda s: s["used"] == 0, "cancel leaked capacity")
        receipts.append({"case": "explicit_owned_cancellation", "capacity_recovered": True})

        # Restore an actual acquired lease after its fixture owner has exited.
        # This tests stale persisted-owner pruning, not an observed OS crash.
        try:
            os.kill(dead_pid, 0)
        except ProcessLookupError:
            pass
        else:
            raise AssertionError("dead-owner PID was reused; refuse stale fixture")
        (state / "work-leases.json").write_text(json.dumps(stale_state) + "\n")
        recovered = status()
        assert recovered["used"] == 0 and not recovered.get("leases")
        receipts.append({"case": "persisted_dead_owner", "capacity_recovered": True})

        # Browser jobs also hold a separate serial execution lock. Test
        # fixtures exercise concurrent weighted admission without that lock.
        owners = [start("concurrent-" + str(i), "test") for i in range(4)]
        wait_for(lambda s: s["used"] == 8 and len(s.get("leases", [])) == 4,
                 "concurrent fixtures did not fill the live capacity")
        follower = start("concurrent-follower", "heavy")
        wait_for(lambda s: s["queue_depth"] == 1, "concurrent follower did not queue")
        released = time.monotonic()
        for _, gate in owners:
            gate.touch()
        for child in owners:
            finish(child)
        wait_for(lambda s: len(s.get("leases", [])) == 1 and s["leases"][0]["class"] == "heavy",
                 "concurrent release did not wake follower")
        receipts.append({"case": "concurrent_release", "owners": 4,
                         "observed_ms": round((time.monotonic()-released)*1000)})
        finish(follower)
        final = wait_for(lambda s: s["used"] == 0 and s["queue_depth"] == 0, "final state leaked")
        assert (state / "policy.json").read_bytes() == policy
        result = {"ok": True, "source_revision": artifact["vcs_revision"],
                  "helper_sha256": artifact["sha256"], "live_policy_unchanged": True,
                  "private_state": True, "cases": receipts, "final_used": final["used"]}
        (root / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result))
    finally:
        for _, gate in children:
            gate.touch()
        for process, _ in children:
            try:
                process.wait(timeout=50)
            except subprocess.TimeoutExpired:
                # Never send a broad cleanup signal. The fixture child has its
                # own finite deadline; preserve evidence if its helper is stuck.
                print("fixture helper remains live; inspect the private receipt", file=sys.stderr)
        for handle in handles:
            handle.close()


if __name__ == "__main__":
    main()
