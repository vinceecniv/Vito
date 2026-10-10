#!/usr/bin/env python3
"""Regenerates the store screenshots in packaging/screenshots/.

    go build -o /tmp/vito ./cmd/vito
    python3 scripts/screenshots.py /tmp/vito

Starts that binary in demo mode with its own throwaway config, data and cache
(port 4599, so an installed Vito keeps running), drives headless Firefox over
Marionette, saves one 1000x700 PNG per tab and shuts the test daemon down.
Needs Linux and Firefox; nothing else to install.

What a store visitor should not see is left out the way a user would leave it
out: the welcome card is dismissed, and Firefox's "can't install as an app" bar
is closed. The demo-mode banner is hidden only in the browser — it says
"sample data", which is true of the screenshot but not of the app.

Flathub's guidelines (docs.flathub.org, quality guidelines): English, default
light appearance, real content, at most 1000x700, captions in the metainfo.
"""
import base64, json, os, shutil, socket, subprocess, sys, tempfile, time, urllib.request

SHOTS = [("status", "dashboard"), ("history", "history"), ("dictionary", "dictionary")]
PORT, TOKEN, MARIONETTE = 4599, "screenshots", 2829
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "packaging", "screenshots")


def api(path, method="GET"):
    req = urllib.request.Request(f"http://127.0.0.1:{PORT}{path}", method=method,
                                 headers={"Authorization": f"Bearer {TOKEN}"})
    return urllib.request.urlopen(req, timeout=10).read()


def wait_port(port):
    for _ in range(60):
        try:
            return socket.create_connection(("127.0.0.1", port))
        except OSError:
            time.sleep(0.5)
    raise SystemExit(f"nothing listening on {port}")


def main():
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    binary = os.path.abspath(sys.argv[1])
    tmp = tempfile.mkdtemp(prefix="vito-screenshots-")
    env = dict(os.environ)
    for name in ("CONFIG", "DATA", "CACHE", "STATE"):
        env[f"XDG_{name}_HOME"] = os.path.join(tmp, name.lower())
        os.makedirs(env[f"XDG_{name}_HOME"])
    os.makedirs(os.path.join(env["XDG_CONFIG_HOME"], "vito"))
    with open(os.path.join(env["XDG_CONFIG_HOME"], "vito", "config.json"), "w") as f:
        json.dump({"server": {"port": PORT, "token": TOKEN}, "demo": True, "update": {"check": False}}, f)

    daemon = subprocess.Popen([binary, "serve"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    browser = None
    try:
        wait_port(PORT).close()
        api("/api/welcome-done", "POST")
        browser, cmd = firefox(tmp)
        shoot(cmd)
    finally:
        if browser:
            browser.terminate()
        try:
            api("/api/quit", "POST")
        except OSError:
            pass
        daemon.wait(timeout=10)
        shutil.rmtree(tmp, ignore_errors=True)


def firefox(tmp):
    prof = os.path.join(tmp, "firefox")
    os.makedirs(prof)
    with open(os.path.join(prof, "user.js"), "w") as f:
        for k, v in {"marionette.port": MARIONETTE, "intl.accept_languages": '"en-US"',
                     "intl.locale.requested": '"en-US"', "ui.systemUsesDarkTheme": 0,
                     "layout.css.prefers-color-scheme.content-override": 1}.items():
            f.write(f'user_pref("{k}", {v});\n')
    proc = subprocess.Popen(["firefox", "--headless", "--marionette", "--no-remote", "-profile", prof],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    sock, buf, mid = wait_port(MARIONETTE), [b""], [0]

    def read():
        while b":" not in buf[0]:
            buf[0] += sock.recv(65536)
        n, rest = buf[0].split(b":", 1)
        while len(rest) < int(n):
            rest += sock.recv(1 << 20)
        buf[0] = rest[int(n):]
        return json.loads(rest[:int(n)])

    def cmd(name, params=None):
        mid[0] += 1
        data = json.dumps([0, mid[0], name, params or {}]).encode()
        sock.sendall(str(len(data)).encode() + b":" + data)
        r = read()
        if r[2]:
            raise SystemExit(f"{name}: {r[2]}")
        return r[3]

    read()  # handshake
    return proc, cmd


def shoot(cmd):
    url = f"http://127.0.0.1:{PORT}/?token={TOKEN}"
    js = lambda code: cmd("WebDriver:ExecuteScript", {"script": code, "args": []})["value"]
    cmd("WebDriver:NewSession", {"capabilities": {}})
    cmd("WebDriver:SetWindowRect", {"width": 1000, "height": 700})
    cmd("WebDriver:Navigate", {"url": url})
    time.sleep(3)
    js('localStorage.setItem("vito-pwa-dismissed","1")')
    cmd("WebDriver:Navigate", {"url": url})
    time.sleep(3)
    vw, vh = js("return [innerWidth, innerHeight]")  # SetWindowRect sizes the outer window
    cmd("WebDriver:SetWindowRect", {"width": 2000 - vw, "height": 1400 - vh})
    js('const s=document.createElement("style"); s.textContent="#demo-banner{display:none!important}"; document.head.appendChild(s);')
    os.makedirs(OUT, exist_ok=True)
    for tab, name in SHOTS:
        js(f'document.querySelector(\'.nav-item[data-tab="{tab}"]\').click(); window.scrollTo(0,0);')
        time.sleep(2.5)
        with open(os.path.join(OUT, f"{name}.png"), "wb") as f:
            f.write(base64.b64decode(cmd("WebDriver:TakeScreenshot", {"full": False})["value"]))
        print("saved", os.path.normpath(os.path.join(OUT, f"{name}.png")))
    cmd("Marionette:Quit", {"flags": ["eForceQuit"]})


if __name__ == "__main__":
    main()
