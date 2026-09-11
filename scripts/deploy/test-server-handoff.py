#!/usr/bin/env python3
"""Disposable real-Server/Caddy/Redis/Postgres exercise. No production config."""
import base64
import hashlib
import hmac
import importlib.util
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

spec = importlib.util.spec_from_file_location("handoff", Path(__file__).with_name("server-handoff.py"))
handoff = importlib.util.module_from_spec(spec)
spec.loader.exec_module(handoff)
run = handoff.run
prefix = "hyclv66-handoff-" + str(os.getpid())
network = prefix
containers = []
image = sys.argv[1]
candidate_image = sys.argv[2] if len(sys.argv) > 2 else image
secret = "isolated-handoff-test-only-secret"
uid = "00000000-0000-4000-8000-000000000001"
wid = "00000000-0000-4000-8000-000000000002"


def launch(name, *args):
    containers.append(name)
    return run("docker", "run", "-d", "--name", name, "--network", network, *args)


def wait_ready(name):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            handoff.ready(name)
            return
        except Exception:
            time.sleep(1)
    raise RuntimeError("isolated Server failed readiness")


def send(sock, payload, opcode=1):
    data = payload.encode()
    mask = os.urandom(4)
    length = len(data)
    header = bytes([128 | opcode, 128 | length]) if length < 126 else bytes([128 | opcode, 254]) + struct.pack("!H", length)
    sock.sendall(header + mask + bytes(value ^ mask[index % 4] for index, value in enumerate(data)))


def exact(sock, length):
    data = b""
    while len(data) < length:
        chunk = sock.recv(length - len(data))
        if not chunk:
            raise RuntimeError("WebSocket disconnected")
        data += chunk
    return data


def receive(sock):
    first, second = exact(sock, 2)
    length = second & 127
    if length == 126:
        length = struct.unpack("!H", exact(sock, 2))[0]
    elif length == 127:
        length = struct.unpack("!Q", exact(sock, 8))[0]
    if first & 15 == 8:
        raise RuntimeError("WebSocket unexpectedly closed")
    return first & 15, exact(sock, length)


def expect_pong(sock, payload):
    # Normal workspace events may be queued before the control-frame reply.
    for _ in range(100):
        opcode, data = receive(sock)
        if opcode == 10 and data == payload.encode():
            return
    raise RuntimeError("matching pong not received")


def mint_token():
    encode = lambda value: base64.urlsafe_b64encode(value).rstrip(b"=").decode()
    token = encode(b'{"alg":"HS256","typ":"JWT"}') + "." + encode(json.dumps({"sub": uid, "exp": int(time.time()) + 600}).encode())
    token += "." + encode(hmac.new(secret.encode(), token.encode(), hashlib.sha256).digest())
    return token


def ws(port):
    sock = socket.create_connection(("127.0.0.1", port), timeout=5)
    key = base64.b64encode(os.urandom(16)).decode()
    sock.sendall((f"GET /ws?workspace_id={wid} HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
    header = b""
    while not header.endswith(b"\r\n\r\n"):
        header += exact(sock, 1)
    assert b"101 Switching Protocols" in header
    token = mint_token()
    send(sock, json.dumps({"type": "auth", "payload": {"token": token}}))
    assert b"auth_ack" in receive(sock)[1]
    return sock


with tempfile.TemporaryDirectory(prefix=prefix) as temp:
    try:
        run("docker", "network", "create", network)
        pg, redis, blue, green, caddy = [prefix + "-" + role for role in ("pg", "redis", "blue", "green", "caddy")]
        launch(pg, "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=isolated", "-e", "POSTGRES_DB=handoff", "pgvector/pgvector:pg17")
        launch(redis, "-p", "127.0.0.1::6379", "redis:7.4-alpine", "redis-server", "--maxmemory-policy", "noeviction", "--requirepass", "isolated-redis")
        launch(blue, "-e", f"DATABASE_URL=postgres://postgres:isolated@{pg}:5432/handoff?sslmode=disable", "-e", f"REDIS_URL=redis://:isolated-redis@{redis}:6379/0", "-e", "JWT_SECRET=" + secret, "-e", "APP_ENV=production", image)
        wait_ready(blue)
        sql = f'''INSERT INTO "user" (id,name,email) VALUES ('{uid}','Handoff Test','handoff@example.invalid'); INSERT INTO workspace (id,name,slug,issue_prefix) VALUES ('{wid}','Handoff Test','handoff-test','TEST'); INSERT INTO member (workspace_id,user_id,role) VALUES ('{wid}','{uid}','admin');'''
        run("docker", "exec", pg, "psql", "-U", "postgres", "-d", "handoff", "-v", "ON_ERROR_STOP=1", "-c", sql)
        config = Path(temp) / "Caddyfile"
        config.write_text(f"http://:8081 {{\n reverse_proxy {blue}:8080 {{\n stream_close_delay 30s\n }}\n}}\n:8080 {{\n @backend path /health /readyz /ws /api/*\n reverse_proxy @backend {blue}:8080 {{\n stream_close_delay 30s\n }}\n}}\n")
        launch(caddy, "-p", "127.0.0.1::8081", "-p", "127.0.0.1::8080", "-v", f"{config}:/etc/caddy/Caddyfile:ro", "caddy:2.10.2-alpine")
        port = int(run("docker", "port", caddy, "8080").rsplit(":", 1)[1])
        url = f"http://127.0.0.1:{port}/health"
        localport = int(run("docker", "port", caddy, "8081").rsplit(":", 1)[1])
        localurl = f"http://127.0.0.1:{localport}/health"
        deadline = time.monotonic() + 15
        while True:
            try:
                before = json.load(urllib.request.urlopen(url, timeout=2))
                break
            except Exception:
                if time.monotonic() > deadline:
                    raise
                time.sleep(.1)
        wrong_config = Path(temp) / "not-mounted-Caddyfile"
        wrong_config.write_text(config.read_text())
        rejected = subprocess.run([sys.executable, str(Path(__file__).with_name("server-handoff.py")), "--active", blue, "--candidate", green, "--image", candidate_image, "--caddy", caddy, "--config", str(wrong_config), "--redis", redis], capture_output=True, text=True)
        assert rejected.returncode and "bind-mount source" in rejected.stderr, "wrong config path was not rejected"
        print("Wrong Caddyfile bind source: rejected before candidate startup")
        conn = ws(port)
        samples, failures = [], []
        stop = threading.Event()
        def probe():
            while not stop.wait(.02):
                try:
                    samples.append(json.load(urllib.request.urlopen(url, timeout=2)))
                    samples.append(json.load(urllib.request.urlopen(localurl, timeout=2)))
                except Exception as error:
                    failures.append(type(error).__name__)
        probe_thread = threading.Thread(target=probe)
        probe_thread.start()
        containers.append(green)
        try:
            output = run(sys.executable, str(Path(__file__).with_name("server-handoff.py")), "--active", blue, "--candidate", green, "--image", candidate_image, "--caddy", caddy, "--config", str(config), "--redis", redis, "--delay-seconds", "30", "--confirm-compatible", "--apply")
            print(output)
            send(conn, "survived-cutover", opcode=9)
            expect_pong(conn, "survived-cutover")
            after = json.load(urllib.request.urlopen(url, timeout=2))
            assert after["started_at"] != before["started_at"], "public route did not switch process"
            assert json.load(urllib.request.urlopen(localurl, timeout=2))["started_at"] == after["started_at"], "local API did not switch"
            # The POST reaches green; this authenticated socket is still on blue.
            request = urllib.request.Request(f"http://127.0.0.1:{port}/api/issues", data=json.dumps({"title": "Cross-node handoff event"}).encode(), headers={"Content-Type": "application/json", "Authorization": "Bearer " + mint_token(), "X-Workspace-ID": wid})
            created = json.load(urllib.request.urlopen(request, timeout=5))
            event = receive(conn)[1]
            assert created["id"].encode() in event, "old node did not receive new-node issue event"
            print("Cross-node issue event: new Server HTTP write reached old authenticated WebSocket through Redis")
            new_conn = ws(port)
            send(new_conn, "new-node", opcode=9)
            expect_pong(new_conn, "new-node")
            new_conn.close()
            # Exercise rollback using the saved config; existing old socket remains alive.
            config.write_text(Path(str(config) + ".before-" + green).read_text())
            run("docker", "exec", caddy, "caddy", "reload", "--config", "/etc/caddy/Caddyfile")
            rolled_back = json.load(urllib.request.urlopen(url, timeout=2))
            assert rolled_back["started_at"] == before["started_at"]
            assert json.load(urllib.request.urlopen(localurl, timeout=2))["started_at"] == before["started_at"]
            send(conn, "survived-rollback", opcode=9)
            expect_pong(conn, "survived-rollback")
        finally:
            stop.set()
            probe_thread.join()
            conn.close()
        assert samples and not failures, failures
        print(json.dumps({"http_samples": len(samples), "http_errors": len(failures), "old_authenticated_ws_survived_cutover_and_rollback": True, "new_authenticated_ws": True, "public_process_switched_and_rolled_back": True}))
        # Existing cross-instance tests use distinct objects and real Redis. Separate logical DB 15.
        pgport = run("docker", "port", pg, "5432").rsplit(":", 1)[1]
        redisport = run("docker", "port", redis, "6379").rsplit(":", 1)[1]
        environment = dict(os.environ, DATABASE_URL=f"postgres://postgres:isolated@127.0.0.1:{pgport}/handoff?sslmode=disable", REDIS_TEST_URL=f"redis://:isolated-redis@127.0.0.1:{redisport}/15")
        result = subprocess.run(["go", "test", "./internal/handler", "-run", "TestRedisModelListStore_(PopPendingAcrossInstances|CreateGetComplete|PopPendingConcurrent)$", "-count=1", "-v"], cwd=Path(__file__).resolve().parents[2] / "server", env=environment, text=True, capture_output=True)
        if result.returncode or "SKIP" in result.stdout or "Skipping" in result.stdout:
            raise RuntimeError("Redis cross-instance tests failed or skipped: " + result.stdout[-3000:])
        print(result.stdout[-2000:])
    finally:
        for container in reversed(containers):
            subprocess.run(["docker", "rm", "-f", "-v", container], capture_output=True)
        subprocess.run(["docker", "network", "rm", network], capture_output=True)
