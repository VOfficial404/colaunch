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
#   agent.py prepare ID        make a plugin's folder
#   agent.py private ID        make a new private folder for a plugin's secrets
#   agent.py seal PATH         make a file there readable by its owner alone
#
# The watchdog runs on the runtime, so the limits hold with the app closed.
# Its files are in folders only their owner can open, and the files it
# writes are made anew, exclusively, then renamed into place.

import base64
import json
import os
import re
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
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


def private_dir(path):
    """Makes path a folder only its owner can open, refusing a link or
    someone else's folder in its place."""
    os.makedirs(path, mode=0o700, exist_ok=True)
    st = os.lstat(path)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        sys.exit("%s isn't a folder" % path)
    if hasattr(os, "getuid") and st.st_uid != os.getuid():
        sys.exit("%s isn't ours" % path)
    if stat.S_IMODE(st.st_mode) != 0o700:
        os.chmod(path, 0o700)
    return path


def write_file(path, data):
    """Writes path anew: a file of its own, made exclusively beside it and
    readable by its owner alone, renamed into place."""
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix="." + os.path.basename(path) + ".")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(data)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def write_json(path, value):
    write_file(path, json.dumps(value))


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
        private_dir(HOME)
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


def proc_start(pid):
    """The time a process started, in clock ticks since boot, None when
    /proc doesn't say."""
    try:
        with open("/proc/%d/stat" % pid) as f:
            data = f.read()
        return int(data[data.rindex(")") + 2:].split()[19])
    except (OSError, ValueError, IndexError):
        return None


def proc_has(pid, name, item):
    """Whether /proc/PID/NAME, environ or cmdline, has item among its
    NUL-separated entries; None when it can't be read."""
    try:
        with open("/proc/%d/%s" % (pid, name), "rb") as f:
            return item.encode() in f.read().split(b"\0")
    except OSError:
        return None


def read_pid(pid_file):
    """The process a pid file names: {"pid", "start"}, or an older file's
    bare number."""
    try:
        with open(pid_file) as f:
            text = f.read().strip()
    except OSError:
        return None
    try:
        rec = json.loads(text)
    except ValueError:
        return None
    if isinstance(rec, int):
        rec = {"pid": rec}
    if not isinstance(rec, dict) or not isinstance(rec.get("pid"), int) or rec["pid"] <= 0:
        return None
    return rec


def write_pid(pid_file, pid):
    write_json(pid_file, {"pid": pid, "start": proc_start(pid)})


def owns(rec, proc_file, item):
    """Whether the process rec names is still the one started, not another
    that got its number since: it started when it did, and its proc_file
    (environ or cmdline) has item. True, False, or None where there's no
    /proc to tell."""
    pid = rec["pid"]
    if not alive(pid):
        return False
    if not os.path.isdir("/proc/self"):
        return None
    start = proc_start(pid)
    if start is None:
        return False
    if rec.get("start") is not None and start != rec["start"]:
        return False
    has = proc_has(pid, proc_file, item)
    if has is False:
        return False
    if has is None and rec.get("start") is None:
        return False
    return True


ID = re.compile(r"^[a-z0-9][a-z0-9_\-]{0,40}$")


def plugin_dir(name):
    if not ID.match(name):
        sys.exit("bad plugin id %r" % name)
    return os.path.join(PLUGINS, name)


def plugin_owns(name, rec):
    return owns(rec, "environ", "COLAUNCH_PLUGIN_DIR=" + plugin_dir(name))


def watchdog_owns(rec):
    return owns(rec, "cmdline", "watchdog")


def plugin_state(pid_file, check):
    rec = read_pid(pid_file)
    if rec is None:
        return None
    o = check(rec)
    return {"pid": rec["pid"], "running": alive(rec["pid"]) if o is None else o}


def status():
    plugins = {}
    if os.path.isdir(PLUGINS):
        for name in sorted(os.listdir(PLUGINS)):
            if not ID.match(name):
                continue
            st = plugin_state(os.path.join(PLUGINS, name, "pid"), lambda rec: plugin_owns(name, rec))
            if st is not None:
                st["state"] = read_json(os.path.join(PLUGINS, name, "state.json"), {})
                plugins[name] = st
    cfg = read_json(CONFIG, {})
    wd = plugin_state(os.path.join(HOME, "watchdog.pid"), watchdog_owns)
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

def prepare(pid_name):
    private_dir(HOME)
    private_dir(PLUGINS)
    return private_dir(plugin_dir(pid_name))


def private(pid_name):
    """Prints a new folder, only the owner's, for the secrets of a plugin's
    run."""
    d = private_dir(os.path.join(prepare(pid_name), "private"))
    print(tempfile.mkdtemp(dir=d))


def seal(path):
    """Makes a file put in a plugin's folder readable by its owner alone."""
    base = os.path.realpath(PLUGINS) + os.sep
    st = os.lstat(path)
    if not os.path.realpath(path).startswith(base) or not stat.S_ISREG(st.st_mode):
        sys.exit("%s isn't a plugin's file" % path)
    os.chmod(path, 0o600)
    print("sealed")


def run(pid_name, script):
    d = prepare(pid_name)
    stop(pid_name, quiet=True)
    flags = os.O_WRONLY | os.O_APPEND | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0)
    log = os.fdopen(os.open(os.path.join(d, "log.txt"), flags, 0o600), "ab", buffering=0)
    env = dict(os.environ, COLAUNCH_HOME=HOME, COLAUNCH_PLUGIN_DIR=d,
               COLAUNCH_BUSY=BUSY, COLAUNCH_ACTIVITY=ACTIVITY)
    p = subprocess.Popen(["bash", script], cwd=d, stdout=log, stderr=log,
                         stdin=subprocess.DEVNULL, env=env, start_new_session=True)
    write_pid(os.path.join(d, "pid"), p.pid)
    print(p.pid)


def stop(pid_name, quiet=False, forget=False):
    """Stops a plugin's processes, once sure its pid is still the plugin's.
    forget also removes what's left of its secrets."""
    d = plugin_dir(pid_name)
    rec = read_pid(os.path.join(d, "pid"))
    o = plugin_owns(pid_name, rec) if rec else False
    if o is None:
        print("can't tell whether process %d is still the plugin's: left running" % rec["pid"])
    elif o:
        try:
            os.killpg(rec["pid"], signal.SIGTERM)
            for _ in range(50):
                if not plugin_owns(pid_name, rec):
                    break
                time.sleep(0.1)
            if plugin_owns(pid_name, rec):
                os.killpg(rec["pid"], signal.SIGKILL)
        except OSError:
            pass
    if forget:
        shutil.rmtree(os.path.join(d, "private"), ignore_errors=True)
    if not quiet:
        print("stopped")


def logs(pid_name, lines=200):
    path = os.path.join(plugin_dir(pid_name), "log.txt")
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
    private_dir(HOME)
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
    private_dir(HOME)
    pid_file = os.path.join(HOME, "watchdog.pid")
    st = plugin_state(pid_file, watchdog_owns)
    if st and st["running"] and st["pid"] != os.getpid():
        print("already running")
        return
    if os.fork() > 0:
        print("started")
        return
    os.setsid()
    if os.fork() > 0:
        os._exit(0)
    write_pid(pid_file, os.getpid())
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
        stop(sys.argv[2], forget=True)
    elif cmd == "prepare":
        print(prepare(sys.argv[2]))
    elif cmd == "private":
        private(sys.argv[2])
    elif cmd == "seal":
        seal(sys.argv[2])
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
