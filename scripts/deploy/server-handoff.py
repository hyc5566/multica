#!/usr/bin/env python3
"""Prepare and switch a same-schema Server; retain old process for explicit drain."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
from urllib.parse import urlparse


def run(*args):
    result = subprocess.run(args, text=True, capture_output=True)
    if result.returncode:
        # Docker errors can include environment values. Never echo arbitrary output.
        raise RuntimeError(f"command failed: {args[0]} {args[1]} (exit {result.returncode})")
    return result.stdout.strip()


def inspect(name):
    return json.loads(run("docker", "inspect", name))[0]


def env(container):
    return dict(item.split("=", 1) for item in container["Config"]["Env"])


def manifests(image):
    return run("docker", "run", "--rm", "--entrypoint", "/bin/sh", image,
               "-c", "cd /app/migrations && sha256sum *.sql | sort")


def ready(name):
    data = json.loads(run("docker", "exec", name, "wget", "-qO-", "http://127.0.0.1:8080/readyz"))
    if data.get("status") != "ok":
        raise RuntimeError("Server readiness failed")


def switch(config, old, new, delay):
    pattern = re.compile(r"(reverse_proxy(?:\s+@backend)?\s+)" + re.escape(old) + r":8080\s*(\{[^}]*\})?")
    matches = list(pattern.finditer(config))
    if not matches:
        raise RuntimeError("Expected a simple reverse_proxy for the active upstream")
    for match in matches:
        block = match.group(2)
        if block and not re.fullmatch(r"\{\s*stream_close_delay\s+\d+[smh]\s*\}", block):
            raise RuntimeError("Nontrivial proxy block: review configuration manually")
    return pattern.sub(lambda m: m.group(1) + new + ":8080 {\n\t\tstream_close_delay " + str(delay) + "s\n\t}\n", config)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("active", "candidate", "image", "caddy", "config", "redis"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--active-upstream", help="Current Caddy Docker DNS alias; defaults to --active")
    parser.add_argument("--confirm-compatible", action="store_true", help="Operator reviewed mixed-version API/worker/session compatibility")
    parser.add_argument("--delay-seconds", type=int, default=300)
    parser.add_argument("--apply", action="store_true", help="Without this flag, only validate and preview")
    args = parser.parse_args()
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", args.image):
        parser.error("--image must be a locally available immutable image ID")
    if args.delay_seconds < 30:
        parser.error("--delay-seconds must be >= 30")
    config_path = Path(args.config).resolve()
    with open(str(config_path) + ".handoff.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        active, proxy, redis = (inspect(name) for name in (args.active, args.caddy, args.redis))
        config_mounts = [mount for mount in proxy["Mounts"] if mount["Destination"] == "/etc/caddy/Caddyfile"]
        if len(config_mounts) != 1 or config_mounts[0]["Type"] != "bind" or Path(config_mounts[0]["Source"]).resolve() != config_path:
            raise RuntimeError("--config must be the actual Caddyfile bind-mount source")
        if not all(item["State"]["Running"] for item in (active, proxy, redis)):
            raise RuntimeError("Active server, proxy and Redis must be running")
        networks = set(active["NetworkSettings"]["Networks"]) & set(proxy["NetworkSettings"]["Networks"]) & set(redis["NetworkSettings"]["Networks"])
        if len(networks) != 1:
            raise RuntimeError("Expected one shared Docker network")
        network = networks.pop()
        variables = env(active)
        redis_url = urlparse(variables.get("REDIS_URL", ""))
        aliases = redis["NetworkSettings"]["Networks"][network].get("Aliases") or []
        allowed_hosts = {args.redis, redis["Name"].lstrip("/"), *aliases}
        if redis_url.scheme != "redis" or redis_url.hostname not in allowed_hosts or redis_url.port not in (None, 6379):
            raise RuntimeError("Active REDIS_URL must reference the inspected shared Redis")
        commands = []
        if redis_url.password:
            from urllib.parse import unquote
            commands.append(["AUTH", *([unquote(redis_url.username)] if redis_url.username else []), unquote(redis_url.password)])
        commands.append(["PING"])
        wire = b""
        for command in commands:
            wire += ("*" + str(len(command)) + "\r\n").encode()
            for part in command:
                data = part.encode()
                wire += ("$" + str(len(data)) + "\r\n").encode() + data + b"\r\n"
        checked = subprocess.run(["docker", "exec", "-i", args.redis, "redis-cli", "--pipe", "--pipe-timeout", "5"], input=wire, capture_output=True)
        if checked.returncode or b"errors: 0," not in checked.stdout:
            raise RuntimeError("Redis authenticated PING failed")
        if variables.get("REALTIME_RELAY_REDIS_URL") not in (None, "", variables["REDIS_URL"]):
            raise RuntimeError("Dedicated relay Redis requires its own reviewed readiness probe")
        if variables.get("REALTIME_RELAY_MODE", "sharded") not in ("", "sharded", "dual"):
            raise RuntimeError("Daemon cross-node wakeup requires sharded relay")
        if manifests(active["Image"]) != manifests(args.image):
            raise RuntimeError("Migration contents differ: same-schema handoff refused")
        ready(args.active)
        original = config_path.read_text()
        proposed = switch(original, args.active_upstream or args.active, args.candidate, args.delay_seconds)
        loaded = json.loads(run("docker", "exec", args.caddy, "wget", "-qO-", "http://127.0.0.1:2019/config/"))
        loaded_proxies = []
        def collect(value):
            if isinstance(value, dict):
                if value.get("handler") == "reverse_proxy" and any(upstream.get("dial") == (args.active_upstream or args.active) + ":8080" for upstream in value.get("upstreams", [])):
                    loaded_proxies.append(value)
                for child in value.values():
                    collect(child)
            elif isinstance(value, list):
                for child in value:
                    collect(child)
        collect(loaded)
        local_route_count = len(re.findall(r"reverse_proxy(?:\s+@backend)?\s+" + re.escape(args.active_upstream or args.active) + r":8080(?:\s|$)", original))
        if len(loaded_proxies) != local_route_count:
            raise RuntimeError("Loaded API routes differ from the supplied Caddyfile")
        if not loaded_proxies or any(proxy.get("stream_close_delay", 0) <= 0 for proxy in loaded_proxies):
            raise RuntimeError("Install stream_close_delay in the ACTIVE Caddy config first; first adoption reconnects old sockets")
        print(json.dumps({"active_image": active["Image"], "candidate_image": args.image,
                          "candidate": args.candidate, "network": network,
                          "old_server_retained": True, "drain_seconds": args.delay_seconds}))
        if not args.apply:
            print("Preview passed. No container started or traffic changed.")
            return
        if not args.confirm_compatible:
            raise RuntimeError("--apply requires --confirm-compatible after mixed-version review")
        # Sensitive environment lives in an owner-only temporary file, never argv/output.
        with tempfile.NamedTemporaryFile(mode="w", prefix="multica-handoff-env-") as secret:
            for key, value in variables.items():
                if "\n" in value or "\r" in value:
                    raise RuntimeError("Multiline environment requires a reviewed Compose deployment")
                if key not in ("MULTICA_INTERNAL_DATABASE_STARTUP_STARTED_AT_UNIX", "HOSTNAME"):
                    secret.write(key + "=" + value + "\n")
            secret.flush()
            command = ["docker", "run", "-d", "--name", args.candidate, "--network", network,
                       "--env-file", secret.name, "--entrypoint", "/app/server", "--workdir", "/app",
                       "--restart", "unless-stopped", "--stop-timeout", "60"]
            for mount in active["Mounts"]:
                if mount["Type"] not in ("bind", "volume"):
                    raise RuntimeError("Unsupported active mount")
                source = mount.get("Name") if mount["Type"] == "volume" else mount["Source"]
                command += ["--mount", "type=" + mount["Type"] + ",source=" + source + ",target=" + mount["Destination"] + ("" if mount["RW"] else ",readonly")]
            run(*command, args.image)
        deadline = time.monotonic() + 60
        while True:
            try:
                ready(args.candidate)
                break
            except RuntimeError:
                if time.monotonic() >= deadline:
                    raise RuntimeError("Candidate not ready; traffic unchanged, candidate retained for diagnosis")
                time.sleep(1)
        if inspect(args.candidate)["Image"] != args.image:
            raise RuntimeError("Candidate image mismatch")
        if inspect(args.active)["Id"] != active["Id"] or config_path.read_text() != original:
            raise RuntimeError("Active deployment changed during preparation; traffic untouched")
        backup = Path(str(config_path) + ".before-" + args.candidate)
        with backup.open("x") as output:
            output.write(original)
        os.chmod(backup, config_path.stat().st_mode & 0o777)
        # Write in place: production may bind-mount this file's inode.
        try:
            config_path.write_text(proposed)
            run("docker", "exec", args.caddy, "caddy", "validate", "--config", "/etc/caddy/Caddyfile")
            run("docker", "exec", args.caddy, "caddy", "reload", "--config", "/etc/caddy/Caddyfile")
        except Exception:
            config_path.write_text(original)
            run("docker", "exec", args.caddy, "caddy", "reload", "--config", "/etc/caddy/Caddyfile")
            raise
        print(json.dumps({"switched": True, "rollback_config": str(backup),
                          "retained_container": args.active,
                          "next": "Verify public commit, HTTP/WS and daemon heartbeat; keep old server until drain + caller routes verified"}))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError) as error:
        raise SystemExit(str(error))
