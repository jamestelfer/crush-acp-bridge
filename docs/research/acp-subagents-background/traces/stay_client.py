"""Minimal ACP v1 client: prompt once, stay connected, log everything, allow permissions."""
import json, subprocess, sys, threading, time

bridge, url, cwd, prompt, linger, log_path = sys.argv[1:7]
p = subprocess.Popen([bridge, "--crush-url", url], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                     stderr=open(log_path + ".stderr", "w"), text=True, bufsize=1)
log = open(log_path, "w")
t0 = time.monotonic()
pending = {}
nid = [0]

def send(obj):
    log.write(f"{time.monotonic()-t0:7.2f} >> {json.dumps(obj)}\n"); log.flush()
    p.stdin.write(json.dumps(obj) + "\n"); p.stdin.flush()

def request(method, params):
    nid[0] += 1
    ev = threading.Event(); pending[nid[0]] = [ev, None]
    send({"jsonrpc": "2.0", "id": nid[0], "method": method, "params": params})
    return pending[nid[0]]

def reader():
    for line in p.stdout:
        log.write(f"{time.monotonic()-t0:7.2f} << {line.strip()}\n"); log.flush()
        msg = json.loads(line)
        if "method" in msg and "id" in msg:
            if msg["method"] == "session/request_permission":
                opts = msg["params"]["options"]
                allow = next((o for o in opts if o["kind"] == "allow_once"), opts[0])
                send({"jsonrpc": "2.0", "id": msg["id"],
                      "result": {"outcome": {"outcome": "selected", "optionId": allow["optionId"]}}})
            else:
                send({"jsonrpc": "2.0", "id": msg["id"], "error": {"code": -32601, "message": "not supported"}})
        elif "id" in msg and msg["id"] in pending:
            pending[msg["id"]][1] = msg; pending[msg["id"]][0].set()

threading.Thread(target=reader, daemon=True).start()
r = request("initialize", {"protocolVersion": 1, "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False}, "terminal": False}})
r[0].wait(60)
r = request("session/new", {"cwd": cwd, "mcpServers": []}); r[0].wait(120)
sid = r[1]["result"]["sessionId"]
r = request("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": prompt}]}); r[0].wait(600)
log.write(f"{time.monotonic()-t0:7.2f} -- prompt returned; lingering {linger}s\n"); log.flush()
time.sleep(float(linger))
p.stdin.close(); p.wait(30)
log.write(f"{time.monotonic()-t0:7.2f} -- done\n")
