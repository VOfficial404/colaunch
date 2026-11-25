#!/usr/bin/env python3
# Colaunch's agent, which the app puts on a Colab runtime. It needs only
# Python's standard library.
#
#   agent.py mux               tunnel TCP connections over stdin and stdout
#   agent.py watchdog          unassign the runtime when it's time, or idle
#   agent.py status            print the runtime's state as JSON
#   agent.py run ID SCRIPT     run a plugin's script in the background
#   agent.py stop ID           stop a plugin
#   agent.py logs ID [LINES]   print the end of a plugin's log
#   agent.py unassign REASON   give the runtime back to Colab now
#
# The watchdog runs on the runtime, so the limits hold with the app closed.

import base64
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time

VERSION = "1"
HOME = os.environ.get("COLAUNCH_HOME") or (
    "/content/colaunch" if os.path.isdir("/content") else os.path.expanduser("~/.colaunch"))
CONFIG = os.path.join(HOME, "watchdog.json")
STOPPED = os.path.join(HOME, "stopped.json")
ACTIVITY = os.path.join(HOME, "activity")
BUSY = os.path.join(HOME, "busy")
PLUGINS = os.path.join(HOME, "plugins")


def touch(path):
    try:
        with open(path, "a"):
            os.utime(path, None)
    except OSError:
        pass


def mtime(path):
    try:
        return os.path.getmtime(path)
    except OSError:
        return 0


def read_json(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return default


def write_json(path, value):
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(value, f)
    os.replace(tmp, path)


# ---------------------------------------------------------------- mux

class Mux:
    """Carries TCP connections to the runtime's ports as lines of text:

    app → agent   O id port | D id base64 | C id | P
    agent → app   K id | E id message | D id base64 | C id | P
    """

    def __init__(self):
        self.out = threading.Lock()
        self.socks = {}
        self.last_touch = 0

    def send(self, line):
        data = (line + "\n").encode()
        with self.out:
            view = memoryview(data)
            while view:
                n = os.write(1, view)
                view = view[n:]

    def active(self):
        now = time.time()
        if now - self.last_touch > 10:
            self.last_touch = now
            touch(ACTIVITY)

    def pump(self, cid, s):
        try:
            while True:
                b = s.recv(24576)
                if not b:
                    break
                self.active()
                self.send("D %s %s" % (cid, base64.b64encode(b).decode()))
        except OSError:
            pass
        if self.socks.pop(cid, None) is not None:
            self.send("C %s" % cid)
        try:
            s.close()
        except OSError:
            pass

    def open(self, cid, port):
        try:
            s = socket.create_connection(("127.0.0.1", int(port)), timeout=10)
            s.settimeout(None)
            s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        except (OSError, ValueError) as e:
            self.send("E %s %s" % (cid, str(e).replace("\n", " ")))
            return
        self.socks[cid] = s
        self.send("K %s" % cid)
        threading.Thread(target=self.pump, args=(cid, s), daemon=True).start()

    def run(self):
        os.makedirs(HOME, exist_ok=True)
        stdin = os.fdopen(0, "rb", buffering=0)
        buf = b""
        self.send("@@COLAUNCH-MUX-READY@@")
        while True:
            chunk = stdin.read(65536)
            if not chunk:
                break
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                self.handle(line.decode("ascii", "replace").strip())
        for s in list(self.socks.values()):
            s.close()

    def handle(self, line):
        if not line:
            return
        parts = line.split(" ", 2)
        op = parts[0]
        if op == "P":
            self.send("P")
        elif op == "O" and len(parts) == 3:
            threading.Thread(target=self.open, args=(parts[1], parts[2]), daemon=True).start()
        elif op == "D" and len(parts) == 3:
            s = self.socks.get(parts[1])
            if s is not None:
                self.active()
                try:
                    s.sendall(base64.b64decode(parts[2]))
                except (OSError, ValueError):
                    self.socks.pop(parts[1], None)
                    self.send("C %s" % parts[1])
        elif op == "C" and len(parts) >= 2:
            s = self.socks.pop(parts[1], None)
            if s is not None:
                try:
                    s.shutdown(socket.SHUT_RDWR)
                    s.close()
                except OSError:
                    pass


# ---------------------------------------------------------------- state

def gpus():
    if not shutil.which("nvidia-smi"):
        return []
    try:
        out = subprocess.run(
            ["nvidia-smi", "--query-gpu=name,memory.total,memory.used,utilization.gpu",
             "--format=csv,noheader,nounits"],
            capture_output=True, text=True, timeout=10).stdout
    except (OSError, subprocess.SubprocessError):
        return []
    result = []
    for line in out.strip().splitlines():
        f = [x.strip() for x in line.split(",")]
        if len(f) == 4:
            try:
                result.append({"name": f[0], "memory_total_mb": int(f[1]),
                               "memory_used_mb": int(f[2]), "utilization": int(f[3])})
            except ValueError:
                pass
    return result


def tpus():
    return len([d for d in os.listdir("/dev") if d.startswith("accel") or d.startswith("vfio")]) \
        if os.path.isdir("/dev") else 0


def meminfo():
    info = {}
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                k, v = line.split(":", 1)
                info[k] = int(v.split()[0]) * 1024
    except (OSError, ValueError):
        pass
    return {"total": info.get("MemTotal", 0), "available": info.get("MemAvailable", 0)}


def disk():
    path = "/content" if os.path.isdir("/content") else "/"
    try:
        u = shutil.disk_usage(path)
        return {"total": u.total, "free": u.free}
    except OSError:
        return {"total": 0, "free": 0}


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def plugin_state(pid_file):
    try:
        with open(pid_file) as f:
            pid = int(f.read().strip())
    except (OSError, ValueError):
        return None
    return {"pid": pid, "running": alive(pid)}


def status():
    plugins = {}
    if os.path.isdir(PLUGINS):
        for name in sorted(os.listdir(PLUGINS)):
            st = plugin_state(os.path.join(PLUGINS, name, "pid"))
            if st is not None:
                st["state"] = read_json(os.path.join(PLUGINS, name, "state.json"), {})
                plugins[name] = st
    cfg = read_json(CONFIG, {})
    wd = plugin_state(os.path.join(HOME, "watchdog.pid"))
    try:
        load = os.getloadavg()[0]
    except OSError:
        load = 0
    print(json.dumps({
        "version": VERSION,
        "time": time.time(),
        "gpus": gpus(),
        "tpu_devices": tpus(),
        "cpus": os.cpu_count(),
        "load": load,
        "memory": meminfo(),
        "disk": disk(),
        "plugins": plugins,
        "watchdog": {"running": bool(wd and wd["running"]), "config": cfg,
                     "last_activity": max(mtime(ACTIVITY), mtime(BUSY))},
        "stopped": read_json(STOPPED),
    }))


# ---------------------------------------------------------------- plugins

def run(pid_name, script):
    d = os.path.join(PLUGINS, pid_name)
    os.makedirs(d, exist_ok=True)
    stop(pid_name, quiet=True)
    log = open(os.path.join(d, "log.txt"), "ab", buffering=0)
    env = dict(os.environ, COLAUNCH_HOME=HOME, COLAUNCH_PLUGIN_DIR=d,
               COLAUNCH_BUSY=BUSY, COLAUNCH_ACTIVITY=ACTIVITY)
    p = subprocess.Popen(["bash", script], cwd=d, stdout=log, stderr=log,
                         stdin=subprocess.DEVNULL, env=env, start_new_session=True)
    with open(os.path.join(d, "pid"), "w") as f:
        f.write(str(p.pid))
    print(p.pid)


def stop(pid_name, quiet=False):
    st = plugin_state(os.path.join(PLUGINS, pid_name, "pid"))
    if st and st["running"]:
        try:
            os.killpg(st["pid"], signal.SIGTERM)
            for _ in range(50):
                if not alive(st["pid"]):
                    break
                time.sleep(0.1)
            if alive(st["pid"]):
                os.killpg(st["pid"], signal.SIGKILL)
        except OSError:
            pass
    if not quiet:
        print("stopped")


def logs(pid_name, lines=200):
    path = os.path.join(PLUGINS, pid_name, "log.txt")
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            size = f.tell()
            f.seek(max(0, size - 256 * 1024))
            data = f.read().decode("utf-8", "replace")
    except OSError:
        return
    # Progress bars redraw a line with \r: keep what each line ended as.
    out = [l.rsplit("\r", 1)[-1] for l in data.split("\n")]
    sys.stdout.write("\n".join(out[-lines:]))


# ---------------------------------------------------------------- watchdog

def runtime_addr():
    addr = os.environ.get("TBE_RUNTIME_ADDR")
    if addr:
        return addr
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            with open("/proc/%s/environ" % pid, "rb") as f:
                for kv in f.read().split(b"\0"):
                    if kv.startswith(b"TBE_RUNTIME_ADDR="):
                        return kv.split(b"=", 1)[1].decode()
        except OSError:
            continue
    return None


def unassign(reason):
    """Asks Colab to take the runtime back, as google.colab.runtime.unassign
    does."""
    write_json(STOPPED, {"reason": reason, "time": time.time()})
    addr = runtime_addr()
    if not addr:
        print("no runtime management service")
        return False
    import urllib.request
    try:
        req = urllib.request.Request("http://%s/unassign" % addr, data=b"", method="POST")
        urllib.request.urlopen(req, timeout=30).read()
        print("unassigned")
        return True
    except Exception as e:  # noqa: BLE001 - whatever stops it, say so
        print("unassign failed: %s" % e)
        return False


def busy_now():
    for g in gpus():
        if g["utilization"] >= 10:
            return True
    return time.time() - mtime(BUSY) < 120


def watchdog():
    os.makedirs(HOME, exist_ok=True)
    pid_file = os.path.join(HOME, "watchdog.pid")
    st = plugin_state(pid_file)
    if st and st["running"] and st["pid"] != os.getpid():
        print("already running")
        return
    if os.fork() > 0:
        print("started")
        return
    os.setsid()
    if os.fork() > 0:
        os._exit(0)
    with open(pid_file, "w") as f:
        f.write(str(os.getpid()))
    devnull = os.open(os.devnull, os.O_RDWR)
    for fd in (0, 1, 2):
        os.dup2(devnull, fd)
    touch(ACTIVITY)
    while True:
        cfg = read_json(CONFIG, {})
        now = time.time()
        reason = None
        if busy_now():
            touch(ACTIVITY)
        stop_at = cfg.get("stop_at") or 0
        budget_at = cfg.get("budget_at") or 0
        idle = cfg.get("idle_minutes") or 0
        if stop_at and now >= stop_at:
            reason = "schedule"
        elif budget_at and now >= budget_at:
            reason = "budget"
        elif idle and now - mtime(ACTIVITY) >= idle * 60:
            reason = "idle"
        if reason:
            if unassign(reason):
                return
            time.sleep(60)
            continue
        time.sleep(15)


def main():
    if len(sys.argv) < 2:
        print(__doc__ or "usage: agent.py mux|watchdog|status|run|stop|logs|unassign")
        sys.exit(2)
    cmd = sys.argv[1]
    if cmd == "mux":
        Mux().run()
    elif cmd == "watchdog":
        watchdog()
    elif cmd == "status":
        status()
    elif cmd == "run":
        run(sys.argv[2], sys.argv[3])
    elif cmd == "stop":
        stop(sys.argv[2])
    elif cmd == "logs":
        logs(sys.argv[2], int(sys.argv[3]) if len(sys.argv) > 3 else 200)
    elif cmd == "unassign":
        unassign(sys.argv[2] if len(sys.argv) > 2 else "manual")
    elif cmd == "version":
        print(VERSION)
    else:
        sys.exit("unknown command " + cmd)


if __name__ == "__main__":
    main()
